package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	coreplugin "github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk/manifest"
	"github.com/spf13/cobra"
)

// -----------------------------------------------------------------------------
// mow plugin dev —— 本地热重载（v0.5.4 P1）
//
// 数据流：
//   1) stageDevPackage：把 <srcDir> 编译 + Manifest 重写到临时 staging 目录，
//      同 `plugin package`，但不打 tar.gz、不写 dist/。
//   2) 通过 core/plugin.Lifecycle 走 Install（首次）或 Update（后续）原子替换到
//      <PluginsDir>/<id>；成功后自动 Enable，方便直接 `mow run <id>.<cmd>`。
//   3) --watch 时轮询 <srcDir> 下所有 .go / plugin.json / recipes / workflows 的
//      mtime，出现变化就重跑 (1)+(2)；轮询间隔由 --interval 控制（默认 500ms）。
//
// 说明：不引 fsnotify —— 一个 dev 工具没必要拉一份跨平台原生依赖；轮询在
// 本地开发的中小型仓库里完全足够，并且天然规避 Windows 上的 fsnotify buggy
// 边界（EVENT-only、rename+create 顺序、隐藏文件等）。
// -----------------------------------------------------------------------------

// packageBuilder 抽象出「把源码 build 到 binOut」这一步；生产实现调 `go build`，
// 测试实现只写占位字节 —— 保证 plugin_watch_test.go 无需依赖 go 工具链。
type packageBuilder interface {
	Build(srcDir, binOut string, goos, goarch string) error
}

type goBuilder struct {
	ldflags  string
	trimpath bool
}

func (g goBuilder) Build(srcDir, binOut, goos, goarch string) error {
	return runGoBuild(srcDir, binOut, pluginPackageOpts{
		GOOS:     goos,
		GOARCH:   goarch,
		LDFlags:  g.ldflags,
		Trimpath: g.trimpath,
	})
}

// pluginDevOpts 由 CLI 层填充。testable，字段与 flag 一一对应。
type pluginDevOpts struct {
	Dir      string
	GOOS     string
	GOARCH   string
	Watch    bool
	Interval time.Duration
	LDFlags  string
	Trimpath bool

	// 测试注入点：替换默认的 goBuilder，让单测无需真实 `go build`。
	Builder packageBuilder
	// 测试注入点：替换 Lifecycle 目录（默认取 app.Cfg.App.PluginsDir）。
	PluginsDir string
	// 测试注入点：--watch 场景下达到 MaxCycles 后自动退出（0 表示无限循环）。
	MaxCycles int
	// 测试注入点：ticker 触发前的辅助 done channel（一次性通知退出）。
	Ctx context.Context
}

