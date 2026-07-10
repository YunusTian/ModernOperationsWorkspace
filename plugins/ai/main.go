// Package main 实现 mow-plugin-ai —— 官方 AI 插件（v0.4 骨架）。
//
// v0.4 骨架交付（本 module）：
//   - ai.chat：一次性对话
//   - ai.chat_stream：流式对话（含 tool-use loop 接口，v0.4 尚未接 Command Engine）
//   - ai.list_providers：列出已启用 provider + capabilities
//   - providers/mock.go：默认自带的 mock provider，回声/echo 行为
//
// v0.4.1 承接：OpenAI / Anthropic 真实 provider、Desktop AI 面板、tool-use 闭环。
// 详见 docs/ai-plugin.md。
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
)

// -----------------------------------------------------------------------------
// Plugin 定义
// -----------------------------------------------------------------------------

// AIPlugin 是 MOW 官方 AI 插件。
type AIPlugin struct {
	// providers 是 name → Provider 注册表。plugins/ai 不感知具体实现，
	// 只按 name 路由。默认自带 "mock" provider（永不失败，方便调试）。
	providers map[string]sdk.Provider
	// dataDir Init 时写入；预留供 provider 落 cache（例：本地 embedding）。
	dataDir string
}

func newAIPlugin() *AIPlugin {
	return &AIPlugin{providers: map[string]sdk.Provider{}}
}

func (p *AIPlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{
		ID:              "ai",
		Name:            "AI",
		Version:         "0.4.0-alpha",
		Author:          "mow",
		Description:     "AI Provider abstraction (chat / tool-use)",
		CoreVersion:     ">=0.1.0",
		ConnectionTypes: nil, // AI 命令不消费 target 连接
	}
}

// aiSettings 是 Plugin.Init 里 Settings 字段的解码目标。
//
// providers[] 声明启用哪些 provider；v0.4 骨架只识别 kind="mock"，其它值
// 会记 warning 并跳过（真实厂商 provider 由 v0.4.1 引入）。
type aiSettings struct {
	Providers []providerSettings `json:"providers,omitempty"`
}

type providerSettings struct {
	// Name：provider 实例名，全局唯一；空则默认取 Kind。
	Name string `json:"name"`
	// Kind："mock" / "openai" / "anthropic" / "mcp"（v0.4 只识别 mock）。
	Kind string `json:"kind"`
	// Options：provider 私有配置（例：openai 的 base_url / api_key_env）；
	// v0.4 骨架不消费。
	Options json.RawMessage `json:"options,omitempty"`
}

func (p *AIPlugin) Init(ctx context.Context, req sdk.InitRequest) error {
	p.dataDir = req.DataDir

	var s aiSettings
	if len(req.Settings) > 0 {
		if err := json.Unmarshal(req.Settings, &s); err != nil {
			return fmt.Errorf("ai: decode settings: %w", err)
		}
	}
	// 未配置 → 默认挂一个 mock，保证 ai.list_providers 至少有一项。
	if len(s.Providers) == 0 {
		s.Providers = []providerSettings{{Name: "mock", Kind: "mock"}}
	}
	for _, pc := range s.Providers {
		name := pc.Name
		if name == "" {
			name = pc.Kind
		}
		if _, exists := p.providers[name]; exists {
			return fmt.Errorf("ai: duplicate provider name %q", name)
		}
		prov, err := buildProvider(pc)
		if err != nil {
			return fmt.Errorf("ai: build provider %q (kind=%s): %w", name, pc.Kind, err)
		}
		p.providers[name] = prov
	}
	return nil
}

func (p *AIPlugin) Shutdown(ctx context.Context) error               { return nil }
func (p *AIPlugin) HealthCheck(ctx context.Context) sdk.HealthStatus { return sdk.StatusHealthy }

func (p *AIPlugin) Commands() []sdk.CommandHandler {
	return []sdk.CommandHandler{
		&listProvidersCmd{plugin: p},
		&chatCmd{plugin: p},
		&chatStreamCmd{plugin: p},
	}
}

// buildProvider 是 provider 工厂：把 settings 里的 kind 翻译成具体实现。
// v0.4 骨架只识别 "mock"；未来在此扩展 "openai" / "anthropic" / "mcp"。
func buildProvider(pc providerSettings) (sdk.Provider, error) {
	switch pc.Kind {
	case "", "mock":
		return newMockProvider(pc), nil
	case "openai", "openai-compatible":
		return newOpenAIProvider(pc)
	default:
		return nil, fmt.Errorf("unsupported provider kind %q", pc.Kind)
	}
}

func main() {
	pluginserve.Serve(newAIPlugin())
}
