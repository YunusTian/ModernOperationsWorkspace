// Package main 桌面客户端后端。
//
// App 是 Wails 绑定的对象；所有 UI 通过它调用 core/command 与 core/connection。
// 一个 App 实例贯穿整个进程生命周期，OnStartup 时绑定 wails ctx。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	coreai "github.com/mow/mow/core/ai"
	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/connection"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/core/workflow/history"
	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/version"
)

// App 是绑定到 Wails 前端的顶层对象。
// 前端通过 window.go.main.App.<Method> 调用这些方法。
type App struct {
	log     *logger.Logger
	cfg     config.Config
	connMgr *connection.Manager
	plugMgr *plugin.Manager
	engine  *command.Engine
	history *history.JSONLStore

	ctxMu sync.RWMutex
	ctx   context.Context

	loadedMu sync.Mutex
	loaded   []func()
	enabled  map[string]bool

	shells sync.Map // sessionID -> *shellSession
	shellN atomic.Int64

	// docker.logs 流式会话
	dockerLogs sync.Map // sessionID -> *dockerLogsSession

	// docker.exec 流式会话（v0.3 第三阶段 Dashboard）
	dockerExecs sync.Map // sessionID -> *dockerExecSession

	// AI 流式对话会话
	aiChats sync.Map // sessionID -> *aiChatSession
	aiN     atomic.Int64

	// AI orchestrator 懒加载，仅供非流式 AIAsk 使用；流式 chat_stream 仍走 engine。
	aiOrchOnce sync.Once
	aiOrch     *coreai.Orchestrator
	aiOrchErr  error

	// Workflow 侧的共享注册表；惰性构造，见 workflow.go: workflowRecipes()。
	wfMu  sync.Mutex
	wfReg *recipe.Registry

	// Catalog 客户端懒加载（v0.5.1 P1）：ListCatalogSources / RefreshCatalog /
	// SearchCatalog / InstallPluginFromCatalog 等方法共享。
	catalogStateMu sync.Mutex
	catalogSt      *catalogState
}

// Version returns the application version injected from the repository-wide
// version package. Wails exposes it to the frontend through the App binding.
func (a *App) Version() string { return version.Version }

// NewApp 装配 Logger / Config / ConnMgr / PluginManager / Engine。
func NewApp() (*App, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.App.DataDir == "" {
		home, _ := os.UserHomeDir()
		cfg.App.DataDir = filepath.Join(home, ".mow")
	}
	if err := os.MkdirAll(cfg.App.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir data_dir: %w", err)
	}

	log := logger.Init(logger.Options{
		Level:  cfg.Logger.Level,
		Format: logger.Format(cfg.Logger.Format),
	}).WithComponent("desktop")

	connMgr, err := connection.NewManager(connection.Options{
		Logger:  log,
		DataDir: cfg.App.DataDir,
	})
	if err != nil {
		return nil, fmt.Errorf("connection manager: %w", err)
	}
	plugMgr := plugin.NewManager(plugin.Options{Logger: log, DataDir: cfg.App.DataDir})

	engine := command.New(command.Options{
		Manager:  plugMgr,
		Logger:   log,
		Resolver: connMgr,
		// 桌面客户端默认允许 Dangerous（前端已在弹窗层做二次确认）。
		Confirm: command.AllowConfirmer{},
	})

	return &App{
		log:     log,
		cfg:     cfg,
		connMgr: connMgr,
		plugMgr: plugMgr,
		engine:  engine,
		history: openHistory(log, cfg.App.DataDir),
		enabled: map[string]bool{},
	}, nil
}

// openHistory 打开 JSONL 存储。失败时降级为 nil：Runner 会自然停用历史。
func openHistory(log *logger.Logger, dir string) *history.JSONLStore {
	s, err := history.NewJSONLStore(dir)
	if err != nil {
		log.WithComponent("workflow.history").
			Warn("disable workflow history", "err", err.Error())
		return nil
	}
	return s
}

// SetContext 由 Wails OnStartup 调用；保存供 EventsEmit 使用。
func (a *App) SetContext(ctx context.Context) {
	a.ctxMu.Lock()
	a.ctx = ctx
	a.ctxMu.Unlock()
}

