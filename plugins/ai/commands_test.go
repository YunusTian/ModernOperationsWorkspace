// commands_test.go —— plugins/ai 骨架的单测。
//
// 目的：v0.4 骨架只有三条 Command + mock provider，测试目标是保证：
//   1. Plugin.Init 能正确从 Settings 装配 provider（含默认 mock、重复 name 拒绝）
//   2. ai.list_providers 返回稳定顺序 + capabilities
//   3. ai.chat 走通 mock provider 主路径 + 参数校验错误路径
//   4. ai.chat_stream 通过 mock provider 产出 delta + Finish（用 fakeStream 收集）
//   5. Provider 未注册 / capability 缺失 时的错误码
//
// mock provider 无网络依赖，测试完全在内存中完成。

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// newPluginWith 快捷构造：允许传入 settings map，Init 后返回。
func newPluginWith(t *testing.T, settings map[string]any) *AIPlugin {
	t.Helper()
	p := newAIPlugin()
	var raw []byte
	if settings != nil {
		b, err := json.Marshal(settings)
		if err != nil {
			t.Fatalf("marshal settings: %v", err)
		}
		raw = b
	}
	if err := p.Init(context.Background(), sdk.InitRequest{
		Settings: raw,
		DataDir:  t.TempDir(),
	}); err != nil {
		t.Fatalf("plugin Init: %v", err)
	}
	return p
}

// fakeStream 是 sdk.Stream 的最小内存实现，供 chat_stream 测试用。
type fakeStream struct {
	ctx    context.Context
	params json.RawMessage
	mu     sync.Mutex
	stdout []byte
	events []any
	final  any
	exit   int
}

func newFakeStream(ctx context.Context, params any) *fakeStream {
	raw, _ := json.Marshal(params)
	return &fakeStream{ctx: ctx, params: raw}
}

func (s *fakeStream) Context() context.Context    { return s.ctx }
func (s *fakeStream) AuditID() string             { return "" }
func (s *fakeStream) Caller() sdk.Caller          { return sdk.Caller{Type: sdk.CallerCLI, User: "test"} }
func (s *fakeStream) Confirmed() bool             { return true }
func (s *fakeStream) RawParams() json.RawMessage  { return s.params }
func (s *fakeStream) Params(dst any) error        { return json.Unmarshal(s.params, dst) }
func (s *fakeStream) Connection() *sdk.Connection { return nil }
func (s *fakeStream) Recv() <-chan sdk.Incoming   { ch := make(chan sdk.Incoming); close(ch); return ch }
func (s *fakeStream) Stdout(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stdout = append(s.stdout, data...)
	return nil
}
func (s *fakeStream) Stderr(data []byte) error { return nil }
func (s *fakeStream) Event(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, v)
	return nil
}
func (s *fakeStream) Finish(v any, exit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.final = v
	s.exit = exit
	return nil
}

// -----------------------------------------------------------------------------
// Init
// -----------------------------------------------------------------------------

// 无 settings → 默认挂 mock，list_providers 至少有一条。
func TestInit_DefaultsToMockProvider(t *testing.T) {
	p := newPluginWith(t, nil)
	if _, ok := p.providers["mock"]; !ok {
		t.Fatalf("expected default mock provider, got %v", p.providers)
	}
}

