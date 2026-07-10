// Package ai implements host-side AI orchestration. Provider protocol handling
// remains in plugins/ai; all tool execution returns through command.Engine.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// CommandRunner 是 core/ai 依赖的窄接口：既能执行 Command，也能读取 Spec。
// 由 core/command.Engine 实现（Run / Spec）。
type CommandRunner interface {
	Run(context.Context, command.Request) (*command.Response, error)
	Spec(pluginID, commandID string) (sdk.CommandSpec, error)
}

// -----------------------------------------------------------------------------
// 稳定错误码
// -----------------------------------------------------------------------------

// Error 是 orchestrator 层的标准错误。Code 保持向后兼容，供 CLI / Desktop
// 做条件判断与用户提示。所有护栏触发时都返回 *Error。
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Is 允许 errors.Is 按 Code 匹配。
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code
}

// 稳定错误码常量。命名前缀 `AI_` 与 plugins/ai 侧的 `ai.*` 命名区分。
const (
	CodeInvalidToolID     = "AI_INVALID_TOOL_ID"      // Allowlist 中的 fqid 格式非法
	CodeRecursiveTool     = "AI_RECURSIVE_TOOL"       // 试图把 ai.* 加入 allowlist
	CodeToolResolveFailed = "AI_TOOL_RESOLVE_FAILED"  // 无法从 Runner 拿到 Spec
	CodeToolNotReadable   = "AI_TOOL_NOT_READABLE"    // Command 不是 PermRead
	CodeToolStreaming     = "AI_TOOL_STREAMING"       // 流式 Command 不能作为工具
	CodeToolNoSchema      = "AI_TOOL_NO_SCHEMA"       // 缺少 InputSchema
	CodeUnknownTool       = "AI_UNKNOWN_TOOL"         // 模型请求 allowlist 之外的工具
	CodeInvalidToolArgs   = "AI_INVALID_TOOL_ARGS"    // ToolCall.Args 不是合法 JSON 对象
	CodeCallsPerRound     = "AI_MAX_CALLS_PER_ROUND"  // 单轮调用数超限
	CodeTotalCalls        = "AI_MAX_TOTAL_CALLS"      // 总调用数超限
	CodeMaxRounds         = "AI_MAX_ROUNDS"           // 编排轮数超限
	CodeProviderDecode    = "AI_PROVIDER_DECODE"      // Provider 返回体无法解析
)

func newErr(code, msg string, cause error) *Error {
	return &Error{Code: code, Message: msg, Cause: cause}
}

// -----------------------------------------------------------------------------
// Options / Orchestrator
// -----------------------------------------------------------------------------

// Options 构造 Orchestrator 的参数。
//
// AllowedTools 使用全限定 ID（例："system.cpu"、"docker.list"）；
// Orchestrator 会在构造时向 Runner 查询每一条 Spec，据此派生 ToolSpec，
// 拒收：非只读 / 流式 / ai.* 递归 / 缺 InputSchema。这一策略保证模型无法
// 声明宿主不认识的工具，也无法绕过 CommandSpec 的 Schema 校验。
type Options struct {
	Runner           CommandRunner
	AllowedTools     []string
	MaxRounds        int           // 默认 8
	MaxCallsPerRound int           // 默认 4
	MaxTotalCalls    int           // 默认 16；0 表示按 MaxRounds*MaxCallsPerRound 上限计算
	MaxResultBytes   int           // 默认 64 KiB
	Timeout          time.Duration // 默认 120s
}

// Orchestrator 是宿主侧 AI tool-use 循环。并发安全（无内部可变状态）。
type Orchestrator struct {
	runner    CommandRunner
	tools     []sdk.ToolSpec  // 已通过校验的工具目录
	allowed   map[string]bool // fqid → 是否允许
	maxRounds int
	maxCalls  int
	maxTotal  int
	maxResult int
	timeout   time.Duration
}

