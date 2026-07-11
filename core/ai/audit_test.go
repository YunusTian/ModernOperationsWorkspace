package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/sdk"
)

// recordingAuditor 收集所有事件；并发安全供多路测试复用。
type recordingAuditor struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingAuditor) OnEvent(_ context.Context, ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingAuditor) Types() []EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EventType, len(r.events))
	for i, e := range r.events {
		out[i] = e.Type
	}
	return out
}

// -----------------------------------------------------------------------------
// 正常路径：LoopStart → RoundStart → RoundEnd → ToolCall → RoundStart → RoundEnd → LoopEnd
// -----------------------------------------------------------------------------

func TestAuditor_HappyPathSequence(t *testing.T) {
	f := &fakeRunner{
		chatResponses: []sdk.ChatResponse{
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{
				ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`),
			}}}, Finish: sdk.FinishToolCalls, Usage: sdk.ChatUsage{TotalTokens: 42}},
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "healthy"}, Finish: sdk.FinishStop, Usage: sdk.ChatUsage{TotalTokens: 7}},
		},
		toolResp: &command.Response{AuditID: "child-1", Data: json.RawMessage(`{"load":1}`)},
	}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, Auditor: rec, Timeout: 5e9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), Request{
		Provider: "openai", Model: "gpt-x", SessionID: "s1",
		Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "check"}},
	}); err != nil {
		t.Fatal(err)
	}

	want := []EventType{
		EventLoopStart,
		EventRoundStart, EventRoundEnd, EventToolCall,
		EventRoundStart, EventRoundEnd,
		EventLoopEnd,
	}
	got := rec.Types()
	if len(got) != len(want) {
		t.Fatalf("event count mismatch: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %s, want %s (all=%v)", i, got[i], want[i], got)
		}
	}

	// 具体字段抽检
	loopStart := rec.events[0]
	if loopStart.Provider != "openai" || loopStart.Model != "gpt-x" || len(loopStart.Tools) != 1 {
		t.Fatalf("loop_start fields: %+v", loopStart)
	}
	tc := rec.events[3]
	if tc.ToolName != "system.cpu" || tc.ToolCallID != "c1" || tc.Rejected {
		t.Fatalf("tool_call fields: %+v", tc)
	}
	if tc.ParentAuditID != "parent" || tc.AuditID != "child-1" {
		t.Fatalf("audit id linkage broken: %+v", tc)
	}
	end := rec.events[len(rec.events)-1]
	if end.FinishReason != sdk.FinishStop || end.UsageTokens != 7 || end.ToolCallsCount != 1 {
		t.Fatalf("loop_end fields: %+v", end)
	}
}

// -----------------------------------------------------------------------------
// 拒收分支：每个护栏都必须发出 tool_call(Rejected) + loop_end(FinishReason=code)
// -----------------------------------------------------------------------------

func TestAuditor_UnknownToolRejection(t *testing.T) {
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "docker.list", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
	}}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, Auditor: rec})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	assertCode(t, err, CodeUnknownTool)

	// 期望：LoopStart, RoundStart, RoundEnd, ToolCall(reject), LoopEnd
	if len(rec.events) != 5 {
		t.Fatalf("unexpected sequence %v", rec.Types())
	}
	rej := rec.events[3]
	if !rej.Rejected || rej.RejectCode != CodeUnknownTool || rej.ToolName != "docker.list" {
		t.Fatalf("reject event: %+v", rej)
	}
	end := rec.events[4]
	if end.Type != EventLoopEnd || end.FinishReason != CodeUnknownTool {
		t.Fatalf("loop_end: %+v", end)
	}
}

func TestAuditor_InvalidArgsRejection(t *testing.T) {
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`"oops"`)}}}, Finish: sdk.FinishToolCalls},
	}}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, Auditor: rec})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	last := rec.events[len(rec.events)-1]
	if last.Type != EventLoopEnd || last.FinishReason != CodeInvalidToolArgs {
		t.Fatalf("loop_end: %+v", last)
	}
	// 最后一条 tool_call 必须是 rejected + 对应 code
	var rej *Event
	for i := range rec.events {
		if rec.events[i].Type == EventToolCall {
			rej = &rec.events[i]
		}
	}
	if rej == nil || !rej.Rejected || rej.RejectCode != CodeInvalidToolArgs {
		t.Fatalf("reject event: %+v", rej)
	}
}

func TestAuditor_TotalCallsRejection(t *testing.T) {
	// 让第 3 次调用超过 MaxTotalCalls=2
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
	}}
	rec := &recordingAuditor{}
	o, err := New(Options{
		Runner: f, AllowedTools: []string{"system.cpu"},
		Auditor: rec, MaxRounds: 5, MaxCallsPerRound: 1, MaxTotalCalls: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	last := rec.events[len(rec.events)-1]
	if last.Type != EventLoopEnd || last.FinishReason != CodeTotalCalls {
		t.Fatalf("loop_end: %+v", last)
	}
}

func TestAuditor_MaxRoundsRejection(t *testing.T) {
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{
		{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
	}}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, Auditor: rec, MaxRounds: 2, MaxCallsPerRound: 4, MaxTotalCalls: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	last := rec.events[len(rec.events)-1]
	if last.Type != EventLoopEnd || last.FinishReason != CodeMaxRounds {
		t.Fatalf("loop_end: %+v", last)
	}
}

func TestAuditor_ProviderDecodeReachesLoopEnd(t *testing.T) {
	wrapped := &corruptRunner{fakeRunner: &fakeRunner{}}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: wrapped, Auditor: rec})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}})
	last := rec.events[len(rec.events)-1]
	if last.Type != EventLoopEnd || last.FinishReason != CodeProviderDecode {
		t.Fatalf("loop_end: %+v", last)
	}
}

// -----------------------------------------------------------------------------
// tool_call 事件：截断标记、结果字节数、错误码传播、args 脱敏
// -----------------------------------------------------------------------------

func TestAuditor_ToolCallTruncationAndError(t *testing.T) {
	f := &fakeRunner{
		chatResponses: []sdk.ChatResponse{
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "ok"}, Finish: sdk.FinishStop},
		},
		toolResp: &command.Response{Data: json.RawMessage(strings.Repeat("x", 100))},
	}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, Auditor: rec, MaxResultBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	var tc *Event
	for i := range rec.events {
		if rec.events[i].Type == EventToolCall {
			tc = &rec.events[i]
		}
	}
	if tc == nil || !tc.Truncated {
		t.Fatalf("tool_call truncation flag: %+v", tc)
	}
	if tc.ResultBytes != 10+len("...[truncated]") {
		t.Fatalf("result bytes: %d", tc.ResultBytes)
	}
}

func TestAuditor_ToolCallErrorCode(t *testing.T) {
	sdkErr := &sdk.Error{Code: "SSH_TIMEOUT"}
	f := &fakeRunner{
		chatResponses: []sdk.ChatResponse{
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{}`)}}}, Finish: sdk.FinishToolCalls},
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "done"}, Finish: sdk.FinishStop},
		},
		toolErr: sdkErr,
	}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"system.cpu"}, Auditor: rec})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	var tc *Event
	for i := range rec.events {
		if rec.events[i].Type == EventToolCall {
			tc = &rec.events[i]
		}
	}
	if tc == nil || tc.ErrorCode != "SSH_TIMEOUT" {
		t.Fatalf("tool_call error code: %+v", tc)
	}
}

