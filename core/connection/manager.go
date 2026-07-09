package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// Manager
// -----------------------------------------------------------------------------

// Manager 管理所有 Target 的注册、持久化与凭据下发。
//
// 并发安全。它 **不** 负责底层协议（例如 crypto/ssh）的会话池——
// 会话池由具体插件（例：plugins/ssh）自行维护，Manager 只把凭据下发过去。
type Manager struct {
	log     *logger.Logger
	dataDir string
	path    string  // targets.json 完整路径
	sealer  *Sealer // 凭据加解密

	mu      sync.RWMutex
	targets map[string]*Target
}

// Options 是 Manager 构造参数。
type Options struct {
	// Logger 供 Manager 内部使用；nil 时使用 logger.Default()。
	Logger *logger.Logger

	// DataDir 是 MOW 数据根目录（一般为 config.AppConfig.DataDir）。
	// Manager 会在其下建立：
	//   <DataDir>/keys/master.key
	//   <DataDir>/connections/targets.json
	DataDir string

	// KeyStore 可选；未提供时使用 FileKeyStore(<DataDir>/keys/master.key)。
	KeyStore KeyStore
}

// NewManager 构造 Manager 并从磁盘加载已有 Target。
// 若 DataDir 为空，退化为纯内存模式（不落盘）。
func NewManager(opts Options) (*Manager, error) {
	log := opts.Logger
	if log == nil {
		log = logger.Default()
	}
	m := &Manager{
		log:     log.WithComponent("connection.manager"),
		dataDir: opts.DataDir,
		targets: map[string]*Target{},
	}
	var ks KeyStore = opts.KeyStore
	if ks == nil {
		if opts.DataDir == "" {
			return nil, errors.New("connection: DataDir or KeyStore required")
		}
		ks = NewFileKeyStore(filepath.Join(opts.DataDir, "keys", "master.key"))
	}
	m.sealer = NewSealer(ks)

	if opts.DataDir != "" {
		m.path = filepath.Join(opts.DataDir, "connections", "targets.json")
		if err := m.load(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// -----------------------------------------------------------------------------
// Target CRUD
// -----------------------------------------------------------------------------

// Upsert 新增或更新一个 Target。
// creds 若非 nil，会先做 Validate 再加密后落盘；若为 nil 则保留旧密文。
func (m *Manager) Upsert(t Target, creds any) error {
	if err := t.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	if creds != nil {
		if v, ok := creds.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return err
			}
		}
		raw, err := MarshalCredentials(creds)
		if err != nil {
			return fmt.Errorf("connection: marshal credentials: %w", err)
		}
		sealed, err := m.sealer.Seal(raw)
		if err != nil {
			return err
		}
		t.EncryptedCredentials = sealed
	} else if existing, ok := m.targets[t.ID]; ok {
		t.EncryptedCredentials = existing.EncryptedCredentials
	}

	if prev, ok := m.targets[t.ID]; ok {
		t.CreatedAt = prev.CreatedAt
	} else {
		t.CreatedAt = now
	}
	t.UpdatedAt = now

	tt := t
	m.targets[t.ID] = &tt
	if err := m.persist(); err != nil {
		return err
	}
	m.log.Info("target upserted", "id", t.ID, "type", t.Type)
	return nil
}

// Get 返回 Target 的副本；不返回凭据明文。
func (m *Manager) Get(id string) (Target, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.targets[id]
	if !ok {
		return Target{}, false
	}
	return *t, true
}

// List 返回按 ID 排序的所有 Target 副本。
func (m *Manager) List() []Target {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Target, 0, len(m.targets))
	for _, t := range m.targets {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Delete 删除一个 Target。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.targets[id]; !ok {
		return ErrTargetNotFound
	}
	delete(m.targets, id)
	if err := m.persist(); err != nil {
		return err
	}
	m.log.Info("target deleted", "id", id)
	return nil
}

// -----------------------------------------------------------------------------
// Open（下发凭据到 sdk.Connection）
// -----------------------------------------------------------------------------

// Open 为一次 Command 调用准备连接快照。
//
// 返回的 sdk.Connection.Credentials 是解密后的凭据明文（JSON），
// 只在进程内存在。插件不得写入日志。
//
// 注意：本函数只 **准备连接材料**，不建立底层协议会话。
// 真实的 TCP / TLS / SSH 握手在插件内完成（例如 plugins/ssh 的 SessionPool）。
func (m *Manager) Open(ctx context.Context, targetID string) (*sdk.Connection, error) {
	m.mu.RLock()
	t, ok := m.targets[targetID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrTargetNotFound
	}

	var credsJSON json.RawMessage
	if len(t.EncryptedCredentials) > 0 {
		pt, err := m.sealer.Open(t.EncryptedCredentials)
		if err != nil {
			return nil, fmt.Errorf("connection: open target %q: %w", targetID, err)
		}
		credsJSON = pt
	}

	return &sdk.Connection{
		ID:          targetID,
		Type:        string(t.Type),
		Credentials: credsJSON,
		Metadata:    t.MetadataMap(),
	}, nil
}

// -----------------------------------------------------------------------------
// 持久化
// -----------------------------------------------------------------------------

// diskFile 是 targets.json 的存储结构；版本号供未来迁移使用。
type diskFile struct {
	Version int       `json:"version"`
	Targets []*Target `json:"targets"`
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("connection: read targets: %w", err)
	}
	var f diskFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("connection: parse targets: %w", err)
	}
	for _, t := range f.Targets {
		if t == nil || t.ID == "" {
			continue
		}
		tt := *t
		m.targets[tt.ID] = &tt
	}
	m.log.Info("targets loaded", "count", len(m.targets), "path", m.path)
	return nil
}

func (m *Manager) persist() error {
	if m.path == "" {
		return nil
	}
	list := make([]*Target, 0, len(m.targets))
	for _, t := range m.targets {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	data, err := json.MarshalIndent(diskFile{Version: 1, Targets: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("connection: encode targets: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("connection: mkdir: %w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("connection: write targets: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("connection: rename targets: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	ErrTargetNotFound = errors.New("connection: target not found")
)
