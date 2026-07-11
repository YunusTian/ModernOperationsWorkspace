package ai

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mow/mow/core/logger"
)

// -----------------------------------------------------------------------------
// AI 决策链路审计
// -----------------------------------------------------------------------------
//
// Orchestrator 在每个可观测决策点触发一个 Event；调用方注入 Auditor 消费。
// 事件设计目标：
//   - 与 core/command 的 AuditRecord 对齐（都带 audit_id / session_id）
//   - 参数摘要而非原文：敏感信息永不落审计
//   - 覆盖所有拒收分支（unknown tool / 超轮数 / 超单轮 / 超总数 / 参数非 JSON /
//     provider decode 失败 / 未知 provider 错误），每条链路必有一个 LoopEnd
//   - 事件不改变主流程语义：Auditor 失败被吞掉（返回 nil），避免拖垮调用
//
// 事件字段命名与 docs/observability.md 保持一致。

// EventType 是事件种类。
type EventType string

const (
	// EventLoopStart 在 orchestrator 进入 Run 循环前发出，携带 provider / model /
	// tools 目录快照。
	EventLoopStart EventType = "ai.loop.start"

	// EventRoundStart 在向 Provider 发起一次 chat 请求前发出。
	EventRoundStart EventType = "ai.round.start"

	// EventRoundEnd 在一轮 chat 完成后发出，记录 finish_reason 与 tool_call 数。
	EventRoundEnd EventType = "ai.round.end"

	// EventToolCall 在一个 tool_call 执行完成后发出（无论成功 / 失败 / 拒收）。
	// Rejected=true 表示护栏拒收，未真正调 Command Engine。
	EventToolCall EventType = "ai.tool.call"

	// EventLoopEnd 是终结事件；FinishReason 描述结束原因（stop / error code 之一）。
	EventLoopEnd EventType = "ai.loop.end"
)

// Event 是一条决策事件。所有字段都可以 JSON 序列化，字段命名对齐 slog / OTLP 惯例。
type Event struct {
	Type      EventType `json:"type"`
	SessionID string    `json:"session_id,omitempty"`
	Round     int       `json:"round,omitempty"`
	Timestamp time.Time `json:"timestamp"`

	// Provider / Model 只在 LoopStart 与 RoundStart 落盘。
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// Tools 仅在 LoopStart 填充：allowlist 派生后的工具名。
	Tools []string `json:"tools,omitempty"`

	// AuditID 是本轮 provider 请求的 command.Response.AuditID；
	// ToolCall 事件里则是**子 Command** 的 AuditID，与父 audit 通过
	// ParentAuditID 关联。
	AuditID       string `json:"audit_id,omitempty"`
	ParentAuditID string `json:"parent_audit_id,omitempty"`

	// FinishReason：
	//   - RoundEnd：sdk.FinishXxx 常量（stop / tool_calls / length ...）
	//   - LoopEnd：sdk.FinishXxx 或 orchestrator 稳定错误码（如 AI_MAX_ROUNDS）
	FinishReason string `json:"finish_reason,omitempty"`

	// ToolCallsCount：本轮 assistant.tool_calls 数量（RoundEnd 携带）。
	ToolCallsCount int `json:"tool_calls_count,omitempty"`

	// —— 仅 ToolCall 事件 ——
	// ToolName 是全限定 command id；ToolCallID 是 provider 生成的调用标识。
	ToolName    string `json:"tool_name,omitempty"`
	ToolCallID  string `json:"tool_call_id,omitempty"`
	Rejected    bool   `json:"rejected,omitempty"`
	RejectCode  string `json:"reject_code,omitempty"`   // 护栏错误码（例：AI_UNKNOWN_TOOL）
	DurationMS  int64  `json:"duration_ms,omitempty"`    // 子 Command 耗时；拒收时为 0
	ErrorCode   string `json:"error_code,omitempty"`     // 若 tool 调用失败，sdk.Error.Code
	ResultBytes int    `json:"result_bytes,omitempty"`   // 序列化后长度，反映是否被截断
	Truncated   bool   `json:"truncated,omitempty"`

	// ArgsDigest 是 tool_call.Args **脱敏后**的短摘要，方便审计定位而不泄露参数。
	// 由 Redactor（若配置）先脱敏，再截断到 argsDigestMaxLen 字节。
	ArgsDigest string `json:"args_digest,omitempty"`

	// UsageTokens 是 provider 返回的 token 用量（RoundEnd 携带；不可用时为 0）。
	UsageTokens int `json:"usage_tokens,omitempty"`
}

// argsDigestMaxLen 控制 ArgsDigest 字段最大长度；超过截断。
const argsDigestMaxLen = 256

// Auditor 是审计事件消费者。实现应保证 OnEvent 快速返回（异步落盘请自行 buffer）。
// 一次 Orchestrator.Run 内的调用是**串行**的，可安全共享单个实例。
type Auditor interface {
	OnEvent(ctx context.Context, ev Event)
}

