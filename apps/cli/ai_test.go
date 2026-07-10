package main

import (
	"context"
	"encoding/json"
	"testing"

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
