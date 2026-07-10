// providers.go —— v0.4 骨架自带的 mock provider。
//
// 目的：
//   - 让 plugins/ai 在完全没有配置 API key 的机器上也能被 Init / 调 chat；
//   - 给 v0.4.1 真实 provider（OpenAI / Anthropic）留一个稳定的对照参照；
//   - 单测里用 mock 走完 CommandHandler 主路径，不依赖网络。
//
// 行为：
//   - Chat：把最后一条 user 消息 echo 回去，前缀 "[mock] "
//   - ChatStream：把 echo 结果按 4 字符切片，逐块回调 OnDelta；结束回调 OnFinish
//   - 声明 Capabilities: Chat + ChatStream；ToolCalls=false（v0.4.1 再加）
//
// 之所以 mock 不走网络：
//   - CI / 离线环境仍能跑 chat_stream 用例
//   - 用户第一次装 mow 就能 "ai.chat" 试出效果

package main

import (
	"context"
	"strings"

	"github.com/mow/mow/sdk"
)

// mockProvider 是可预测行为的 Provider 实现。
type mockProvider struct {
	name string
}

func newMockProvider(pc providerSettings) *mockProvider {
	name := pc.Name
	if name == "" {
		name = "mock"
	}
	return &mockProvider{name: name}
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Capabilities() sdk.ProviderCapabilities {
	return sdk.ProviderCapabilities{
		Chat:       true,
		ChatStream: true,
		ToolCalls:  false, // v0.4.1 补齐
		Models:     []string{"mock-echo"},
	}
}

// Chat 直接生成 echoed 回复。
func (m *mockProvider) Chat(ctx context.Context, req sdk.ChatRequest) (*sdk.ChatResponse, error) {
	// 尊重 ctx —— 上层可能已 timeout / canceled
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply := mockReply(req.Messages)
	return &sdk.ChatResponse{
		Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: reply},
		Usage: sdk.ChatUsage{
			PromptTokens:     approxTokens(req.Messages),
			CompletionTokens: approxTokensOf(reply),
			TotalTokens:      approxTokens(req.Messages) + approxTokensOf(reply),
		},
		Finish: sdk.FinishStop,
	}, nil
}

// ChatStream 分片回调 OnDelta，结束 OnFinish。
func (m *mockProvider) ChatStream(ctx context.Context, req sdk.ChatRequest, sink sdk.ChatStreamSink) error {
	reply := mockReply(req.Messages)
	// 4 char 一片；短消息只会有 1~2 片。
	const chunk = 4
	for i := 0; i < len(reply); i += chunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := i + chunk
		if end > len(reply) {
			end = len(reply)
		}
		if err := sink.OnDelta(reply[i:end]); err != nil {
			return err
		}
	}
	final := sdk.ChatResponse{
		Message: sdk.ChatMessage{Role: sdk.RoleAssistant, Content: reply},
		Usage: sdk.ChatUsage{
			PromptTokens:     approxTokens(req.Messages),
			CompletionTokens: approxTokensOf(reply),
			TotalTokens:      approxTokens(req.Messages) + approxTokensOf(reply),
		},
		Finish: sdk.FinishStop,
	}
	return sink.OnFinish(final)
}

// mockReply 抽取最后一条 user 消息，前缀 "[mock] "。
// 找不到 user 消息时返回 "[mock] (no user input)"，保证输出始终非空。
func mockReply(msgs []sdk.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sdk.RoleUser {
			return "[mock] " + strings.TrimSpace(msgs[i].Content)
		}
	}
	return "[mock] (no user input)"
}

// approxTokens / approxTokensOf 用"4 char ≈ 1 token"经验估算，
// 避免拉入真实 tokenizer 依赖；仅供 UI / 计费展示的近似值。
func approxTokens(msgs []sdk.ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += approxTokensOf(m.Content)
	}
	return total
}

func approxTokensOf(s string) int {
	if s == "" {
		return 0
	}
	// +3 是为了对短字符串（"hi"）也算成 1 token，符合直觉。
	return (len(s) + 3) / 4
}
