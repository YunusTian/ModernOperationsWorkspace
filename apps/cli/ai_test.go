package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	coreai "github.com/mow/mow/core/ai"
	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

func TestAICmdStructure(t *testing.T) {
	c := newAICmd(&appHolder{})
	for _, name := range []string{"providers", "ask", "chat"} {
		if child, _, err := c.Find([]string{name}); err != nil || child == c {
			t.Fatalf("missing ai %s command: %v", name, err)
		}
	}
}

func TestCLIChatStreamCapturesFinal(t *testing.T) {
	s := newCLIChatStream(context.Background(), json.RawMessage(`{"model":"x"}`))
	s.SetAuditID("audit-1")
	if s.AuditID() != "audit-1" {
		t.Fatalf("audit=%q", s.AuditID())
	}
	var params map[string]any
	if err := s.Params(&params); err != nil || params["model"] != "x" {
		t.Fatalf("params=%v err=%v", params, err)
	}
	want := sdk.ChatResponse{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "done"}, Finish: sdk.FinishStop}
	if err := s.Finish(want, 0); err != nil {
		t.Fatal(err)
	}
	if s.final.Message.Content != "done" || s.final.Finish != sdk.FinishStop {
		t.Fatalf("final=%+v", s.final)
	}
}

// TestAppOrchestratorWiresRedactorAndAuditor 断言：
//   - App.Orchestrator() 使用 command.RedactParams + SlogAuditor
//   - 空 AllowedTools 时不报错（纯对话模式）
//   - 二次调用返回同一实例（sync.Once 惰性化）
func TestAppOrchestratorWiresRedactorAndAuditor(t *testing.T) {
	// 用最小 App 组合：仅需 Engine 支持 Spec 查询；不启动 plugin 子进程。
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})
	app := &App{
		Cfg:     config.Config{AI: config.AIConfig{}},
		Log:     log,
		PlugMgr: plugMgr,
		Engine:  engine,
	}
	o1, err := app.Orchestrator()
	if err != nil {
		t.Fatalf("build orchestrator: %v", err)
	}
	if o1 == nil {
		t.Fatal("orchestrator is nil")
	}
	if len(o1.Tools()) != 0 {
		t.Fatalf("empty allowlist should yield 0 tools, got %d", len(o1.Tools()))
	}
	o2, _ := app.Orchestrator()
	if o1 != o2 {
		t.Fatal("Orchestrator() should be memoized")
	}
}

// TestAppOrchestratorRejectsUnknownTool 断言：Cfg 里配置了不存在的工具时，
// 构造期直接失败并返回稳定错误码。
func TestAppOrchestratorRejectsUnknownTool(t *testing.T) {
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})
	app := &App{
		Cfg: config.Config{AI: config.AIConfig{
			AllowedTools: []string{"nonexistent.command"},
		}},
		Log:     log,
		PlugMgr: plugMgr,
		Engine:  engine,
	}
	if _, err := app.Orchestrator(); err == nil {
		t.Fatal("expected orchestrator construction to fail on unknown tool")
	}
}

// -----------------------------------------------------------------------------
// CLI 错误路径 —— 项目 2 收敛真实插件错误的表驱动测试
// -----------------------------------------------------------------------------

// TestAppOrchestratorRejectsDangerousTool 断言：Dangerous 命令即使在 allowlist
// 中也会被 orchestrator 构造期拒收（v0.4 强制 Read-only）。
func TestAppOrchestratorRejectsDangerousTool(t *testing.T) {
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})

	// 注册一个提供 Dangerous 命令的 in-process 插件。
	if err := plugMgr.Register(&fakePlugin{
		id:      "fake",
		cmdID:   "danger",
		perm:    sdk.PermDangerous,
		schema:  json.RawMessage(`{"type":"object"}`),
	}); err != nil {
		t.Fatalf("register fake plugin: %v", err)
	}
	if err := plugMgr.Enable(context.Background(), "fake", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable fake plugin: %v", err)
	}

	app := &App{
		Cfg:     config.Config{AI: config.AIConfig{AllowedTools: []string{"fake.danger"}}},
		Log:     log,
		PlugMgr: plugMgr,
		Engine:  engine,
	}
	_, err := app.Orchestrator()
	if err == nil || !strings.Contains(err.Error(), "AI_TOOL_NOT_READABLE") {
		t.Fatalf("expected AI_TOOL_NOT_READABLE, got %v", err)
	}
}

