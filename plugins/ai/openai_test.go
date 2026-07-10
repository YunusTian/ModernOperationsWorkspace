package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mow/mow/sdk"
)

func newTestOpenAI(t *testing.T, handler http.HandlerFunc) *openAIProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("MOW_TEST_OPENAI_KEY", "secret-value")
	options, _ := json.Marshal(openAIOptions{BaseURL: srv.URL + "/v1", APIKeyEnv: "MOW_TEST_OPENAI_KEY", DefaultModel: "test-model"})
	p, err := newOpenAIProvider(providerSettings{Name: "test", Kind: "openai", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenAIChat(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-value" {
			t.Errorf("authorization=%q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "test-model" {
			t.Errorf("model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	})
	resp, err := p.Chat(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok" || resp.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v", resp)
	}
}

func TestOpenAIHTTPError(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
	})
	_, err := p.Chat(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hello"}}})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "AI_RATE_LIMITED" || !se.Retryable {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenAIChatStreamToolCall(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"checking "}}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"docker.list","arguments":"{\"all\":"}}]}}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"true}"}}]},"finish_reason":"tool_calls"}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	})
	sink := &collectAISink{}
	err := p.ChatStream(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "inspect"}}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sink.delta != "checking " || len(sink.calls) != 1 || string(sink.calls[0].Args) != `{"all":true}` || sink.final.Finish != sdk.FinishToolCalls {
		t.Fatalf("sink=%+v", sink)
	}
}

type collectAISink struct {
	delta string
	calls []sdk.ToolCall
	final sdk.ChatResponse
}

func (s *collectAISink) OnDelta(v string) error            { s.delta += v; return nil }
func (s *collectAISink) OnToolCall(v sdk.ToolCall) error   { s.calls = append(s.calls, v); return nil }
func (s *collectAISink) OnFinish(v sdk.ChatResponse) error { s.final = v; return nil }
