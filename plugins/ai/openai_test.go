package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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
	// 关闭重试，让 429 直接冒泡便于断言错误码。
	p.retry.MaxAttempts = 1
	_, err := p.Chat(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hello"}}})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "AI_RATE_LIMITED" || !se.Retryable {
		t.Fatalf("error=%v", err)
	}
}

// -----------------------------------------------------------------------------
// P0-4：有上限指数退避
// -----------------------------------------------------------------------------

func TestOpenAIRetriesRateLimited(t *testing.T) {
	var attempts int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":1}}`)
	})
	// 保留默认 3 次；替换 sleep 避免真的等待。
	var slept []time.Duration
	p.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	resp, err := p.Chat(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("unexpected content: %q", resp.Message.Content)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	// 期望：第 1 次失败后退 500ms，第 2 次失败后退 1s（=2*Base）
	if len(slept) != 2 {
		t.Fatalf("expected 2 sleeps, got %d: %v", len(slept), slept)
	}
	if slept[0] != 500*time.Millisecond || slept[1] != time.Second {
		t.Fatalf("unexpected backoff: %v", slept)
	}
}

func TestOpenAIDoesNotRetry401(t *testing.T) {
	var attempts int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	})
	p.sleep = func(_ context.Context, _ time.Duration) error { return nil }
	_, err := p.Chat(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}}})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "AI_AUTH_FAILED" {
		t.Fatalf("error=%v", err)
	}
	if attempts != 1 {
		t.Fatalf("401 should not retry, got %d attempts", attempts)
	}
}

func TestOpenAIRetryGivesUp(t *testing.T) {
	var attempts int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	p.retry.MaxAttempts = 2
	p.sleep = func(_ context.Context, _ time.Duration) error { return nil }
	_, err := p.Chat(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}}})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "AI_PROVIDER_UNAVAILABLE" {
		t.Fatalf("error=%v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestOpenAIBackoffCappedAtMax(t *testing.T) {
	p := &openAIProvider{retry: retryPolicy{MaxAttempts: 10, Base: 100 * time.Millisecond, Max: 300 * time.Millisecond}}
	// attempt=1 → 100ms；attempt=2 → 200ms；attempt=3 → 400ms→cap→300ms
	if got := p.backoffFor(1); got != 100*time.Millisecond {
		t.Fatalf("attempt1=%v", got)
	}
	if got := p.backoffFor(2); got != 200*time.Millisecond {
		t.Fatalf("attempt2=%v", got)
	}
	if got := p.backoffFor(3); got != 300*time.Millisecond {
		t.Fatalf("attempt3=%v", got)
	}
	if got := p.backoffFor(100); got != 300*time.Millisecond {
		t.Fatalf("attemptBig=%v", got)
	}
}

func TestOpenAIRetryHonorsCtx(t *testing.T) {
	var attempts int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	// sleep 立即返回 ctx.Canceled 模拟用户取消。
	p.sleep = func(_ context.Context, _ time.Duration) error { return context.Canceled }
	_, err := p.Chat(context.Background(), sdk.ChatRequest{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}}})
	if !errors.Is(err, sdk.ErrCanceled) {
		t.Fatalf("expected ErrCanceled, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt before cancel, got %d", attempts)
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
