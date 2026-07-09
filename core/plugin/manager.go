// Package plugin 实现 MOW 的 Plugin 生命周期管理（stub 版）。
//
// v0.1 stub 目标：
//   - 定义 Manager 的公开 API（Register / Enable / Disable / Get / List / Shutdown）
//   - 支持"进程内插件"用于单元测试与自举场景（不依赖 gRPC）
//   - 与 go-plugin 的对接留给下一步（sdk/internal/grpcbridge 就绪后接入）
//
// 详见 docs/plugin-system.md。
package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/sdk"
)

// State 表示插件在生命周期中的状态。
type State int

const (
	StateRegistered State = iota // 已注册但未启用
	StateEnabled                 // 已启用，可接受调用
	StateDisabled                // 已停用
	StateFailed                  // 启用失败
)

func (s State) String() string {
	switch s {
	case StateRegistered:
		return "registered"
	case StateEnabled:
		return "enabled"
	case StateDisabled:
		return "disabled"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Entry 是 Manager 中一个插件的运行时视图。
type Entry struct {
	ID       string
	Metadata sdk.Metadata
	State    State
	Err      error // 仅当 State = StateFailed 时非 nil
}

// Manager 管理所有已注册插件的生命周期。
// 并发安全。
type Manager struct {
	mu      sync.RWMutex
	log     *logger.Logger
	dataDir string
	items   map[string]*item
}

// item 是 Manager 内部维护的完整状态（不对外暴露）。
type item struct {
	plugin  sdk.Plugin
	meta    sdk.Metadata
	state   State
	err     error
	cmdByID map[string]sdk.CommandHandler // command_id → handler
}

// Options 是 Manager 的构造参数。
type Options struct {
	// Logger 供 Manager 内部使用；nil 时使用 logger.Default()。
	Logger *logger.Logger
	// DataDir 供插件持久化数据的根目录。
	DataDir string
}

// NewManager 构造一个空的 Manager。
func NewManager(opts Options) *Manager {
	log := opts.Logger
	if log == nil {
		log = logger.Default()
	}
	return &Manager{
		log:     log.WithComponent("plugin.manager"),
		dataDir: opts.DataDir,
		items:   map[string]*item{},
	}
}

// Register 将 sdk.Plugin 注册到 Manager，但不会立即启用。
// 注册阶段会调用 Plugin.Metadata 并做基本校验。
func (m *Manager) Register(p sdk.Plugin) error {
	if err := sdk.Validate(p); err != nil {
		return fmt.Errorf("plugin: validate: %w", err)
	}
	meta := p.Metadata()

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, dup := m.items[meta.ID]; dup {
		return fmt.Errorf("plugin: duplicate id %q", meta.ID)
	}

	cmds := map[string]sdk.CommandHandler{}
	for _, h := range p.Commands() {
		cmds[h.Spec().ID] = h
	}

	m.items[meta.ID] = &item{
		plugin:  p,
		meta:    meta,
		state:   StateRegistered,
		cmdByID: cmds,
	}
	m.log.Info("registered",
		"id", meta.ID,
		"version", meta.Version,
		"commands", len(cmds),
	)
	return nil
}

// Enable 启用一个已注册的插件（幂等）。
// req.Settings 通常来自 config.PluginConfig.Settings。
func (m *Manager) Enable(ctx context.Context, id string, req sdk.InitRequest) error {
	m.mu.Lock()
	it, ok := m.items[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}

	if it.state == StateEnabled {
		return nil
	}

	if err := it.plugin.Init(ctx, req); err != nil {
		m.mu.Lock()
		it.state = StateFailed
		it.err = err
		m.mu.Unlock()
		m.log.Error("enable failed", "id", id, "err", err)
		return fmt.Errorf("plugin: init %q: %w", id, err)
	}

	m.mu.Lock()
	it.state = StateEnabled
	it.err = nil
	m.mu.Unlock()
	m.log.Info("enabled", "id", id)
	return nil
}

// Disable 停用一个插件（幂等）。
func (m *Manager) Disable(ctx context.Context, id string) error {
	m.mu.Lock()
	it, ok := m.items[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if it.state != StateEnabled {
		return nil
	}

	if err := it.plugin.Shutdown(ctx); err != nil {
		m.log.Warn("shutdown reported error", "id", id, "err", err)
	}

	m.mu.Lock()
	it.state = StateDisabled
	m.mu.Unlock()
	m.log.Info("disabled", "id", id)
	return nil
}

// Shutdown 停用所有插件；用于进程退出前调用。
func (m *Manager) Shutdown(ctx context.Context) error {
	var firstErr error
	for _, id := range m.List() {
		if err := m.Disable(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Get 返回插件运行时视图。
func (m *Manager) Get(id string) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	it, ok := m.items[id]
	if !ok {
		return Entry{}, false
	}
	return Entry{ID: id, Metadata: it.meta, State: it.state, Err: it.err}, true
}

// List 返回所有已注册插件的 ID（按字母序）。
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.items))
	for id := range m.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Command 根据 pluginID.commandID 查找 handler。
// 未来 Command Engine 将成为唯一调用方，此方法保持内部导出但对外收敛。
func (m *Manager) Command(pluginID, commandID string) (sdk.CommandHandler, error) {
	m.mu.RLock()
	it, ok := m.items[pluginID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if it.state != StateEnabled {
		return nil, fmt.Errorf("plugin %q not enabled (state=%s)", pluginID, it.state)
	}
	h, ok := it.cmdByID[commandID]
	if !ok {
		return nil, ErrCommandNotFound
	}
	return h, nil
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	ErrNotFound        = errors.New("plugin: not found")
	ErrCommandNotFound = errors.New("plugin: command not found")
)
