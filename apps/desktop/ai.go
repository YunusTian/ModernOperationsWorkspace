package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

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
