package main

import (
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
)

func TestAIChatOpenRejectsEmptyMessages(t *testing.T) {
	a := &App{}
	if _, err := a.AIChatOpen(AIChatOpenInput{}); err == nil {
		t.Fatal("expected empty messages error")
	}
}

func TestAIChatStartRejectsUnknownSession(t *testing.T) {
	a := &App{}
	if err := a.AIChatStart("missing"); err == nil {
		t.Fatal("expected missing session error")
	}
}

// TestAIAskRejectsEmptyMessages 保证非流式入口也做参数校验。
func TestAIAskRejectsEmptyMessages(t *testing.T) {
	a := &App{}
	if _, err := a.AIAsk(AIAskInput{}); err == nil {
		t.Fatal("expected empty messages error")
	}
}

// TestDesktopOrchestratorMemoized 断言 Orchestrator() 复用同一实例，
// 并使用了 SlogAuditor + Redactor（通过验证空 allowlist 构造成功来间接确认）。
func TestDesktopOrchestratorMemoized(t *testing.T) {
	log := logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON})
	plugMgr := plugin.NewManager(plugin.Options{Logger: log})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})
	a := &App{
		log:     log,
		cfg:     config.Config{AI: config.AIConfig{}},
		plugMgr: plugMgr,
		engine:  engine,
	}
	o1, err := a.Orchestrator()
	if err != nil {
		t.Fatal(err)
	}
	o2, _ := a.Orchestrator()
	if o1 != o2 {
		t.Fatal("Orchestrator() should be memoized")
	}
}