// AuditorFunc 让普通函数直接充当 Auditor。
type AuditorFunc func(ctx context.Context, ev Event)

// OnEvent 实现 Auditor。
func (f AuditorFunc) OnEvent(ctx context.Context, ev Event) { f(ctx, ev) }

// nopAuditor 是默认实现，不产生任何副作用。
type nopAuditor struct{}

func (nopAuditor) OnEvent(context.Context, Event) {}

// -----------------------------------------------------------------------------
// SlogAuditor —— 内置实现，把事件转成结构化日志
// -----------------------------------------------------------------------------

// SlogAuditor 把 Event 打到 core/logger。CLI / Desktop / API 可直接用，
// 无需引入第三方存储。产出的日志同时能被外部 slog handler（例如 OTLP、SQLite
// sink）消费。
type SlogAuditor struct {
	log *logger.Logger
}

// NewSlogAuditor 返回一个基于给定 logger 的 SlogAuditor；nil 使用 default。
func NewSlogAuditor(log *logger.Logger) *SlogAuditor {
	if log == nil {
		log = logger.Default()
	}
	return &SlogAuditor{log: log.WithComponent("ai.audit")}
}

// OnEvent 把 Event 写成结构化 log。级别策略：
//   - Rejected / LoopEnd 且 FinishReason 是错误码 → Warn
//   - 其它 → Info
func (a *SlogAuditor) OnEvent(_ context.Context, ev Event) {
	fields := eventToFields(ev)
	msg := string(ev.Type)
	switch {
	case ev.Rejected, isErrorFinish(ev):
		a.log.Warn(msg, fields...)
	default:
		a.log.Info(msg, fields...)
	}
}

func isErrorFinish(ev Event) bool {
	if ev.Type != EventLoopEnd {
		return false
	}
	// 所有 orchestrator 层错误码统一 "AI_" 前缀。
	return strings.HasPrefix(ev.FinishReason, "AI_")
}

// eventToFields 拉平 Event 里非零字段为 slog key-value 序列。
// 手写而非 reflect 以获得可预测的字段顺序与零分配路径。
func eventToFields(ev Event) []any {
	fields := []any{
		"session_id", ev.SessionID,
		"ts", ev.Timestamp.Format(time.RFC3339Nano),
	}
	if ev.Round > 0 {
		fields = append(fields, "round", ev.Round)
	}
	if ev.Provider != "" {
		fields = append(fields, "provider", ev.Provider)
	}
	if ev.Model != "" {
		fields = append(fields, "model", ev.Model)
	}
	if len(ev.Tools) > 0 {
		fields = append(fields, "tools", ev.Tools)
	}
	if ev.AuditID != "" {
		fields = append(fields, "audit_id", ev.AuditID)
	}
	if ev.ParentAuditID != "" {
		fields = append(fields, "parent_audit_id", ev.ParentAuditID)
	}
	if ev.FinishReason != "" {
		fields = append(fields, "finish_reason", ev.FinishReason)
	}
	if ev.ToolCallsCount > 0 {
		fields = append(fields, "tool_calls", ev.ToolCallsCount)
	}
	if ev.ToolName != "" {
		fields = append(fields, "tool_name", ev.ToolName)
	}
	if ev.ToolCallID != "" {
		fields = append(fields, "tool_call_id", ev.ToolCallID)
	}
	if ev.Rejected {
		fields = append(fields, "rejected", true)
	}
	if ev.RejectCode != "" {
		fields = append(fields, "reject_code", ev.RejectCode)
	}
	if ev.DurationMS > 0 {
		fields = append(fields, "duration_ms", ev.DurationMS)
	}
	if ev.ErrorCode != "" {
		fields = append(fields, "error_code", ev.ErrorCode)
	}
	if ev.ResultBytes > 0 {
		fields = append(fields, "result_bytes", ev.ResultBytes)
	}
	if ev.Truncated {
		fields = append(fields, "truncated", true)
	}
	if ev.ArgsDigest != "" {
		fields = append(fields, "args_digest", ev.ArgsDigest)
	}
	if ev.UsageTokens > 0 {
		fields = append(fields, "usage_tokens", ev.UsageTokens)
	}
	return fields
}

// -----------------------------------------------------------------------------
// helper
// -----------------------------------------------------------------------------

// argsDigest 返回可安全审计的 args 摘要：
//  1. 如果配置了 redactor 与 schema，则先脱敏
//  2. 结果按 argsDigestMaxLen 截断，超出末尾追加 "…"
//  3. 保证输出永远是合法字符串（即便 args 是二进制）
func argsDigest(schema, args json.RawMessage,
	redact func(schema, params json.RawMessage) json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	view := args
	if redact != nil && len(schema) > 0 {
		view = redact(schema, args)
	}
	s := string(view)
	if len(s) <= argsDigestMaxLen {
		return s
	}
	return s[:argsDigestMaxLen] + "…"
}
