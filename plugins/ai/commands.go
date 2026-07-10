// commands.go —— plugins/ai 的三条 Command 实现。
//
//   - ai.list_providers：列出已启用 provider + capabilities
//   - ai.chat：一次性对话（对齐 Provider.Chat）
//   - ai.chat_stream：流式对话（对齐 Provider.ChatStream；tool-use loop v0.4.1 补齐）
//
// 全部 Command 都通过 params.provider 字段选路由；未指定时用 defaultProvider()。
//
// 设计要点：
//   - 参数模型对齐 OpenAI 兼容 API 的字段名，未来切真实 provider 无需改 UI
//   - Command Engine 会在调用前按 CommandSpec.InputSchema 做校验（这里不重复）
//   - 所有错误统一包成 sdk.NewError；ctx 取消 → ai.CANCELED / TIMEOUT 走 Engine 默认

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// ai.list_providers
// -----------------------------------------------------------------------------

type listProvidersCmd struct {
	plugin *AIPlugin
}

// providerInfo 是 list_providers 返回项，字段名与 Provider.Capabilities 保持一致。
type providerInfo struct {
	Name         string                    `json:"name"`
	Capabilities sdk.ProviderCapabilities  `json:"capabilities"`
}

type listProvidersResult struct {
	Providers []providerInfo `json:"providers"`
}

func (c *listProvidersCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "list_providers",
		Description: "list AI providers currently enabled and their capabilities",
		Permission:  sdk.PermRead,
		Idempotent:  true,
	}
}

