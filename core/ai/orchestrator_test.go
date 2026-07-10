package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// fakeRunner 是可脚本化的 CommandRunner，方便逐分支覆盖 orchestrator。
//
//   - specFn：控制 Spec 返回什么（默认给 read + 空对象 schema）
//   - chatResponses：ai.chat 的连续返回，末位反复复用
//   - toolResp / toolErr：非 ai.* 命令的返回；未设则返回 {}
type fakeRunner struct {
	specFn        func(p, c string) (sdk.CommandSpec, error)
	chatResponses []sdk.ChatResponse
	chatIndex     int

	toolResp *command.Response
	toolErr  error

	// 观测字段
	toolCallers []sdk.Caller
	toolParams  [][]byte
}

func (f *fakeRunner) Spec(p, c string) (sdk.CommandSpec, error) {
	if f.specFn != nil {
		return f.specFn(p, c)
	}
	return sdk.CommandSpec{
		ID: c, Description: "test", Permission: sdk.PermRead,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, nil
}

func (f *fakeRunner) Run(ctx context.Context, r command.Request) (*command.Response, error) {
	if r.PluginID == "ai" {
		i := f.chatIndex
		if i >= len(f.chatResponses) {
			i = len(f.chatResponses) - 1
		}
		f.chatIndex++
		b, _ := json.Marshal(f.chatResponses[i])
		return &command.Response{AuditID: "parent", Data: b}, nil
	}
	f.toolCallers = append(f.toolCallers, r.Caller)
	f.toolParams = append(f.toolParams, append([]byte(nil), r.Params...))
	if f.toolErr != nil {
		return nil, f.toolErr
	}
	if f.toolResp != nil {
		return f.toolResp, nil
	}
	return &command.Response{Data: json.RawMessage(`{}`)}, nil
}

// -----------------------------------------------------------------------------
// 构造期（Tool 目录派生）
// -----------------------------------------------------------------------------

func TestNewRequiresRunner(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error when Runner is nil")
	}
}

func TestNewRejectsInvalidToolID(t *testing.T) {
	_, err := New(Options{Runner: &fakeRunner{}, AllowedTools: []string{"badid"}})
	assertCode(t, err, CodeInvalidToolID)
}

func TestNewRejectsRecursiveAI(t *testing.T) {
	_, err := New(Options{Runner: &fakeRunner{}, AllowedTools: []string{"ai.chat"}})
	assertCode(t, err, CodeRecursiveTool)
}

func TestNewRejectsSpecError(t *testing.T) {
	f := &fakeRunner{specFn: func(p, c string) (sdk.CommandSpec, error) {
		return sdk.CommandSpec{}, errors.New("not found")
	}}
	_, err := New(Options{Runner: f, AllowedTools: []string{"docker.list"}})
	assertCode(t, err, CodeToolResolveFailed)
}

func TestNewRejectsNonReadPermission(t *testing.T) {
	f := &fakeRunner{specFn: func(p, c string) (sdk.CommandSpec, error) {
		return sdk.CommandSpec{ID: c, Permission: sdk.PermWrite, InputSchema: json.RawMessage(`{}`)}, nil
	}}
	_, err := New(Options{Runner: f, AllowedTools: []string{"docker.rm"}})
	assertCode(t, err, CodeToolNotReadable)
}

func TestNewRejectsDangerous(t *testing.T) {
	f := &fakeRunner{specFn: func(p, c string) (sdk.CommandSpec, error) {
		return sdk.CommandSpec{ID: c, Permission: sdk.PermDangerous, InputSchema: json.RawMessage(`{}`)}, nil
	}}
	_, err := New(Options{Runner: f, AllowedTools: []string{"docker.rm"}})
	assertCode(t, err, CodeToolNotReadable)
}

func TestNewRejectsStreaming(t *testing.T) {
	f := &fakeRunner{specFn: func(p, c string) (sdk.CommandSpec, error) {
		return sdk.CommandSpec{ID: c, Permission: sdk.PermRead, Streaming: true, InputSchema: json.RawMessage(`{}`)}, nil
	}}
	_, err := New(Options{Runner: f, AllowedTools: []string{"docker.logs"}})
	assertCode(t, err, CodeToolStreaming)
}

func TestNewRejectsMissingSchema(t *testing.T) {
	f := &fakeRunner{specFn: func(p, c string) (sdk.CommandSpec, error) {
		return sdk.CommandSpec{ID: c, Permission: sdk.PermRead}, nil
	}}
	_, err := New(Options{Runner: f, AllowedTools: []string{"docker.list"}})
	assertCode(t, err, CodeToolNoSchema)
}

func TestNewDerivesToolsFromSpec(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)
	f := &fakeRunner{specFn: func(p, c string) (sdk.CommandSpec, error) {
		return sdk.CommandSpec{ID: c, Description: "list containers", Permission: sdk.PermRead, InputSchema: schema}, nil
	}}
	o, err := New(Options{Runner: f, AllowedTools: []string{"docker.list"}})
	if err != nil {
		t.Fatal(err)
	}
	tools := o.Tools()
	if len(tools) != 1 || tools[0].Name != "docker.list" || tools[0].Description != "list containers" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if string(tools[0].InputSchema) != string(schema) {
		t.Fatalf("schema mismatch: %s", tools[0].InputSchema)
	}
}

// -----------------------------------------------------------------------------
// Run 主路径
// -----------------------------------------------------------------------------

