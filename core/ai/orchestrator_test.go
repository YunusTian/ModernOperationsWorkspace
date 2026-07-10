package ai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

type fakeRunner struct {
	chats      int
	toolCaller sdk.Caller
}

func (f *fakeRunner) Spec(p, c string) (sdk.CommandSpec, error) {
	perm := sdk.PermRead
	if c == "write" {
		perm = sdk.PermWrite
	}
	return sdk.CommandSpec{ID: c, Description: "test", Permission: perm, InputSchema: json.RawMessage(`{"type":"object"}`)}, nil
}
func (f *fakeRunner) Run(_ context.Context, r command.Request) (*command.Response, error) {
	if r.PluginID == "ai" {
		f.chats++
		var out sdk.ChatResponse
		if f.chats == 1 {
			out = sdk.ChatResponse{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls}
		} else {
			out = sdk.ChatResponse{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "healthy"}, Finish: sdk.FinishStop}
		}
		b, _ := json.Marshal(out)
		return &command.Response{AuditID: "parent", Data: b}, nil
	}
	f.toolCaller = r.Caller
	return &command.Response{Data: json.RawMessage(`{"load":1}`)}, nil
}

func TestOrchestratorReadOnlyLoop(t *testing.T) {
	f := &fakeRunner{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "check"}}, SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Response.Message.Content != "healthy" || got.ToolCalls != 1 || f.toolCaller.Type != sdk.CallerAI || f.toolCaller.ParentAuditID != "parent" {
		t.Fatalf("result=%+v caller=%+v", got, f.toolCaller)
	}
}
func TestOrchestratorRejectsNonRead(t *testing.T) {
	_, err := New(Options{Runner: &fakeRunner{}, AllowedTools: []string{"system.write"}})
	if err == nil {
		t.Fatal("expected rejection")
	}
}
func TestOrchestratorRejectsRecursiveAI(t *testing.T) {
	_, err := New(Options{Runner: &fakeRunner{}, AllowedTools: []string{"ai.chat"}})
	if err == nil {
		t.Fatal("expected rejection")
	}
}
