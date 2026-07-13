package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mow/mow/sdk/manifest"
)

const lifecycleStateDir = ".state"

var lifecycleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

type Installation struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	Enabled   bool      `json:"enabled"`
	Installed time.Time `json:"installed_at"`
}

type Diagnostic struct {
	Installation
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type Lifecycle struct {
	dir string
	now func() time.Time
}

func NewLifecycle(pluginsDir string) (*Lifecycle, error) {
	if strings.TrimSpace(pluginsDir) == "" {
		return nil, errors.New("plugin lifecycle: plugins directory is empty")
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, fmt.Errorf("plugin lifecycle: create plugins directory: %w", err)
	}
	return &Lifecycle{dir: pluginsDir, now: time.Now}, nil
}

func (l *Lifecycle) Install(source string) (Installation, error) {
	mf, err := manifest.Load(source)
	if err != nil {
		return Installation{}, err
	}
	if _, err := manifest.ValidatePackage(source); err != nil {
		return Installation{}, err
	}
	sourceDir, err := packageDirectory(source)
	if err != nil {
		return Installation{}, err
	}
	destination := filepath.Join(l.dir, mf.ID)
	if _, err := os.Stat(destination); err == nil {
		return Installation{}, fmt.Errorf("plugin %q is already installed", mf.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Installation{}, fmt.Errorf("stat installed plugin %q: %w", mf.ID, err)
	}

	staging, err := os.MkdirTemp(l.dir, ".install-"+mf.ID+"-")
	if err != nil {
		return Installation{}, fmt.Errorf("create install staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := copyPackage(sourceDir, staging); err != nil {
		return Installation{}, err
	}
	if _, err := manifest.ValidatePackage(staging); err != nil {
		return Installation{}, fmt.Errorf("validate staged plugin: %w", err)
	}
	if err := os.Rename(staging, destination); err != nil {
		return Installation{}, fmt.Errorf("activate plugin %q: %w", mf.ID, err)
	}

	item := Installation{ID: mf.ID, Version: mf.Version, Enabled: false, Installed: l.now().UTC()}
	if err := l.writeState(item); err != nil {
		_ = os.RemoveAll(destination)
		return Installation{}, err
	}
	return item, nil
}

// Uninstall 删除已安装的插件目录。默认保留 .state/<id>.json，方便再次安装时
// 恢复启用状态；调用方可通过 purge=true 显式清除状态数据。
func (l *Lifecycle) Uninstall(id string, purge bool) error {
	if !lifecycleIDPattern.MatchString(id) {
		return fmt.Errorf("invalid plugin id %q", id)
	}
	destination := filepath.Join(l.dir, id)
	info, err := os.Stat(destination)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("plugin %q is not installed", id)
		}
		return fmt.Errorf("stat installed plugin %q: %w", id, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("plugin %q installation is not a directory", id)
	}

	// 先将目录 rename 到临时位置，rename 失败时不留半成品。
	trash, err := os.MkdirTemp(l.dir, ".uninstall-"+id+"-")
	if err != nil {
		return fmt.Errorf("create uninstall staging directory: %w", err)
	}
	// 空临时目录不能作为 rename 目标（需要目标不存在或为空的父目录），因此先移除自己。
	if err := os.Remove(trash); err != nil {
		return fmt.Errorf("prepare uninstall staging: %w", err)
	}
	if err := os.Rename(destination, trash); err != nil {
		return fmt.Errorf("detach plugin %q: %w", id, err)
	}
	if err := os.RemoveAll(trash); err != nil {
		return fmt.Errorf("remove plugin %q: %w", id, err)
	}

	if purge {
		statePath := l.statePath(id)
		if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("purge plugin state %q: %w", id, err)
		}
		_ = os.Remove(statePath + ".bak")
	}
	return nil
}

// Update 使用 source 处的新版本替换已安装的插件，遵循先校验→原子替换→
// 失败回退的顺序。启用状态在升级过程中保持不变。
func (l *Lifecycle) Update(source string) (Installation, error) {
	mf, err := manifest.Load(source)
	if err != nil {
		return Installation{}, err
	}
	if _, err := manifest.ValidatePackage(source); err != nil {
		return Installation{}, err
	}
	sourceDir, err := packageDirectory(source)
	if err != nil {
		return Installation{}, err
	}
	destination := filepath.Join(l.dir, mf.ID)
	if _, err := manifest.Load(destination); err != nil {
		return Installation{}, fmt.Errorf("plugin %q is not installed: %w", mf.ID, err)
	}

	staging, err := os.MkdirTemp(l.dir, ".update-"+mf.ID+"-")
	if err != nil {
		return Installation{}, fmt.Errorf("create update staging directory: %w", err)
	}
	stagingCleanup := staging
	defer func() {
		if stagingCleanup != "" {
			_ = os.RemoveAll(stagingCleanup)
		}
	}()
	if err := copyPackage(sourceDir, staging); err != nil {
		return Installation{}, err
	}
	if _, err := manifest.ValidatePackage(staging); err != nil {
		return Installation{}, fmt.Errorf("validate staged plugin: %w", err)
	}

	// 备份现有目录：先 rename 到同级 .backup 目录，为失败时快速回退准备。
	backup, err := os.MkdirTemp(l.dir, ".update-backup-"+mf.ID+"-")
	if err != nil {
		return Installation{}, fmt.Errorf("create update backup directory: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return Installation{}, fmt.Errorf("prepare update backup: %w", err)
	}
	if err := os.Rename(destination, backup); err != nil {
		return Installation{}, fmt.Errorf("backup plugin %q: %w", mf.ID, err)
	}

	// 原子替换：将 staging 目录 rename 到目的地。
	if err := os.Rename(staging, destination); err != nil {
		// 回退：尝试恢复备份。
		if restoreErr := os.Rename(backup, destination); restoreErr != nil {
			return Installation{}, fmt.Errorf("activate updated plugin %q: %w (rollback failed: %v)", mf.ID, err, restoreErr)
		}
		return Installation{}, fmt.Errorf("activate updated plugin %q: %w", mf.ID, err)
	}
	stagingCleanup = ""

	// 至此新版本已就位。若 state 更新失败，回退整个升级。
	item, err := l.readState(mf.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		l.rollbackUpdate(destination, backup)
		return Installation{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		item = Installation{ID: mf.ID, Installed: l.now().UTC()}
	}
	item.ID = mf.ID
	item.Version = mf.Version
	if err := l.writeState(item); err != nil {
		l.rollbackUpdate(destination, backup)
		return Installation{}, err
	}

	// 一切正常，清理备份。
	if err := os.RemoveAll(backup); err != nil {
		return Installation{}, fmt.Errorf("cleanup update backup: %w", err)
	}
	return item, nil
}

// rollbackUpdate 在原子替换成功之后但后续步骤失败时使用，尝试删除新版本
// 并把备份 rename 回原位置。best-effort：失败仅记录到返回错误链之外。
func (l *Lifecycle) rollbackUpdate(destination, backup string) {
	_ = os.RemoveAll(destination)
	_ = os.Rename(backup, destination)
}

func (l *Lifecycle) List() ([]Installation, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	items := make([]Installation, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		mf, loadErr := manifest.Load(filepath.Join(l.dir, entry.Name()))
		if loadErr != nil {
			continue
		}
		item, stateErr := l.readState(mf.ID)
		if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
			return nil, stateErr
		}
		if errors.Is(stateErr, os.ErrNotExist) {
			item = Installation{ID: mf.ID, Version: mf.Version}
		}
		item.Version = mf.Version
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (l *Lifecycle) SetEnabled(id string, enabled bool) (Installation, error) {
	if !lifecycleIDPattern.MatchString(id) {
		return Installation{}, fmt.Errorf("invalid plugin id %q", id)
	}
	mf, err := manifest.Load(filepath.Join(l.dir, id))
	if err != nil {
		return Installation{}, fmt.Errorf("plugin %q is not installed: %w", id, err)
	}
	if _, err := manifest.ValidatePackage(filepath.Join(l.dir, id)); err != nil {
		return Installation{}, err
	}
	item, err := l.readState(id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Installation{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		item = Installation{ID: id, Version: mf.Version, Installed: l.now().UTC()}
	}
	item.Version = mf.Version
	item.Enabled = enabled
	if err := l.writeState(item); err != nil {
		return Installation{}, err
	}
	return item, nil
}

func (l *Lifecycle) IsEnabled(id string) (bool, bool, error) {
	if !lifecycleIDPattern.MatchString(id) {
		return false, false, fmt.Errorf("invalid plugin id %q", id)
	}
	item, err := l.readState(id)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return item.Enabled, true, nil
}

func (l *Lifecycle) Doctor() ([]Diagnostic, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Diagnostic, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		item := Installation{ID: entry.Name()}
		if state, stateErr := l.readState(entry.Name()); stateErr == nil {
			item = state
		}
		if mf, loadErr := manifest.Load(filepath.Join(l.dir, entry.Name())); loadErr == nil {
			item.ID = mf.ID
			item.Version = mf.Version
		}
		d := Diagnostic{Installation: item, OK: true}
		if _, err := manifest.ValidatePackage(filepath.Join(l.dir, entry.Name())); err != nil {
			d.OK = false
			d.Error = err.Error()
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func packageDirectory(source string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return source, nil
	}
	return filepath.Dir(source), nil
}

func copyPackage(source, destination string) error {
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer destinationRoot.Close()

	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin package contains unsupported symlink %q", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel == "." {
				return nil
			}
			return destinationRoot.MkdirAll(rel, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		in, err := sourceRoot.Open(rel)
		if err != nil {
			return err
		}
		out, err := destinationRoot.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		inCloseErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return inCloseErr
	})
}

func (l *Lifecycle) statePath(id string) string {
	return filepath.Join(l.dir, lifecycleStateDir, id+".json")
}

func (l *Lifecycle) readState(id string) (Installation, error) {
	data, err := os.ReadFile(l.statePath(id))
	if err != nil {
		return Installation{}, err
	}
	var item Installation
	if err := json.Unmarshal(data, &item); err != nil {
		return Installation{}, fmt.Errorf("decode plugin state %q: %w", id, err)
	}
	return item, nil
}

func (l *Lifecycle) writeState(item Installation) error {
	dir := filepath.Join(l.dir, lifecycleStateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create plugin state directory: %w", err)
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	target := l.statePath(item.ID)
	backup := target + ".bak"
	_ = os.Remove(backup)
	if err := os.Rename(target, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.Remove(backup)
	return nil
}