func TestAuditor_ArgsDigestRedacted(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"password":{"type":"string","x-mow-sensitive":true}}}`)
	f := &fakeRunner{
		specFn: func(p, c string) (sdk.CommandSpec, error) {
			return sdk.CommandSpec{ID: c, Permission: sdk.PermRead, InputSchema: schema}, nil
		},
		chatResponses: []sdk.ChatResponse{
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{
				ID: "c1", Name: "ssh.probe", Args: json.RawMessage(`{"password":"hunter2"}`),
			}}}, Finish: sdk.FinishToolCalls},
			{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "done"}, Finish: sdk.FinishStop},
		},
	}
	redact := func(_ json.RawMessage, args json.RawMessage) json.RawMessage {
		var m map[string]any
		_ = json.Unmarshal(args, &m)
		m["password"] = "***"
		out, _ := json.Marshal(m)
		return out
	}
	rec := &recordingAuditor{}
	o, err := New(Options{Runner: f, AllowedTools: []string{"ssh.probe"}, Auditor: rec, Redactor: redact})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	var tc *Event
	for i := range rec.events {
		if rec.events[i].Type == EventToolCall {
			tc = &rec.events[i]
		}
	}
	if tc == nil {
		t.Fatal("no tool_call event")
	}
	if strings.Contains(tc.ArgsDigest, "hunter2") {
		t.Fatalf("secret leaked into digest: %s", tc.ArgsDigest)
	}
	if !strings.Contains(tc.ArgsDigest, "***") {
		t.Fatalf("digest missing mask: %s", tc.ArgsDigest)
	}
}

// -----------------------------------------------------------------------------
// SlogAuditor：日志级别 / 字段
// -----------------------------------------------------------------------------

func TestSlogAuditor_WarnsOnRejection(t *testing.T) {
	var buf bytes.Buffer
	log := logger.Init(logger.Options{Level: "debug", Format: logger.FormatJSON, Output: &buf})
	a := NewSlogAuditor(log)
	a.OnEvent(context.Background(), Event{
		Type: EventToolCall, ToolName: "docker.list",
		Rejected: true, RejectCode: CodeUnknownTool,
	})
	line := buf.String()
	if !strings.Contains(line, `"level":"WARN"`) {
		t.Fatalf("expected WARN, got %s", line)
	}
	if !strings.Contains(line, `"reject_code":"AI_UNKNOWN_TOOL"`) {
		t.Fatalf("expected reject_code field, got %s", line)
	}
	// LoopEnd 以正常 finish_reason 落 INFO
	buf.Reset()
	a.OnEvent(context.Background(), Event{Type: EventLoopEnd, FinishReason: sdk.FinishStop})
	if !strings.Contains(buf.String(), `"level":"INFO"`) {
		t.Fatalf("expected INFO for stop, got %s", buf.String())
	}
	// LoopEnd 携带错误码则升级为 WARN
	buf.Reset()
	a.OnEvent(context.Background(), Event{Type: EventLoopEnd, FinishReason: CodeMaxRounds})
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("expected WARN for AI_ code, got %s", buf.String())
	}
}

// -----------------------------------------------------------------------------
// Emit 稳定性：nil auditor 与 panic 都不影响主流程
// -----------------------------------------------------------------------------

type panicAuditor struct{}

func (panicAuditor) OnEvent(context.Context, Event) { panic("boom") }

func TestAuditor_PanicIsSwallowed(t *testing.T) {
	f := &fakeRunner{chatResponses: []sdk.ChatResponse{{
		Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: "done"}, Finish: sdk.FinishStop,
	}}}
	o, err := New(Options{Runner: f, Auditor: panicAuditor{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), Request{Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "x"}}}); err != nil {
		t.Fatalf("orchestrator should not fail on auditor panic: %v", err)
	}
}

// silenceUnusedImports 保证 slog import 在测试文件里始终被引用
// （测试期不直接调用 slog，但保留以便后续扩展）。
var _ = slog.LevelInfo

// argsDigestFallback 断言在 redactor=nil 时也会给出摘要。
func TestArgsDigestNoRedactor(t *testing.T) {
	got := argsDigest(nil, json.RawMessage(`{"a":1}`), nil)
	if got != `{"a":1}` {
		t.Fatalf("digest without redactor should be identity: %q", got)
	}
	// 超长截断
	big := json.RawMessage(strings.Repeat("z", argsDigestMaxLen+50))
	got = argsDigest(nil, big, nil)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
}

// errorCode 分支覆盖：unwrap sdk.Error、unwrap orchestrator Error、普通 error。
func TestErrorCodeMapping(t *testing.T) {
	if errorCode(nil) != "" {
		t.Fatalf("nil should map to empty")
	}
	if errorCode(&Error{Code: "AI_X"}) != "AI_X" {
		t.Fatalf("ai.Error not extracted")
	}
	if errorCode(&sdk.Error{Code: "SDK_X"}) != "SDK_X" {
		t.Fatalf("sdk.Error not extracted")
	}
	if errorCode(errors.New("raw")) != "ERROR" {
		t.Fatalf("plain error should map to ERROR")
	}
}