// TestAppOrchestratorRejectsRecursiveAITool 断言：AllowedTools 中若含 ai.* 命令
// （递归调用 AI）会被构造期拒收。
func TestAppOrchestratorRejectsRecursiveAITool(t *testing.T) {
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})
	app := &App{
		Cfg:     config.Config{AI: config.AIConfig{AllowedTools: []string{"ai.list_providers"}}},
		Log:     log,
		PlugMgr: plugMgr,
		Engine:  engine,
	}
	_, err := app.Orchestrator()
	if err == nil || !strings.Contains(err.Error(), "AI_RECURSIVE_TOOL") {
		t.Fatalf("expected AI_RECURSIVE_TOOL, got %v", err)
	}
}

// TestAppOrchestratorRejectsStreamingTool 断言：Streaming 命令也不能进入 tool 目录。
func TestAppOrchestratorRejectsStreamingTool(t *testing.T) {
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})

	if err := plugMgr.Register(&fakePlugin{
		id:        "stream",
		cmdID:     "tail",
		perm:      sdk.PermRead,
		streaming: true,
		schema:    json.RawMessage(`{"type":"object"}`),
	}); err != nil {
		t.Fatalf("register stream plugin: %v", err)
	}
	if err := plugMgr.Enable(context.Background(), "stream", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable stream plugin: %v", err)
	}

	app := &App{
		Cfg:     config.Config{AI: config.AIConfig{AllowedTools: []string{"stream.tail"}}},
		Log:     log,
		PlugMgr: plugMgr,
		Engine:  engine,
	}
	_, err := app.Orchestrator()
	if err == nil || !strings.Contains(err.Error(), "AI_TOOL_STREAMING") {
		t.Fatalf("expected AI_TOOL_STREAMING, got %v", err)
	}
}

// TestAppOrchestratorRunPropagatesProviderError 断言：ai plugin 未挂 provider
// 或调用未知 provider 时，orchestrator.Run 会把 sdk.Error 一路上抛，供 CLI
// 显示。
func TestAppOrchestratorRunPropagatesProviderError(t *testing.T) {
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})

	// in-process 挂一个仅提供 ai.chat 的假 plugin，返回 AI_PROVIDER_UNAVAILABLE。
	if err := plugMgr.Register(&fakeAIChatPlugin{
		errCode: "AI_PROVIDER_UNAVAILABLE",
		errMsg:  "provider offline",
	}); err != nil {
		t.Fatalf("register fake ai: %v", err)
	}
	if err := plugMgr.Enable(context.Background(), "ai", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable fake ai: %v", err)
	}
	app := &App{
		Cfg:     config.Config{AI: config.AIConfig{}},
		Log:     log,
		PlugMgr: plugMgr,
		Engine:  engine,
	}
	orch, err := app.Orchestrator()
	if err != nil {
		t.Fatalf("orch: %v", err)
	}
	_, err = orch.Run(context.Background(), coreai.Request{
		Provider: "unknown", Model: "x",
		Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected provider error")
	}
	var aerr *sdk.Error
	if !errors.As(err, &aerr) || aerr.Code != "AI_PROVIDER_UNAVAILABLE" {
		t.Fatalf("expected sdk.Error AI_PROVIDER_UNAVAILABLE, got %v", err)
	}
}