func TestOrchestratorReadOnlyLoop(t *testing.T) {
	f := &fakeRunner{
		chatResponses: []sdk.ChatResponse{
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "healthy"}, Finish: sdk.FinishStop},
		},
		toolResp: &command.Response{Data: json.RawMessage(`{"load":1}`)},
	}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := o.Run(context.Background(), Request{
		Messages:  []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "check"}},
		SessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Response.Message.Content != "healthy" {
		t.Fatalf("unexpected content: %q", got.Response.Message.Content)
	}
	if got.ToolCalls != 1 || got.Rounds != 2 {
		t.Fatalf("unexpected counters: %+v", got)
	}
	if len(f.toolCallers) != 1 || f.toolCallers[0].Type != sdk.CallerAI || f.toolCallers[0].ParentAuditID != "parent" {
		t.Fatalf("caller not propagated: %+v", f.toolCallers)
	}
}

// -----------------------------------------------------------------------------
// 护栏分支
// -----------------------------------------------------------------------------

func TestRunRejectsUnknownTool(t *testing.T) {
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "docker.list", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
	}}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	assertCode(t, err, CodeUnknownTool)
}

func TestRunRejectsMaxCallsPerRound(t *testing.T) {
	calls := []sdk.ToolCall{
		{ID: "a", Name: "system.cpu", Args: json.RawMessage(`{}`)},
		{ID: "b", Name: "system.cpu", Args: json.RawMessage(`{}`)},
		{ID: "c", Name: "system.cpu", Args: json.RawMessage(`{}`)},
	}
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: calls}, Finish: sdk.FinishToolCalls},
	}}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, MaxCallsPerRound: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	assertCode(t, err, CodeCallsPerRound)
}

func TestRunRejectsMaxTotalCalls(t *testing.T) {
	// 每轮 1 个调用；MaxTotalCalls=2 → 第 3 次触发。
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
	}}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, MaxRounds: 5, MaxCallsPerRound: 1, MaxTotalCalls: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	assertCode(t, err, CodeTotalCalls)
}

func TestRunRejectsMaxRounds(t *testing.T) {
	// 无限循环声明 tool_call，MaxRounds=2 → 循环结束时报错。
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
	}}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, MaxRounds: 2, MaxCallsPerRound: 4, MaxTotalCalls: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	assertCode(t, err, CodeMaxRounds)
}

func TestRunRejectsInvalidArgs(t *testing.T) {
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`"oops"`)}}}, Finish: sdk.FinishToolCalls},
	}}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	assertCode(t, err, CodeInvalidToolArgs)
}

func TestRunAcceptsEmptyArgs(t *testing.T) {
	// Args 为空 / null 应被视为空对象。
	for _, args := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("{}")} {
		f := &fakeRunner{chatResponses: []sdk.ChatResponse{
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: args}}}, Finish: sdk.FinishToolCalls},
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "done"}, Finish: sdk.FinishStop},
		}}
		o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}}); err != nil {
			t.Fatalf("args=%q: %v", string(args), err)
		}
	}
}

func TestRunProviderDecodeError(t *testing.T) {
	// runner.Run 返回的 Data 不是合法 ChatResponse。
	f := &fakeRunner{}
	// 走一次自定义：直接给 chatResponses 里塞一个坏 JSON。
	f = &fakeRunner{chatResponses: nil}
	// 走个包装 runner：
	wrapped := &corruptRunner{fakeRunner: f}
	o, err := New(Options{Runner: wrapped, AllowedTools: nil})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	assertCode(t, err, CodeProviderDecode)
}

// corruptRunner 让 ai.chat 返回坏 JSON。
type corruptRunner struct{ *fakeRunner }

func (c *corruptRunner) Run(ctx context.Context, r command.Request) (*command.Response, error) {
	if r.PluginID == "ai" {
		return &command.Response{AuditID: "parent", Data: json.RawMessage(`not-json`)}, nil
	}
	return c.fakeRunner.Run(ctx, r)
}

// -----------------------------------------------------------------------------
// 截断与错误传播
// -----------------------------------------------------------------------------

func TestToolResultTruncation(t *testing.T) {
	big := strings.Repeat("x", 100)
	f := &fakeRunner{
		chatResponses: []sdk.ChatResponse{
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "ok"}, Finish: sdk.FinishStop},
		},
		toolResp: &command.Response{Data: json.RawMessage(big)},
	}
	// MaxResultBytes = 10 → 应触发截断。
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, MaxResultBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	// 无法直接读取消息序列（Runner 只保留 params 与 caller）。
	// 通过 helper 断言：
	got := toolResultContent(&command.Response{Data: json.RawMessage(big)}, nil, 10)
	if !strings.HasSuffix(got, "...[truncated]") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if len(got) != 10+len("...[truncated]") {
		t.Fatalf("unexpected length %d: %q", len(got), got)
	}
}

func TestToolResultCarriesSDKErrorCode(t *testing.T) {
	sdkErr := &sdk.Error{Code: "DOCKER_NOT_FOUND", Message: "missing"}
	got := toolResultContent(nil, sdkErr, 1024)
	if !strings.Contains(got, `"code":"DOCKER_NOT_FOUND"`) {
		t.Fatalf("expected code field, got %q", got)
	}
	if !strings.Contains(got, `"error":"DOCKER_NOT_FOUND: missing"`) {
		t.Fatalf("expected error message, got %q", got)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var aerr *Error
	if !errors.As(err, &aerr) {
		t.Fatalf("expected *ai.Error, got %T: %v", err, err)
	}
	if aerr.Code != want {
		t.Fatalf("code=%s want=%s (msg=%q)", aerr.Code, want, aerr.Message)
	}
}