// New 构造 Orchestrator。任一 AllowedTools 元素不符合护栏即返回 *Error。
func New(opts Options) (*Orchestrator, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("ai: Runner is required")
	}
	o := &Orchestrator{
		runner:    opts.Runner,
		allowed:   make(map[string]bool),
		maxRounds: opts.MaxRounds,
		maxCalls:  opts.MaxCallsPerRound,
		maxTotal:  opts.MaxTotalCalls,
		maxResult: opts.MaxResultBytes,
		timeout:   opts.Timeout,
	}
	if o.maxRounds <= 0 {
		o.maxRounds = 8
	}
	if o.maxCalls <= 0 {
		o.maxCalls = 4
	}
	if o.maxTotal <= 0 {
		o.maxTotal = o.maxRounds * o.maxCalls
	}
	if o.maxResult <= 0 {
		o.maxResult = 64 << 10
	}
	if o.timeout <= 0 {
		o.timeout = 120 * time.Second
	}
	for _, fqid := range opts.AllowedTools {
		spec, err := o.buildToolSpec(fqid)
		if err != nil {
			return nil, err
		}
		o.tools = append(o.tools, spec)
		o.allowed[fqid] = true
	}
	return o, nil
}

// Tools 返回派生后的工具目录（只读视图）。CLI / Desktop 可据此做展示或调试。
func (o *Orchestrator) Tools() []sdk.ToolSpec {
	out := make([]sdk.ToolSpec, len(o.tools))
	copy(out, o.tools)
	return out
}

// buildToolSpec 从 CommandSpec 派生 ToolSpec 并执行护栏校验。
//
// 拒收原因（返回稳定错误码）：
//   - fqid 格式非法 → AI_INVALID_TOOL_ID
//   - pluginID == "ai" → AI_RECURSIVE_TOOL
//   - Runner.Spec 失败 → AI_TOOL_RESOLVE_FAILED
//   - Permission != PermRead → AI_TOOL_NOT_READABLE（Dangerous 一律不进 v0.4 目录）
//   - Spec.Streaming → AI_TOOL_STREAMING（tool-use 循环无法承载流式）
//   - InputSchema 为空 → AI_TOOL_NO_SCHEMA（否则 Command Engine 无法校验模型参数）
func (o *Orchestrator) buildToolSpec(fqid string) (sdk.ToolSpec, error) {
	pluginID, commandID, ok := splitFQID(fqid)
	if !ok {
		return sdk.ToolSpec{}, newErr(CodeInvalidToolID, fmt.Sprintf("invalid tool id %q", fqid), nil)
	}
	if pluginID == "ai" {
		return sdk.ToolSpec{}, newErr(CodeRecursiveTool, fmt.Sprintf("recursive tool %q is forbidden", fqid), nil)
	}
	spec, err := o.runner.Spec(pluginID, commandID)
	if err != nil {
		return sdk.ToolSpec{}, newErr(CodeToolResolveFailed, fmt.Sprintf("resolve tool %q: %v", fqid, err), err)
	}
	if spec.Permission != sdk.PermRead {
		return sdk.ToolSpec{}, newErr(CodeToolNotReadable,
			fmt.Sprintf("tool %q must have read permission (got %s)", fqid, spec.Permission), nil)
	}
	if spec.Streaming {
		return sdk.ToolSpec{}, newErr(CodeToolStreaming, fmt.Sprintf("tool %q is streaming; not allowed in v0.4", fqid), nil)
	}
	if len(spec.InputSchema) == 0 {
		return sdk.ToolSpec{}, newErr(CodeToolNoSchema, fmt.Sprintf("tool %q has no InputSchema", fqid), nil)
	}
	return sdk.ToolSpec{
		Name:        fqid,
		Description: spec.Description,
		InputSchema: append(json.RawMessage(nil), spec.InputSchema...),
	}, nil
}

// -----------------------------------------------------------------------------
// Run
// -----------------------------------------------------------------------------

// Request 是 Orchestrator.Run 的入参。
type Request struct {
	Provider  string
	Model     string
	Messages  []sdk.ChatMessage
	SessionID string
}

// Result 是 Orchestrator.Run 的返回值。
type Result struct {
	Response  sdk.ChatResponse
	Rounds    int
	ToolCalls int
}

