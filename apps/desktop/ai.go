package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	coreai "github.com/mow/mow/core/ai"
	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

type AIProviderVM struct {
	Name         string                   `json:"name"`
	Capabilities sdk.ProviderCapabilities `json:"capabilities"`
}
type AIChatOpenInput struct {
	Provider       string            `json:"provider"`
	Model          string            `json:"model"`
	Messages       []sdk.ChatMessage `json:"messages"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

func (a *App) AIProviders() ([]AIProviderVM, error) {
	ctx := a.wailsCtx()
	if err := a.ensurePlugin(ctx, "ai"); err != nil {
		return nil, err
	}
	resp, err := a.engine.Run(ctx, command.Request{PluginID: "ai", CommandID: "list_providers", Caller: sdk.Caller{Type: sdk.CallerDesktop}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Providers []AIProviderVM `json:"providers"`
	}
	if err = json.Unmarshal(resp.Data, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

func (a *App) AIChatOpen(in AIChatOpenInput) (string, error) {
	if len(in.Messages) == 0 {
		return "", fmt.Errorf("messages must be non-empty")
	}
	root := a.wailsCtx()
	if err := a.ensurePlugin(root, "ai"); err != nil {
		return "", err
	}
	sid := fmt.Sprintf("ai-%d", a.aiN.Add(1))
	ctx := root
	cancel := func() {}
	if in.TimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(root, time.Duration(in.TimeoutSeconds)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(root)
	}
	params, err := json.Marshal(map[string]any{"provider": in.Provider, "model": in.Model, "messages": in.Messages})
	if err != nil {
		cancel()
		return "", err
	}
	s := &aiChatSession{id: sid, ctx: ctx, cancel: cancel, wailsCtx: root, params: params, recv: make(chan sdk.Incoming)}
	a.aiChats.Store(sid, s)
	return sid, nil
}

// AIChatStart 在前端完成事件订阅后启动，避免首个 delta 早于订阅到达。
func (a *App) AIChatStart(sid string) error {
	v, ok := a.aiChats.Load(sid)
	if !ok {
		return fmt.Errorf("AI chat session %q not found", sid)
	}
	s := v.(*aiChatSession)
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("AI chat session %q already started", sid)
	}
	s.started = true
	s.mu.Unlock()
	go func() {
		defer a.aiChats.Delete(sid)
		defer s.cancel()
		err := a.engine.RunStream(s.ctx, command.Request{PluginID: "ai", CommandID: "chat_stream", Params: s.params, Caller: sdk.Caller{Type: sdk.CallerDesktop, SessionID: sid}}, s)
		payload := map[string]any{"audit_id": s.auditID}
		if err != nil {
			payload["error"] = err.Error()
		}
		wailsruntime.EventsEmit(s.wailsCtx, "ai:"+sid+":done", payload)
	}()
	return nil
}

func (a *App) AIChatClose(sid string) {
	if v, ok := a.aiChats.Load(sid); ok {
		v.(*aiChatSession).cancel()
	}
}

type aiChatSession struct {
	id       string
	ctx      context.Context
	cancel   context.CancelFunc
	wailsCtx context.Context
	auditID  string
	params   json.RawMessage
	recv     chan sdk.Incoming
	mu       sync.Mutex
	started  bool
}

func (s *aiChatSession) SetAuditID(v string)      { s.auditID = v }
func (s *aiChatSession) Context() context.Context { return s.ctx }
func (s *aiChatSession) AuditID() string          { return s.auditID }
func (s *aiChatSession) Caller() sdk.Caller {
	return sdk.Caller{Type: sdk.CallerDesktop, SessionID: s.id}
}
func (s *aiChatSession) Confirmed() bool             { return false }
func (s *aiChatSession) Params(v any) error          { return json.Unmarshal(s.params, v) }
func (s *aiChatSession) RawParams() json.RawMessage  { return s.params }
func (s *aiChatSession) Connection() *sdk.Connection { return nil }
func (s *aiChatSession) Recv() <-chan sdk.Incoming   { return s.recv }
func (s *aiChatSession) Stdout(b []byte) error {
	if len(b) > 0 {
		wailsruntime.EventsEmit(s.wailsCtx, "ai:"+s.id+":delta", string(b))
	}
	return nil
}
func (s *aiChatSession) Stderr(b []byte) error {
	if len(b) > 0 {
		wailsruntime.EventsEmit(s.wailsCtx, "ai:"+s.id+":error", string(b))
	}
	return nil
}
func (s *aiChatSession) Event(v any) error {
	wailsruntime.EventsEmit(s.wailsCtx, "ai:"+s.id+":tool", v)
	return nil
}
func (s *aiChatSession) Finish(v any, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wailsruntime.EventsEmit(s.wailsCtx, "ai:"+s.id+":finish", v)
	return nil
}

// -----------------------------------------------------------------------------
// AIAsk —— 非流式一次性对话，走宿主 orchestrator
// -----------------------------------------------------------------------------

// AIStatus 描述 host-side AI 编排器的健康状态，供 UI 在加载时展示配置问题。
// 若 ConfigError 非空说明 Cfg.AI.AllowedTools 里有工具被 orchestrator 拒收
// （常见原因：Dangerous / Streaming / 非 Read / ai.* 递归 / 未知命令）。
// 这条信息让 UI 可以在启动时用一条明显的横幅告诉用户"AI tool-use 未启用"，
// 而不是等第一次 Ask 时才失败。
type AIStatus struct {
	ToolCount   int      `json:"tool_count"`
	Tools       []string `json:"tools"`
	ConfigError string   `json:"config_error,omitempty"`
}

// AIStatus 返回宿主 orchestrator 状态。UI 展示用；不会真正调用 provider。
func (a *App) AIStatus() AIStatus {
	orch, err := a.Orchestrator()
	if err != nil {
		return AIStatus{ConfigError: err.Error()}
	}
	specs := orch.Tools()
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	return AIStatus{ToolCount: len(names), Tools: names}
}

// AIAskInput 是 AIAsk 的入参。
type AIAskInput struct {
	Provider       string            `json:"provider"`
	Model          string            `json:"model"`
	Messages       []sdk.ChatMessage `json:"messages"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

// AIAskResult 是 AIAsk 的返回：暴露给前端做用量与轮次展示。
type AIAskResult struct {
	Response  sdk.ChatResponse `json:"response"`
	Rounds    int              `json:"rounds"`
	ToolCalls int              `json:"tool_calls"`
}

// AIAsk 走 core/ai.Orchestrator：
//   - Tool 目录、参数脱敏、决策审计全部与 CLI 一致
//   - 前端一次性拿到 ChatResponse + 轮次统计；无需订阅事件
//   - Ctx 支持 timeout，默认 120s
func (a *App) AIAsk(in AIAskInput) (*AIAskResult, error) {
	if len(in.Messages) == 0 {
		return nil, fmt.Errorf("messages must be non-empty")
	}
	root := a.wailsCtx()
	if err := a.ensurePlugin(root, "ai"); err != nil {
		return nil, err
	}
	orch, err := a.Orchestrator()
	if err != nil {
		return nil, fmt.Errorf("ai orchestrator: %w", err)
	}
	ctx := root
	cancel := func() {}
	if in.TimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(root, time.Duration(in.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	sid := fmt.Sprintf("ask-%d", a.aiN.Add(1))
	res, err := orch.Run(ctx, coreai.Request{
		Provider:  in.Provider,
		Model:     in.Model,
		Messages:  in.Messages,
		SessionID: sid,
	})
	if err != nil {
		return nil, err
	}
	return &AIAskResult{Response: res.Response, Rounds: res.Rounds, ToolCalls: res.ToolCalls}, nil
}

// Orchestrator 懒加载 host-side AI 编排器（复用 command.Engine）。
// 装配 SlogAuditor + command.RedactParams + Cfg.AI.AllowedTools 与各上限。
func (a *App) Orchestrator() (*coreai.Orchestrator, error) {
	a.aiOrchOnce.Do(func() {
		a.aiOrch, a.aiOrchErr = coreai.New(coreai.Options{
			Runner:           desktopEngineRunner{engine: a.engine},
			AllowedTools:     a.cfg.AI.AllowedTools,
			MaxRounds:        a.cfg.AI.MaxRounds,
			MaxCallsPerRound: a.cfg.AI.MaxCallsPerRound,
			MaxTotalCalls:    a.cfg.AI.MaxTotalCalls,
			MaxResultBytes:   a.cfg.AI.MaxResultBytes,
			Timeout:          time.Duration(a.cfg.AI.TimeoutSeconds) * time.Second,
			Redactor:         command.RedactParams,
			Auditor:          coreai.NewSlogAuditor(a.log),
		})
	})
	return a.aiOrch, a.aiOrchErr
}

// desktopEngineRunner 把 *command.Engine 适配到 coreai.CommandRunner。
type desktopEngineRunner struct{ engine *command.Engine }

func (e desktopEngineRunner) Run(ctx context.Context, req command.Request) (*command.Response, error) {
	return e.engine.Run(ctx, req)
}
func (e desktopEngineRunner) Spec(pluginID, commandID string) (sdk.CommandSpec, error) {
	return e.engine.Spec(pluginID, commandID)
}