// TestAppOrchestratorRunHonorsCancel 断言：ctx 取消能一路传导到 orchestrator。
// Ctrl+C 对应 CLI 的 signal.NotifyContext，走的就是这条路径。
func TestAppOrchestratorRunHonorsCancel(t *testing.T) {
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})

	if err := plugMgr.Register(&fakeAIChatPlugin{
		block: true, // 让 Execute 阻塞在 ctx.Done()
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := plugMgr.Enable(context.Background(), "ai", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	app := &App{
		Cfg:     config.Config{AI: config.AIConfig{TimeoutSeconds: 60}},
		Log:     log,
		PlugMgr: plugMgr,
		Engine:  engine,
	}
	orch, err := app.Orchestrator()
	if err != nil {
		t.Fatalf("orch: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := orch.Run(ctx, coreai.Request{
			Provider: "mock", Model: "x",
			Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}},
		})
		done <- e
	}()
	// 等 provider 阻塞后取消
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancel error, got nil")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("expected cancel-ish error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("orchestrator did not observe ctx cancellation")
	}
}

// -----------------------------------------------------------------------------
// In-process 测试插件
// -----------------------------------------------------------------------------

// fakePlugin 提供单条 Command，供 tool 目录派生的错误分支测试使用。
type fakePlugin struct {
	id        string
	cmdID     string
	perm      sdk.Permission
	streaming bool
	schema    json.RawMessage
}

func (p *fakePlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: p.id, Name: p.id, Version: "0.0.1", CoreVersion: ">=0.1.0"}
}
func (p *fakePlugin) Init(context.Context, sdk.InitRequest) error  { return nil }
func (p *fakePlugin) Shutdown(context.Context) error               { return nil }
func (p *fakePlugin) HealthCheck(context.Context) sdk.HealthStatus { return sdk.StatusHealthy }
func (p *fakePlugin) Commands() []sdk.CommandHandler {
	return []sdk.CommandHandler{&fakeCmd{owner: p}}
}

type fakeCmd struct{ owner *fakePlugin }

func (c *fakeCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          c.owner.cmdID,
		Permission:  c.owner.perm,
		InputSchema: c.owner.schema,
		Streaming:   c.owner.streaming,
	}
}
func (c *fakeCmd) Execute(context.Context, *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return &sdk.ExecuteResponse{Data: json.RawMessage(`{}`)}, nil
}
func (c *fakeCmd) ExecuteStream(context.Context, sdk.Stream) error { return nil }

// fakeAIChatPlugin 假装是 ai 插件，只实现 ai.chat（Read），行为可编程：
//   - errCode/errMsg 非空 → 返回对应 sdk.Error
//   - block=true → 阻塞在 ctx.Done()，用于取消测试
type fakeAIChatPlugin struct {
	errCode string
	errMsg  string
	block   bool
}

func (p *fakeAIChatPlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "ai", Name: "ai", Version: "0.0.1", CoreVersion: ">=0.1.0"}
}
func (p *fakeAIChatPlugin) Init(context.Context, sdk.InitRequest) error  { return nil }
func (p *fakeAIChatPlugin) Shutdown(context.Context) error               { return nil }
func (p *fakeAIChatPlugin) HealthCheck(context.Context) sdk.HealthStatus { return sdk.StatusHealthy }
func (p *fakeAIChatPlugin) Commands() []sdk.CommandHandler {
	return []sdk.CommandHandler{&fakeAIChatCmd{owner: p}}
}

type fakeAIChatCmd struct{ owner *fakeAIChatPlugin }

func (c *fakeAIChatCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "chat",
		Permission:  sdk.PermRead,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
}
func (c *fakeAIChatCmd) Execute(ctx context.Context, _ *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if c.owner.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.owner.errCode != "" {
		return nil, &sdk.Error{Code: c.owner.errCode, Message: c.owner.errMsg}
	}
	return &sdk.ExecuteResponse{Data: json.RawMessage(`{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}`)}, nil
}
func (c *fakeAIChatCmd) ExecuteStream(context.Context, sdk.Stream) error { return nil }