// Run 执行 tool-use 循环。触及任一护栏 → 返回 *Error（带稳定 Code）。
func (o *Orchestrator) Run(ctx context.Context, in Request) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	messages := append([]sdk.ChatMessage(nil), in.Messages...)
	totalCalls := 0

	for round := 1; round <= o.maxRounds; round++ {
		params, _ := json.Marshal(map[string]any{
			"provider": in.Provider,
			"model":    in.Model,
			"messages": messages,
			"tools":    o.tools,
		})
		resp, err := o.runner.Run(ctx, command.Request{
			PluginID:  "ai",
			CommandID: "chat",
			Params:    params,
			Caller:    sdk.Caller{Type: sdk.CallerAI, SessionID: in.SessionID},
		})
		if err != nil {
			return nil, err
		}
		var chat sdk.ChatResponse
		if err = json.Unmarshal(resp.Data, &chat); err != nil {
			return nil, newErr(CodeProviderDecode, "decode provider response", err)
		}

		// 正常结束：Finish != tool_calls 或工具列表为空。
		if chat.Finish != sdk.FinishToolCalls || len(chat.Message.ToolCalls) == 0 {
			return &Result{Response: chat, Rounds: round, ToolCalls: totalCalls}, nil
		}

		// 护栏：单轮调用数上限。
		if len(chat.Message.ToolCalls) > o.maxCalls {
			return nil, newErr(CodeCallsPerRound,
				fmt.Sprintf("tool calls in one round (%d) exceeds limit (%d)", len(chat.Message.ToolCalls), o.maxCalls), nil)
		}

		messages = append(messages, chat.Message)

		for _, tc := range chat.Message.ToolCalls {
			// 护栏：未知工具（不在 allowlist）。
			if !o.allowed[tc.Name] {
				return nil, newErr(CodeUnknownTool, fmt.Sprintf("tool %q is not allowed", tc.Name), nil)
			}
			// 护栏：参数必须是合法 JSON 对象；由 Command Engine 再做 schema 校验。
			if err := validateArgs(tc.Args); err != nil {
				return nil, newErr(CodeInvalidToolArgs,
					fmt.Sprintf("tool %q args invalid: %v", tc.Name, err), err)
			}
			// 护栏：总调用数上限（避免模型无限调用低成本工具耗尽预算）。
			if totalCalls >= o.maxTotal {
				return nil, newErr(CodeTotalCalls,
					fmt.Sprintf("total tool calls exceed limit (%d)", o.maxTotal), nil)
			}

			pluginID, commandID, _ := splitFQID(tc.Name)
			toolResp, runErr := o.runner.Run(ctx, command.Request{
				PluginID:  pluginID,
				CommandID: commandID,
				Params:    tc.Args,
				Caller: sdk.Caller{
					Type:          sdk.CallerAI,
					SessionID:     in.SessionID,
					ParentAuditID: resp.AuditID,
				},
			})
			content := toolResultContent(toolResp, runErr, o.maxResult)
			messages = append(messages, sdk.ChatMessage{
				Role:       sdk.RoleTool,
				ToolCallID: tc.ID,
				Content:    content,
			})
			totalCalls++
		}
	}
	return nil, newErr(CodeMaxRounds,
		fmt.Sprintf("maximum orchestration rounds exceeded (%d)", o.maxRounds), nil)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// validateArgs 保证 ToolCall.Args 是合法 JSON 对象。
// 允许 nil / 空 → 视为空对象；非 object（数组 / 字符串 / null 等）一律拒绝。
func validateArgs(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] != '{' {
		return fmt.Errorf("expected JSON object, got %q", firstRune(trimmed))
	}
	var probe map[string]json.RawMessage
	return json.Unmarshal(raw, &probe)
}

// toolResultContent 序列化 tool 执行结果或错误为 role=tool 消息内容，
// 并对超长内容做截断标注（末尾追加 "...[truncated]"，方便模型识别）。
func toolResultContent(resp *command.Response, runErr error, maxBytes int) string {
	var raw []byte
	if runErr != nil {
		payload := map[string]any{"error": runErr.Error()}
		var aerr *sdk.Error
		if errors.As(runErr, &aerr) {
			payload["code"] = aerr.Code
		}
		raw, _ = json.Marshal(payload)
	} else if resp != nil {
		raw = resp.Data
	}
	if len(raw) <= maxBytes {
		return string(raw)
	}
	return string(raw[:maxBytes]) + "...[truncated]"
}

func firstRune(s string) string {
	if s == "" {
		return ""
	}
	return string([]rune(s)[0])
}

func splitFQID(v string) (string, string, bool) {
	p, c, ok := strings.Cut(v, ".")
	if !ok || p == "" || c == "" {
		return "", "", false
	}
	return p, c, true
}
