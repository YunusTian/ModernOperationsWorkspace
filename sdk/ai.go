// ai.go —— v0.4 AI Plugin 的 sdk 抽象层。
//
// 本文件只暴露"接口 + 值对象"，不含具体实现：plugins/ai 与它未来的
// providers/* 才是消费方。UI / CLI / Workflow 一律通过 Command Engine
// 消费 `ai.chat` / `ai.chat_stream` / `ai.list_providers`，而不是直接依赖 Provider。
//
// 为什么放 sdk：
//   - sdk 是插件系统的"边界层"，AI 供应商实现将来会跨 Plugin/MCP 两种形态
//     并存；把公共 Provider 契约放这里，双方都能对接
//   - 主 core 侧不需要感知 AI —— 它只需要 CommandHandler，符合 Plugin 一致性
//
// 详见 docs/ai-plugin.md。

package sdk

import (
	"context"
	"encoding/json"
)

// -----------------------------------------------------------------------------
// Provider —— AI 供应商抽象
// -----------------------------------------------------------------------------

// Provider 是所有 AI 供应商必须实现的最小接口。
//
// 生命周期：
//   - 由 plugins/ai 在 Init 阶段根据 Settings 里 providers[] 数组实例化
//   - 供应商之间彼此独立；plugins/ai 用 map[name]Provider 路由
//
// 稳定性承诺：
//   - v0.4：接口签名可能小改（feedback 期）
//   - v0.4.1 开始：sdk minor 版本兼容
//
// 实现建议：
//   - 全部方法必须尊重 ctx；ctx.Done() 时立即中断请求
//   - 返回错误建议包成 *sdk.Error，附上 Retryable / details，供 Command
//     Engine 的 retry 策略消费
type Provider interface {
	// Name 是 provider 的唯一标识（与 Settings.providers[].name 对应）。
	// 例："openai" / "anthropic" / "mock" / "mcp:qdrant"。
	Name() string

	// Capabilities 声明本 provider 支持哪些能力。plugins/ai 会据此
	// 拒绝不支持的调用（例：只挂 mock 时不允许 stream）。
	Capabilities() ProviderCapabilities

	// Chat 一次性对话。若 provider 不支持一次性、只有 stream，可返回
	// ErrNotSupported，让上层退化到 ChatStream + 内部聚合。
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// ChatStream 流式对话。stream 由 plugins/ai 提供，负责把 delta
	// 转成 sdk.Stream.Stdout / Event。返回 nil 表示成功结束。
	ChatStream(ctx context.Context, req ChatRequest, stream ChatStreamSink) error
}

// ProviderCapabilities 描述一个 Provider 能做什么。
// 与 sdk.Metadata 的思路一致：静态可读，便于 UI / Command 内部路由。
type ProviderCapabilities struct {
	// Chat 是否支持 non-streaming Chat 调用。
	Chat bool `json:"chat"`
	// ChatStream 是否支持流式 Chat 调用。
	ChatStream bool `json:"chat_stream"`
	// ToolCalls 是否支持 tool-use / function calling。
	// v0.4 骨架里 mock provider 会返回 false；真实 OpenAI/Anthropic 返回 true。
	ToolCalls bool `json:"tool_calls"`
	// Models 支持的模型 ID 列表（用于 UI 下拉）。可空表示 provider 自行处理默认值。
	Models []string `json:"models,omitempty"`
}

// -----------------------------------------------------------------------------
// Chat 消息与请求
// -----------------------------------------------------------------------------