// 未知 kind 应拒绝，防止用户误配。
func TestInit_UnknownKindRejected(t *testing.T) {
	p := newAIPlugin()
	raw, _ := json.Marshal(map[string]any{
		"providers": []map[string]any{{"name": "x", "kind": "openai"}},
	})
	err := p.Init(context.Background(), sdk.InitRequest{Settings: raw, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "unsupported provider kind") {
		t.Fatalf("expected unsupported kind error, got %v", err)
	}
}

// 重复 name 应拒绝。
func TestInit_DuplicateNameRejected(t *testing.T) {
	p := newAIPlugin()
	raw, _ := json.Marshal(map[string]any{
		"providers": []map[string]any{
			{"name": "m", "kind": "mock"},
			{"name": "m", "kind": "mock"},
		},
	})
	err := p.Init(context.Background(), sdk.InitRequest{Settings: raw, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "duplicate provider name") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

// name 缺省 → 取 kind
func TestInit_NameDefaultsToKind(t *testing.T) {
	p := newPluginWith(t, map[string]any{
		"providers": []map[string]any{{"kind": "mock"}},
	})
	if _, ok := p.providers["mock"]; !ok {
		t.Fatalf("expected provider named 'mock', got %v", p.providers)
	}
}

// -----------------------------------------------------------------------------
// ai.list_providers
// -----------------------------------------------------------------------------

func TestListProviders_StableOrder(t *testing.T) {
	p := newPluginWith(t, map[string]any{
		"providers": []map[string]any{
			{"name": "zeta", "kind": "mock"},
			{"name": "alpha", "kind": "mock"},
		},
	})
	cmd := &listProvidersCmd{plugin: p}
	resp, err := cmd.Execute(context.Background(), &sdk.ExecuteRequest{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out listProvidersResult
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Providers) != 2 {
		t.Fatalf("len = %d", len(out.Providers))
	}
	if out.Providers[0].Name != "alpha" || out.Providers[1].Name != "zeta" {
		t.Fatalf("order = %+v", out.Providers)
	}
	// capability shape
	if !out.Providers[0].Capabilities.Chat || !out.Providers[0].Capabilities.ChatStream {
		t.Fatalf("mock provider should support Chat + ChatStream, got %+v", out.Providers[0].Capabilities)
	}
}

// list_providers 是非流式 → ExecuteStream 应返回 ErrNotSupported
func TestListProviders_ExecuteStreamNotSupported(t *testing.T) {
	cmd := &listProvidersCmd{plugin: newPluginWith(t, nil)}
	if err := cmd.ExecuteStream(context.Background(), nil); !errors.Is(err, sdk.ErrNotSupported) {
		t.Fatalf("expected ErrNotSupported, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ai.chat
// -----------------------------------------------------------------------------

func TestChat_HappyPathEchoesLastUserMessage(t *testing.T) {
	p := newPluginWith(t, nil)
	cmd := &chatCmd{plugin: p}
	params, _ := json.Marshal(chatParams{
		Messages: []sdk.ChatMessage{
			{Role: sdk.RoleSystem, Content: "you are a mock"},
			{Role: sdk.RoleUser, Content: "hello mow"},
		},
	})
	resp, err := cmd.Execute(context.Background(), &sdk.ExecuteRequest{Params: params})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out sdk.ChatResponse
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out.Message.Content, "hello mow") {
		t.Fatalf("expected echo, got %q", out.Message.Content)
	}
	if out.Finish != sdk.FinishStop {
		t.Fatalf("finish = %q", out.Finish)
	}
	if out.Usage.TotalTokens == 0 {
		t.Fatalf("token counts should be non-zero (approx)")
	}
}

func TestChat_EmptyMessagesRejected(t *testing.T) {
	cmd := &chatCmd{plugin: newPluginWith(t, nil)}
	params, _ := json.Marshal(chatParams{Messages: nil})
	_, err := cmd.Execute(context.Background(), &sdk.ExecuteRequest{Params: params})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Fatalf("expected PARAM_INVALID, got %v", err)
	}
}

func TestChat_UnknownProviderRejected(t *testing.T) {
	cmd := &chatCmd{plugin: newPluginWith(t, nil)}
	params, _ := json.Marshal(chatParams{
		Provider: "openai", // 未注册
		Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}},
	})
	_, err := cmd.Execute(context.Background(), &sdk.ExecuteRequest{Params: params})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "AI_PROVIDER_NOT_FOUND" {
		t.Fatalf("expected AI_PROVIDER_NOT_FOUND, got %v", err)
	}
}

// 非流式命令 → ExecuteStream 应 not supported
func TestChat_ExecuteStreamNotSupported(t *testing.T) {
	cmd := &chatCmd{plugin: newPluginWith(t, nil)}
	if err := cmd.ExecuteStream(context.Background(), nil); !errors.Is(err, sdk.ErrNotSupported) {
		t.Fatalf("expected ErrNotSupported, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ai.chat_stream
// -----------------------------------------------------------------------------

func TestChatStream_ProducesDeltasAndFinishes(t *testing.T) {
	p := newPluginWith(t, nil)
	cmd := &chatStreamCmd{plugin: p}
	params := chatParams{
		Messages: []sdk.ChatMessage{
			{Role: sdk.RoleUser, Content: "abcdefghij"}, // mock 会 echo "[mock] abcdefghij"
		},
	}
	stream := newFakeStream(context.Background(), params)
	if err := cmd.ExecuteStream(context.Background(), stream); err != nil {
		t.Fatalf("stream: %v", err)
	}
	// stdout 应完整包含 echo 内容（各 delta 拼起来）
	full := string(stream.stdout)
	if !strings.Contains(full, "abcdefghij") {
		t.Fatalf("stdout missing echo, got %q", full)
	}
	// final 应是 sdk.ChatResponse
	final, ok := stream.final.(sdk.ChatResponse)
	if !ok {
		t.Fatalf("final is not ChatResponse: %T", stream.final)
	}
	if final.Finish != sdk.FinishStop {
		t.Fatalf("finish = %q", final.Finish)
	}
}

func TestChatStream_ExecuteNotSupported(t *testing.T) {
	cmd := &chatStreamCmd{plugin: newPluginWith(t, nil)}
	_, err := cmd.Execute(context.Background(), &sdk.ExecuteRequest{})
	if !errors.Is(err, sdk.ErrNotSupported) {
		t.Fatalf("expected ErrNotSupported, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// mock provider 内部辅助
// -----------------------------------------------------------------------------

func TestMockReply_NoUserMessage(t *testing.T) {
	got := mockReply(nil)
	if !strings.Contains(got, "(no user input)") {
		t.Fatalf("empty history should produce fallback reply, got %q", got)
	}
}

// Plugin.Metadata 稳定断言
func TestPluginMetadata(t *testing.T) {
	p := newAIPlugin()
	md := p.Metadata()
	if md.ID != "ai" || md.Name != "AI" {
		t.Fatalf("unexpected metadata: %+v", md)
	}
	if len(md.ConnectionTypes) != 0 {
		t.Fatalf("AI 命令不消费连接类型: %v", md.ConnectionTypes)
	}
	// v0.4 骨架有 3 个 Command
	if got := len(p.Commands()); got != 3 {
		t.Fatalf("expected 3 commands, got %d", got)
	}
}

// approxTokens 边界
func TestApproxTokens(t *testing.T) {
	if approxTokensOf("") != 0 {
		t.Fatal("empty string should yield 0 tokens")
	}
	if approxTokensOf("hi") == 0 {
		t.Fatal("short string should yield >=1 token")
	}
}

// Init pool.Shutdown / HealthCheck 桩
func TestShutdownAndHealth(t *testing.T) {
	p := newPluginWith(t, nil)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if p.HealthCheck(context.Background()) != sdk.StatusHealthy {
		t.Fatal("expected healthy")
	}
}
