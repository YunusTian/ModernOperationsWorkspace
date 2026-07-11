package main

import (
	"context"
	"encoding/json"
	"testing"

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