// ChatRole 用字符串常量而非枚举，让 provider 侧无需二次翻译。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ChatMessage 是一条对话消息。
//
// 字段设计对齐 OpenAI 兼容 API 惯例，方便真实 provider 直接透传；
// 同时允许 provider 自行选用需要的字段（例如 Anthropic 忽略 ToolCallID）。
type ChatMessage struct {
	// Role 见 RoleXxx 常量。
	Role string `json:"role"`

	// Content 文本内容。tool 消息里放 command 的返回值序列化。
	Content string `json:"content,omitempty"`

	// Name 可选说话者名（例：多用户群聊场景）。
	Name string `json:"name,omitempty"`

	// ToolCalls 由 assistant 消息生成，声明 AI 想调用的一批工具。
	// plugins/ai 会把这些翻译成一次 Command Engine 调用；结果再以
	// role=tool 消息喂回给 provider 续写。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// ToolCallID 只在 role=tool 消息里出现，用来关联上游 assistant.tool_calls[i].ID。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall 描述 AI 请求调用的一个工具（对应 Command）。
type ToolCall struct {
	// ID 由 provider 生成，唯一标识本次调用，供 role=tool 消息回引。
	ID string `json:"id"`

	// Name 是全限定 Command ID，例如 "docker.list" / "ssh.exec"。
	// plugins/ai 会以此调 Command Engine；未知名称直接拒绝。
	Name string `json:"name"`

	// Args 是 Command 的 params（JSON）。plugins/ai 直接透传给
	// command.Request.Params，由 Command Engine 做 InputSchema 校验。
	Args json.RawMessage `json:"args"`
}

// ToolSpec 声明"这次 chat 允许 AI 调用哪些工具"。
// plugins/ai 会由此过滤出白名单 Command 的 Spec（含 InputSchema / Description），
// 组装成 provider 需要的 tool schema。
type ToolSpec struct {
	// Name 全限定 Command ID（例 "docker.list"）。
	Name string `json:"name"`
	// Description 供 provider 使用；默认取 CommandSpec.Description。
	Description string `json:"description,omitempty"`
	// InputSchema 与 CommandSpec.InputSchema 保持一致。
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ChatRequest 是 Provider.Chat / ChatStream 的入参。
type ChatRequest struct {
	// Model 由调用方选择；空字符串表示让 provider 用其默认模型。
	Model string `json:"model,omitempty"`

	// Messages 对话历史。第一条通常是 system。
	Messages []ChatMessage `json:"messages"`

	// Tools 本次允许 AI 调用的工具白名单（可空 → 无 tool-use）。
	Tools []ToolSpec `json:"tools,omitempty"`

	// Temp 采样温度；0 → provider 默认。范围 [0,2]，由 provider 各自截断。
	Temp float32 `json:"temperature,omitempty"`

	// MaxTokens 输出上限；0 → provider 默认。
	MaxTokens int `json:"max_tokens,omitempty"`

	// Extra 允许 UI 传自定义参数（例：MCP context / stop sequences）。
	// provider 自行识别未知字段。
	Extra map[string]any `json:"extra,omitempty"`
}

// ChatResponse 是一次性 Chat 的完整返回。
type ChatResponse struct {
	// Message 是 assistant 的完整回复；若走 tool-use 则 Message.ToolCalls 非空、Content 可能为空。
	Message ChatMessage `json:"message"`

	// Usage 是本次调用的 token 消耗（部分 provider 可能返回 0）。
	Usage ChatUsage `json:"usage"`

	// Finish 结束原因，字符串常量：
	//   "stop"        —— 正常停止
	//   "length"      —— 达到 MaxTokens
	//   "tool_calls"  —— 请求调用工具，等待 role=tool 消息回喂
	//   "content_filter" —— 内容审核拦截
	//   "error"       —— 出错，见 Message.Content 或独立 err
	Finish string `json:"finish_reason"`
}

// ChatUsage 记录 token 用量。
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatStreamSink 由 plugins/ai 提供，供 Provider.ChatStream 回调。
//
// provider 实现应保证：
//   - OnDelta 与 OnToolCall 可交替调用，顺序反映真实到达顺序
//   - OnFinish 有且仅有一次；调用后 stream 视为结束
//   - 任何回调返回 error → provider 应立刻中断并把该 error 冒泡出 ChatStream
type ChatStreamSink interface {
	// OnDelta 收到文本增量。delta 是自上次回调以来的**新增**片段。
	OnDelta(delta string) error

	// OnToolCall 收到 tool-call。多数 provider 会分片下发（Name 先给，
	// Args 逐块拼装），plugins/ai 内部聚合完再一次性调用 Command Engine。
	OnToolCall(tc ToolCall) error

	// OnFinish 通知本次对话结束，含最终聚合的 ChatResponse。
	// plugins/ai 会据 Finish 决定是否进入下一轮 tool-use loop。
	OnFinish(final ChatResponse) error
}

// -----------------------------------------------------------------------------
// 常量
// -----------------------------------------------------------------------------

// FinishXxx 用于 ChatResponse.Finish 的字符串常量，避免误拼。
const (
	FinishStop          = "stop"
	FinishLength        = "length"
	FinishToolCalls     = "tool_calls"
	FinishContentFilter = "content_filter"
	FinishError         = "error"
)