func (c *listProvidersCmd) Execute(ctx context.Context, _ *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	result := listProvidersResult{Providers: make([]providerInfo, 0, len(c.plugin.providers))}
	for name, prov := range c.plugin.providers {
		result.Providers = append(result.Providers, providerInfo{
			Name:         name,
			Capabilities: prov.Capabilities(),
		})
	}
	// 稳定顺序：按 name 升序
	sort.Slice(result.Providers, func(i, j int) bool {
		return result.Providers[i].Name < result.Providers[j].Name
	})
	data, err := json.Marshal(result)
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

func (c *listProvidersCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

// -----------------------------------------------------------------------------
// ai.chat
// -----------------------------------------------------------------------------

// chatParams 与 sdk.ChatRequest 相似，多一个 provider 字段用于路由。
type chatParams struct {
	Provider  string             `json:"provider,omitempty"`
	Model     string             `json:"model,omitempty"`
	Messages  []sdk.ChatMessage  `json:"messages"`
	Tools     []sdk.ToolSpec     `json:"tools,omitempty"`
	Temp      float32            `json:"temperature,omitempty"`
	MaxTokens int                `json:"max_tokens,omitempty"`
	Extra     map[string]any     `json:"extra,omitempty"`
}

type chatCmd struct {
	plugin *AIPlugin
}

func (c *chatCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "chat",
		Description: "one-shot chat completion against a configured AI provider",
		Permission:  sdk.PermRead,
		Idempotent:  false, // AI 输出天然不幂等
	}
}

func (c *chatCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	var p chatParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if len(p.Messages) == 0 {
		return nil, sdk.NewError("PARAM_INVALID", "messages must be non-empty", nil)
	}
	prov, err := c.plugin.pick(p.Provider)
	if err != nil {
		return nil, err
	}
	if !prov.Capabilities().Chat {
		return nil, sdk.NewError("AI_CAPABILITY_MISSING",
			fmt.Sprintf("provider %q does not support one-shot Chat", prov.Name()), nil)
	}

	resp, err := prov.Chat(ctx, toChatRequest(p))
	if err != nil {
		return nil, wrapProviderErr(err)
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

func (c *chatCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

// -----------------------------------------------------------------------------
// ai.chat_stream
// -----------------------------------------------------------------------------

type chatStreamCmd struct {
	plugin *AIPlugin
}

func (c *chatStreamCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "chat_stream",
		Description: "streaming chat completion (with future tool-use loop) against a configured AI provider",
		Permission:  sdk.PermExecute, // 未来会代表 AI 触发 Command Engine
		Streaming:   true,
	}
}

func (c *chatStreamCmd) Execute(ctx context.Context, _ *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}

func (c *chatStreamCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	var p chatParams
	if err := decodeParamsFromStream(s, &p); err != nil {
		return err
	}
	if len(p.Messages) == 0 {
		return sdk.NewError("PARAM_INVALID", "messages must be non-empty", nil)
	}
	prov, err := c.plugin.pick(p.Provider)
	if err != nil {
		return err
	}
	if !prov.Capabilities().ChatStream {
		return sdk.NewError("AI_CAPABILITY_MISSING",
			fmt.Sprintf("provider %q does not support ChatStream", prov.Name()), nil)
	}

	sink := &streamSink{stream: s}
	if err := prov.ChatStream(ctx, toChatRequest(p), sink); err != nil {
		return wrapProviderErr(err)
	}
	// provider 已在 OnFinish 里调 stream.Finish；此处只需成功返回。
	return nil
}

// streamSink 把 Provider 的回调翻译成 sdk.Stream 的 Stdout / Event / Finish。
//
// 约定：
//   - OnDelta → Stream.Stdout（UI 端 typewriter 效果）
//   - OnToolCall → Stream.Event（结构化事件，前端解析后另开 tool 执行 UI）
//   - OnFinish → Stream.Finish（把最终 ChatResponse 作为 finalData 返回）
type streamSink struct {
	stream sdk.Stream
}

func (s *streamSink) OnDelta(delta string) error {
	if delta == "" {
		return nil
	}
	return s.stream.Stdout([]byte(delta))
}

func (s *streamSink) OnToolCall(tc sdk.ToolCall) error {
	// v0.4 骨架：直接把 tool call 作为 event 转发给上层；
	// v0.4.1 会替换为"就地调 Command Engine 并把结果 replay 给 provider"。
	return s.stream.Event(map[string]any{
		"type": "tool_call",
		"data": tc,
	})
}

func (s *streamSink) OnFinish(final sdk.ChatResponse) error {
	return s.stream.Finish(final, 0)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// pick 按 name 找 provider；空名走"任意一个"（当前只有一个 → 直接给它；
// 有多个则按名字排序取首个，保证确定性）。
func (p *AIPlugin) pick(name string) (sdk.Provider, error) {
	if name != "" {
		if prov, ok := p.providers[name]; ok {
			return prov, nil
		}
		return nil, sdk.NewError("AI_PROVIDER_NOT_FOUND",
			fmt.Sprintf("provider %q is not configured", name), nil)
	}
	if len(p.providers) == 0 {
		return nil, sdk.NewError("AI_PROVIDER_NOT_FOUND",
			"no AI provider is configured", nil)
	}
	// 单个 → 直接返回；多个 → 排序取首个（确定性）
	names := make([]string, 0, len(p.providers))
	for n := range p.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return p.providers[names[0]], nil
}

func toChatRequest(p chatParams) sdk.ChatRequest {
	return sdk.ChatRequest{
		Model:     p.Model,
		Messages:  p.Messages,
		Tools:     p.Tools,
		Temp:      p.Temp,
		MaxTokens: p.MaxTokens,
		Extra:     p.Extra,
	}
}

// decodeParams 用于非流式 Command，读取 req.Params。
func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed: "+err.Error(), err)
	}
	return nil
}

// decodeParamsFromStream 供流式 Command 使用；等价于 s.Params(&dst)，
// 但错误包成稳定 sdk.Error。
func decodeParamsFromStream(s sdk.Stream, dst any) error {
	if err := s.Params(dst); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed: "+err.Error(), err)
	}
	return nil
}

// wrapProviderErr 把 provider 底层错误映射为稳定 sdk.Error。
// 若已是 *sdk.Error 直接透传；否则统一 AI_PROVIDER_ERROR，标记 retryable。
func wrapProviderErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sdk.Error
	if errors.As(err, &se) {
		return se
	}
	return sdk.NewError("AI_PROVIDER_ERROR", err.Error(), err).WithRetryable(true)
}