func newPluginDevCmd(holder *appHolder) *cobra.Command {
	var (
		dir      string
		goos     string
		goarch   string
		watch    bool
		interval time.Duration
		ldflags  string
		trimpath bool
	)
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Build the plugin from <dir> and install it into the local PluginsDir",
		Long: `Dev compiles the plugin entrypoint from <dir>, stages a packaged
layout (plugin.json + entrypoint with real checksum), then hands it to
core/plugin.Lifecycle to install (first run) or update (subsequent
runs) the plugin under <PluginsDir>/<id>. On success the plugin is
also enabled, so 'mow run <id>.<cmd>' works immediately.

With --watch the source tree is polled every --interval and the
above cycle is re-run whenever a *.go, plugin.json, recipes/**/*.yaml
or workflows/**/*.yaml file changes. Ctrl-C stops the loop.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginDev(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), holder, pluginDevOpts{
				Dir:      dir,
				GOOS:     goos,
				GOARCH:   goarch,
				Watch:    watch,
				Interval: interval,
				LDFlags:  ldflags,
				Trimpath: trimpath,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", ".", "plugin source directory")
	f.StringVar(&goos, "os", runtime.GOOS, "target GOOS (must match host for install)")
	f.StringVar(&goarch, "arch", runtime.GOARCH, "target GOARCH (must match host for install)")
	f.BoolVar(&watch, "watch", false, "keep watching source files and rebuild on change")
	f.DurationVar(&interval, "interval", 500*time.Millisecond, "poll interval when --watch is set")
	f.StringVar(&ldflags, "ldflags", "-s -w", "extra ldflags passed to 'go build'")
	f.BoolVar(&trimpath, "trimpath", true, "pass -trimpath to 'go build'")
	return cmd
}

// runPluginDev 是 CLI 入口；holder 允许注入自定义 PluginsDir 供测试使用。
func runPluginDev(ctx context.Context, stdout, stderr io.Writer, holder *appHolder, o pluginDevOpts) error {
	if o.Ctx != nil {
		ctx = o.Ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	if _, ok := allowedGOOS[o.GOOS]; !ok {
		return fmt.Errorf("unsupported GOOS %q", o.GOOS)
	}
	if _, ok := allowedGOARCH[o.GOARCH]; !ok {
		return fmt.Errorf("unsupported GOARCH %q", o.GOARCH)
	}
	// --watch 时禁止跨平台 —— Lifecycle.Install 会用 host 平台条目去
	// ValidatePackage，跨平台产物会立即失败。非 watch 场景（一次性 dev）也
	// 只在 host 平台安装才有意义。
	if o.GOOS != runtime.GOOS || o.GOARCH != runtime.GOARCH {
		return fmt.Errorf("plugin dev only supports host GOOS/GOARCH (%s/%s); got %s/%s",
			runtime.GOOS, runtime.GOARCH, o.GOOS, o.GOARCH)
	}
	if o.Interval <= 0 {
		o.Interval = 500 * time.Millisecond
	}
	if o.Builder == nil {
		o.Builder = goBuilder{ldflags: o.LDFlags, trimpath: o.Trimpath}
	}

	pluginsDir := o.PluginsDir
	if pluginsDir == "" {
		app, err := holder.Load()
		if err != nil {
			return err
		}
		pluginsDir = app.Cfg.App.PluginsDir
	}
	lifecycle, err := coreplugin.NewLifecycle(pluginsDir)
	if err != nil {
		return err
	}

	srcDir, err := filepath.Abs(o.Dir)
	if err != nil {
		return fmt.Errorf("resolve dir: %w", err)
	}

	// 一次性 build + install/update。
	first, err := devCycle(stdout, stderr, srcDir, lifecycle, o)
	if err != nil {
		return err
	}

	if !o.Watch {
		return nil
	}

	fmt.Fprintf(stdout, "watching %s (interval=%s); press Ctrl-C to stop.\n", srcDir, o.Interval)
	return devWatchLoop(ctx, stdout, stderr, srcDir, lifecycle, o, first.mtime)
}

// devInstallation 记录一轮 cycle 的结果，供 watch loop 决定后续 Install/Update。
type devInstallation struct {
	id    string
	mtime time.Time
}

// devCycle：staging → Install(首次)/Update(后续)。返回本轮 sourceMtime，供
// watcher 决定下轮是否触发。
func devCycle(stdout, stderr io.Writer, srcDir string, lifecycle *coreplugin.Lifecycle, o pluginDevOpts) (devInstallation, error) {
	mtime, err := latestSourceMtime(srcDir)
	if err != nil {
		return devInstallation{}, err
	}
	staging, m, err := stageDevPackage(stdout, srcDir, o)
	if err != nil {
		return devInstallation{}, err
	}
	defer os.RemoveAll(staging)

	installed := isInstalled(lifecycle, m.ID)
	var inst coreplugin.Installation
	if installed {
		inst, err = lifecycle.Update(staging)
	} else {
		inst, err = lifecycle.Install(staging)
	}
	if err != nil {
		fmt.Fprintf(stderr, "dev: install failed: %v\n", err)
		return devInstallation{}, err
	}

	// 自动 Enable —— dev 场景下用户期望改完就能 `mow run`。
	if _, err := lifecycle.SetEnabled(inst.ID, true); err != nil {
		fmt.Fprintf(stderr, "dev: enable failed: %v\n", err)
		return devInstallation{}, err
	}

	verb := "installed"
	if installed {
		verb = "updated"
	}
	fmt.Fprintf(stdout, "%s %s@%s (enabled).\n", verb, inst.ID, inst.Version)
	return devInstallation{id: inst.ID, mtime: mtime}, nil
}

// isInstalled 通过尝试加载 <PluginsDir>/<id>/plugin.json 判断插件是否已安装。
// 这里刻意不暴露 Lifecycle 内部字段，靠 manifest.Load 语义等价复用。
func isInstalled(l *coreplugin.Lifecycle, id string) bool {
	items, err := l.List()
	if err != nil {
		return false
	}
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

// stageDevPackage 与 runPluginPackage 的 stage 阶段等价：编译 host 平台
// entrypoint 到临时目录，然后重写 plugin.json 的 platforms[] 只保留 host 条目
// 并注入真实 checksum。返回 staging 目录路径与解析后的 Manifest。
func stageDevPackage(stdout io.Writer, srcDir string, o pluginDevOpts) (string, *manifest.Manifest, error) {
	m, err := manifest.Load(srcDir)
	if err != nil {
		return "", nil, err
	}
	var target *manifest.Platform
	for i := range m.Platforms {
		if m.Platforms[i].OS == o.GOOS && m.Platforms[i].Arch == o.GOARCH {
			p := m.Platforms[i]
			target = &p
			break
		}
	}
	if target == nil {
		return "", nil, fmt.Errorf("plugin.json has no platforms entry for %s/%s", o.GOOS, o.GOARCH)
	}

	rawManifest, err := os.ReadFile(filepath.Join(srcDir, manifest.ManifestFileName))
	if err != nil {
		return "", nil, fmt.Errorf("read plugin.json: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rawManifest, &doc); err != nil {
		return "", nil, fmt.Errorf("decode plugin.json: %w", err)
	}

	staging, err := os.MkdirTemp("", fmt.Sprintf(".mow-dev-%s-", m.ID))
	if err != nil {
		return "", nil, fmt.Errorf("mkdir staging: %w", err)
	}
	// 出错时兜底清理，避免临时目录堆积。
	cleanupOnErr := staging
	defer func() {
		if cleanupOnErr != "" {
			_ = os.RemoveAll(cleanupOnErr)
		}
	}()

	entryRel := filepath.FromSlash(target.Entrypoint)
	binOut := filepath.Join(staging, entryRel)
	if err := os.MkdirAll(filepath.Dir(binOut), 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir entrypoint dir: %w", err)
	}
	fmt.Fprintf(stdout, "dev: building %s/%s → %s\n", o.GOOS, o.GOARCH, target.Entrypoint)
	if err := o.Builder.Build(srcDir, binOut, o.GOOS, o.GOARCH); err != nil {
		return "", nil, err
	}
	sum, err := hashFileSHA256Dev(binOut)
	if err != nil {
		return "", nil, fmt.Errorf("hash entrypoint: %w", err)
	}
	target.Checksum = "sha256:" + sum
	newPlatforms, _ := json.Marshal([]manifest.Platform{*target})
	doc["platforms"] = newPlatforms

	// 保留 recipes / workflows：Manifest 校验会检查它们的 path 存在，因此
	// 需要把源码里的这两个目录整体拷进 staging（若存在）。
	for _, rel := range []string{"recipes", "workflows", "schemas"} {
		src := filepath.Join(srcDir, rel)
		if _, err := os.Stat(src); err == nil {
			if err := copyDir(src, filepath.Join(staging, rel)); err != nil {
				return "", nil, fmt.Errorf("copy %s: %w", rel, err)
			}
		}
	}

	outManifest, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode plugin.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, manifest.ManifestFileName), append(outManifest, '\n'), 0o644); err != nil {
		return "", nil, fmt.Errorf("write plugin.json: %w", err)
	}

	cleanupOnErr = ""
	return staging, m, nil
}

// hashFileSHA256Dev 与 plugin_dev.go 中同名 helper 等价；此处独立命名避免
// 与已有 hashFileSHA256 混淆（两者签名一致，行为相同）。
func hashFileSHA256Dev(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyDir 递归拷贝 src 到 dst；符号链接会被跳过（避免路径穿越）。
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode()&os.ModeSymlink != 0:
			return nil // 静默跳过 symlink
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, in)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
	})
}

// watchExts 是触发重建的源码扩展名白名单；带 dot。
// plugin.json 单独走文件名比对，见 latestSourceMtime。
var watchExts = map[string]struct{}{
	".go":   {},
	".yaml": {},
	".yml":  {},
	".json": {},
	".mod":  {}, // go.mod
	".sum":  {}, // go.sum
}

// latestSourceMtime 递归扫描 <srcDir>，返回被跟踪文件的最大 mtime。
// 遇到隐藏目录（. 开头）与 vendor/ 会跳过；文件按 watchExts 白名单过滤。
func latestSourceMtime(srcDir string) (time.Time, error) {
	var latest time.Time
	err := filepath.Walk(srcDir, func(path string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := info.Name()
		if info.IsDir() {
			if path == srcDir {
				return nil
			}
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "dist" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(name)
		if _, ok := watchExts[ext]; !ok {
			return nil
		}
		if mt := info.ModTime(); mt.After(latest) {
			latest = mt
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return latest, nil
}

// devWatchLoop 轮询源码变更；last 是上一轮拿到的 mtime。
// ctx 取消或 MaxCycles 达到时优雅退出。
func devWatchLoop(ctx context.Context, stdout, stderr io.Writer, srcDir string, lifecycle *coreplugin.Lifecycle, o pluginDevOpts, last time.Time) error {
	ticker := time.NewTicker(o.Interval)
	defer ticker.Stop()

	cycles := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		mt, err := latestSourceMtime(srcDir)
		if err != nil {
			fmt.Fprintf(stderr, "dev: scan source failed: %v\n", err)
			continue
		}
		if !mt.After(last) {
			continue
		}
		fmt.Fprintf(stdout, "dev: change detected at %s\n", mt.Format(time.RFC3339Nano))
		res, err := devCycle(stdout, stderr, srcDir, lifecycle, o)
		if err != nil {
			// build 失败不退出：允许用户改回来继续；单测通过 MaxCycles=1 精确控制退出。
			last = mt
			if o.MaxCycles > 0 {
				cycles++
				if cycles >= o.MaxCycles {
					return err
				}
			}
			continue
		}
		last = res.mtime
		if o.MaxCycles > 0 {
			cycles++
			if cycles >= o.MaxCycles {
				return nil
			}
		}
	}
}

// pluginDevValidateID 保留以防未来对 --dir 派生的 id 进行额外校验。
// 现在暂通过 manifest.Load 内部校验完成，无需在此实现。
var _ = errors.New // 保留 errors 引用（未来错误码扩展时会用到）