func (a *App) wailsCtx() context.Context {
	a.ctxMu.RLock()
	defer a.ctxMu.RUnlock()
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// Close 关闭所有资源；由 OnShutdown 调用。
func (a *App) Close() {
	// 关闭所有 shell 会话
	a.shells.Range(func(k, v any) bool {
		if s, ok := v.(*shellSession); ok {
			s.cancel()
		}
		return true
	})
	// 关闭所有 docker.logs 会话
	a.dockerLogs.Range(func(k, v any) bool {
		if s, ok := v.(*dockerLogsSession); ok {
			s.cancel()
		}
		return true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = a.plugMgr.Shutdown(ctx)

	a.loadedMu.Lock()
	defer a.loadedMu.Unlock()
	for _, c := range a.loaded {
		c()
	}
}

// -----------------------------------------------------------------------------
// 插件加载
// -----------------------------------------------------------------------------

// ensurePlugin 保证指定插件已注册并 Enable；重复调用安全。
func (a *App) ensurePlugin(ctx context.Context, id string) error {
	a.loadedMu.Lock()
	if a.enabled[id] {
		a.loadedMu.Unlock()
		return nil
	}
	a.loadedMu.Unlock()

	// 若已在 PluginManager 中注册（任意状态），只需确保启用
	if e, ok := a.plugMgr.Get(id); ok {
		if e.State != plugin.StateEnabled {
			if err := a.plugMgr.Enable(ctx, id, a.buildInitRequest(id)); err != nil {
				return fmt.Errorf("enable plugin %q: %w", id, err)
			}
		}
		a.loadedMu.Lock()
		a.enabled[id] = true
		a.loadedMu.Unlock()
		return nil
	}

	lp, _, legacy, err := plugin.LoadInstalled(a.cfg.App.PluginsDir, id, nil)
	if err != nil {
		return fmt.Errorf("load plugin %q: %w", id, err)
	}
	if legacy {
		a.log.WithComponent("plugin.loader").Warn("legacy flat plugin layout is deprecated", "id", id)
	}

	if err := a.plugMgr.Register(lp.Plugin); err != nil {
		lp.Close()
		// 并发调用可能已抢先注册，降级处理
		if e, ok2 := a.plugMgr.Get(id); ok2 {
			if e.State != plugin.StateEnabled {
				if err2 := a.plugMgr.Enable(ctx, id, a.buildInitRequest(id)); err2 != nil {
					return fmt.Errorf("enable plugin %q: %w", id, err2)
				}
			}
			a.loadedMu.Lock()
			a.enabled[id] = true
			a.loadedMu.Unlock()
			return nil
		}
		return fmt.Errorf("register plugin %q: %w", id, err)
	}

	if err := a.plugMgr.Enable(ctx, id, a.buildInitRequest(id)); err != nil {
		lp.Close()
		return fmt.Errorf("enable plugin %q: %w", id, err)
	}

	a.loadedMu.Lock()
	a.loaded = append(a.loaded, lp.Close)
	a.enabled[id] = true
	a.loadedMu.Unlock()
	return nil
}

// -----------------------------------------------------------------------------
// Target 管理（对应第一屏：Target 列表）
// -----------------------------------------------------------------------------

// TargetVM 是暴露给前端的 Target 视图（不含凭据）。
//
// 为了给多种连接类型提供统一 UI，除了 Host / Port / User（SSH 专用）之外，
// 我们额外暴露一个 DisplayHost —— 前端优先展示它：
//   - ssh：   "user@host:port"
//   - docker："<scheme>://<addr>"（unix / tcp / npipe）
type TargetVM struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	User        string            `json:"user"`
	DisplayHost string            `json:"display_host"`
	Tags        map[string]string `json:"tags"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// ListTargets 返回全部 Target。
func (a *App) ListTargets() ([]TargetVM, error) {
	ts := a.connMgr.List()
	out := make([]TargetVM, 0, len(ts))
	for _, t := range ts {
		display := ""
		switch t.Type {
		case connection.TypeSSH:
			if t.User != "" || t.Host != "" {
				port := t.Port
				if port == 0 {
					port = 22
				}
				display = fmt.Sprintf("%s@%s:%d", t.User, t.Host, port)
			}
		case connection.TypeDocker:
			// Docker target 把 host 也保存在 Target.Host 供 UI 展示（凭据里存的是完整字符串）。
			display = t.Host
		}
		out = append(out, TargetVM{
			ID:          t.ID,
			Type:        string(t.Type),
			Name:        t.Name,
			Host:        t.Host,
			Port:        t.Port,
			User:        t.User,
			DisplayHost: display,
			Tags:        t.Tags,
			CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:   t.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// UpsertSSHTargetInput 是 UpsertSSHTarget 的请求体。
// 用一个扁平结构体避免前端拼装复杂对象。
type UpsertSSHTargetInput struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Host string            `json:"host"`
	Port int               `json:"port"`
	User string            `json:"user"`
	Tags map[string]string `json:"tags"`

	// 认证方式：password / privatekey / agent
	Method     string `json:"method"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`

	// KnownHosts 策略；桌面默认 accept-new。
	KnownHostsMode string `json:"known_hosts_mode,omitempty"`
	KnownHostsPath string `json:"known_hosts_path,omitempty"`
}

// UpsertSSHTarget 新增/更新一个 SSH Target。
func (a *App) UpsertSSHTarget(in UpsertSSHTargetInput) error {
	if in.Port == 0 {
		in.Port = 22
	}
	if in.KnownHostsMode == "" {
		in.KnownHostsMode = "accept-new"
	}
	if in.KnownHostsPath == "" {
		in.KnownHostsPath = filepath.Join(a.cfg.App.DataDir, "plugin-data", "known_hosts")
	}
	return a.connMgr.Upsert(connection.Target{
		ID:   in.ID,
		Type: connection.TypeSSH,
		Name: in.Name,
		Host: in.Host,
		Port: in.Port,
		User: in.User,
		Tags: in.Tags,
	}, &connection.SSHCredentials{
		Method:         connection.SSHAuthMethod(in.Method),
		Password:       in.Password,
		PrivateKey:     in.PrivateKey,
		Passphrase:     in.Passphrase,
		KnownHostsMode: in.KnownHostsMode,
		KnownHostsPath: in.KnownHostsPath,
	})
}

// DeleteTarget 删除一个 Target。
func (a *App) DeleteTarget(id string) error {
	return a.connMgr.Delete(id)
}

// UpsertDockerTargetInput 是 UpsertDockerTarget 的请求体。
// TLS 三件套是完整 PEM 文本（前端通过文件选择器或粘贴框读取）。
type UpsertDockerTargetInput struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Tags map[string]string `json:"tags"`

	Host       string `json:"host"`
	APIVersion string `json:"api_version,omitempty"`
	TLSVerify  bool   `json:"tls_verify,omitempty"`
	TLSCA      string `json:"tls_ca,omitempty"`
	TLSCert    string `json:"tls_cert,omitempty"`
	TLSKey     string `json:"tls_key,omitempty"`
}

// UpsertDockerTarget 新增 / 更新一个 Docker Target。
// Target.Host 与凭据 Host 同步，供 UI 免解密展示。
func (a *App) UpsertDockerTarget(in UpsertDockerTargetInput) error {
	return a.connMgr.Upsert(connection.Target{
		ID:   in.ID,
		Type: connection.TypeDocker,
		Name: in.Name,
		Host: in.Host,
		Tags: in.Tags,
	}, &connection.DockerCredentials{
		Host:       in.Host,
		APIVersion: in.APIVersion,
		TLSVerify:  in.TLSVerify,
		TLSCA:      in.TLSCA,
		TLSCert:    in.TLSCert,
		TLSKey:     in.TLSKey,
	})
}

// PingTarget 通过 ssh.ping 端到端测试连接（不需要真实登录）。
// 主要用于 UI 上的健康检查按钮；后续可换成 ssh.exec("true")。
func (a *App) PingTarget(targetID string) (string, error) {
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 15*time.Second)
	defer cancel()
	if err := a.ensurePlugin(ctx, "ssh"); err != nil {
		return "", err
	}
	resp, err := a.engine.Run(ctx, command.Request{
		PluginID:  "ssh",
		CommandID: "exec",
		TargetID:  targetID,
		Params:    json.RawMessage(`{"cmd":"true"}`),
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
		Timeout:   10 * time.Second,
	})
	if err != nil {
		return "", err
	}
	return resp.AuditID, nil
}

// -----------------------------------------------------------------------------
// SFTP（对应第三屏：文件浏览器）
// -----------------------------------------------------------------------------

// SFTPEntry 与插件 sftpEntry 对齐。
type SFTPEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
	IsLink  bool   `json:"is_link"`
}

// SFTPListResult 与插件对齐。
type SFTPListResult struct {
	Path    string      `json:"path"`
	Entries []SFTPEntry `json:"entries"`
}

// SftpList 列出 targetID 上的 remotePath 目录。
func (a *App) SftpList(targetID, remotePath string) (*SFTPListResult, error) {
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 30*time.Second)
	defer cancel()
	if err := a.ensurePlugin(ctx, "ssh"); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{"path": remotePath})
	resp, err := a.engine.Run(ctx, command.Request{
		PluginID:  "ssh",
		CommandID: "sftp.list",
		TargetID:  targetID,
		Params:    params,
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
	if err != nil {
		return nil, err
	}
	var out SFTPListResult
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SftpUpload 把本机 localPath 上传到 remotePath。
func (a *App) SftpUpload(targetID, localPath, remotePath string) error {
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 5*time.Minute)
	defer cancel()
	if err := a.ensurePlugin(ctx, "ssh"); err != nil {
		return err
	}
	params, _ := json.Marshal(map[string]any{
		"local_path":  localPath,
		"remote_path": remotePath,
		"mkdir_all":   true,
	})
	_, err := a.engine.Run(ctx, command.Request{
		PluginID:  "ssh",
		CommandID: "sftp.upload",
		TargetID:  targetID,
		Params:    params,
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
	return err
}

// SftpDownload 把远端 remotePath 下载到本机 localPath。
func (a *App) SftpDownload(targetID, remotePath, localPath string) error {
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 5*time.Minute)
	defer cancel()
	if err := a.ensurePlugin(ctx, "ssh"); err != nil {
		return err
	}
	params, _ := json.Marshal(map[string]any{
		"remote_path": remotePath,
		"local_path":  localPath,
	})
	_, err := a.engine.Run(ctx, command.Request{
		PluginID:  "ssh",
		CommandID: "sftp.download",
		TargetID:  targetID,
		Params:    params,
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
	return err
}

// -----------------------------------------------------------------------------
// Terminal（对应第二屏：xterm.js ↔ ssh.shell）
// -----------------------------------------------------------------------------
//
// 前端交互（Wails Events）：
//   ShellOpen(targetID, {rows, cols, term}) → sessionID
//   ShellWrite(sessionID, base64Data)                    (前端调用)
//   ShellResize(sessionID, rows, cols)
//   ShellClose(sessionID)
//
//   event: "shell:<sessionID>:stdout"  (base64 data payload)
//   event: "shell:<sessionID>:exit"    ({exit_code, error?})

type shellSession struct {
	id     string
	cancel context.CancelFunc

	// 通过 sdk.Stream 桥接：这里持有 desktopShellStream 便于 write/resize/signal。
	stream *desktopShellStream
}

// ShellOpenInput 是 ShellOpen 的入参。
type ShellOpenInput struct {
	Term string `json:"term"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
}

// ShellOpen 打开一个交互式 PTY 会话，返回 sessionID。
// 后续通过 ShellWrite / ShellResize / ShellClose 交互；
// 输出通过 EventsEmit 到前端。
func (a *App) ShellOpen(targetID string, in ShellOpenInput) (string, error) {
	sess := fmt.Sprintf("sh-%d", a.shellN.Add(1))
	ctx, cancel := context.WithCancel(a.wailsCtx())

	if err := a.ensurePlugin(ctx, "ssh"); err != nil {
		cancel()
		return "", err
	}

	// 提前解析连接：core/command.RunStream 内部会做 resolve，但结果只回填到 Request.Connection，
	// 不会注入 stream。为了让插件端 s.Connection() 拿得到，我们这里手动先 Open 一次。
	conn, err := a.connMgr.Open(ctx, targetID)
	if err != nil {
		cancel()
		return "", fmt.Errorf("open target %q: %w", targetID, err)
	}

	params, _ := json.Marshal(map[string]any{
		"term": defaultString(in.Term, "xterm-256color"),
		"rows": defaultInt(in.Rows, 24),
		"cols": defaultInt(in.Cols, 80),
	})
	stream := newDesktopShellStream(ctx, a.wailsCtx(), sess)
	stream.setParams(params)
	stream.SetConnection(conn)

	req := command.Request{
		PluginID:   "ssh",
		CommandID:  "shell",
		TargetID:   targetID,
		Connection: conn, // 复用同一份，避免 Middleware 再次 Open
		Params:     params,
		Caller:     sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	}

	a.shells.Store(sess, &shellSession{id: sess, cancel: cancel, stream: stream})

	go func() {
		defer a.shells.Delete(sess)
		defer stream.close()
		err := a.engine.RunStream(ctx, req, stream)
		exitCode := stream.exitCode()
		payload := map[string]any{"exit_code": exitCode}
		if err != nil {
			payload["error"] = err.Error()
		}
		wailsruntime.EventsEmit(a.wailsCtx(), "shell:"+sess+":exit", payload)
	}()

	return sess, nil
}

// ShellWrite 把 base64 编码的字节写入远端 stdin。
func (a *App) ShellWrite(sessionID, dataB64 string) error {
	v, ok := a.shells.Load(sessionID)
	if !ok {
		return fmt.Errorf("shell session %q not found", sessionID)
	}
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	v.(*shellSession).stream.pushStdin(raw)
	return nil
}

// ShellResize 通知远端 PTY 窗口变化。
func (a *App) ShellResize(sessionID string, rows, cols int) error {
	v, ok := a.shells.Load(sessionID)
	if !ok {
		return fmt.Errorf("shell session %q not found", sessionID)
	}
	v.(*shellSession).stream.pushWinch(rows, cols)
	return nil
}

// ShellClose 主动关闭一个 shell 会话。
func (a *App) ShellClose(sessionID string) error {
	v, ok := a.shells.LoadAndDelete(sessionID)
	if !ok {
		return nil
	}
	v.(*shellSession).cancel()
	return nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func defaultString(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
func defaultInt(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	return "desktop"
}
