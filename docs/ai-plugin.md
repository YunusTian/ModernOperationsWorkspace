# AI Plugin (v0.4 · 初稿)

> 状态：🚧 v0.4 实现中 · Provider 接口、mock 与三条 Command 已完成；正式发布范围见 [v0.4 验收清单](./v0.4-acceptance-checklist.md)。
> 1. `sdk` 侧的 Provider / Chat / Tool 抽象；
> 2. `plugins/ai` 骨架（含 mock provider + 三条 Command Handlers）；
> 3. 与现有 Command Engine / Workflow 融合的调用协议。
>
> **v0.4 不包含**：真实 OpenAI / Anthropic / MCP 客户端实现（放到 v0.4.1+），
> 也不包含桌面 AI 对话 UI（放到 v0.4.2）。

## 1. 目标

- 让 MOW 拥有一个**统一的 AI 能力抽象**：Provider 与业务代码解耦，切换厂商不影响
  Command / Workflow 消费方
- 让 AI 具备"**调用 MOW 里其它 Command**"的能力（tool-use / function calling）：
  借用 Command Engine 的权限模型、审计与二次确认
- 保留 v0.5+ 的 MCP（Model Context Protocol）接入点：Provider 抽象与 MCP
  Server/Client 两种形态在同一接口下共存

**非目标**：

- **不**在 v0.4 引入具体供应商 SDK 依赖（keep dependency tree clean）
- **不**做 fine-tuning / embedding vector store（视为独立 v0.5 议题）
- **不**做流式的图片 / 语音（先做文本 chat）

## 2. 分层

```
┌───────────────────────────────────────────────────────────────┐
│  UI / Workflow / CLI (调用方)                                 │
│    ai.chat / ai.complete / ai.tools —— 都是普通 Command       │
└───────────────────────┬───────────────────────────────────────┘
                        │  Command Engine (已有 · v0.2)
                        ▼
┌───────────────────────────────────────────────────────────────┐
│  plugins/ai (新增 · v0.4)                                     │
│    - chatCmd / completeCmd / toolsCmd (CommandHandler)        │
│    - Provider registry (from InitRequest.Settings)            │
└───────────────────────┬───────────────────────────────────────┘
                        │  sdk.Provider (新增接口 · v0.4)
                        ▼
┌────────────────────────────────┬──────────────────────────────┐
│  MockProvider (v0.4)           │  OpenAIProvider (v0.4.1)     │
│  EchoProvider (测试用)         │  AnthropicProvider (v0.4.1)  │
│                                │  MCPProvider (v0.5)          │
└────────────────────────────────┴──────────────────────────────┘
```

**关键设计**：`sdk.Provider` 是**唯一的 AI 抽象**；plugins/ai 不感知具体供应商。
供应商实现放到 `plugins/ai/providers/*.go`，通过 Settings 的 `provider: "openai"`
选择。

## 3. sdk 侧接口（v0.4 骨架）

新增文件：`sdk/ai.go`（本次交付）

```go
package sdk

// Provider 是 AI 供应商抽象。
// plugins/ai 通过它统一屏蔽底层差异（OpenAI / Anthropic / MCP / 本地模型等）。
//
// 实现方约定：
//   - Chat / Complete 应支持 ctx 取消 —— 上层 Command 会依此实现流式中断
//   - 返回错误建议包成 sdk.Error 附上 Retryable / RateLimited 属性
type Provider interface {
    Name() string
    Capabilities() ProviderCapabilities

    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest, stream ChatStreamSink) error
}

type ProviderCapabilities struct {
    Chat       bool
    ChatStream bool
    ToolCalls  bool
    Models     []string
}

type ChatMessage struct {
    Role    string // system / user / assistant / tool
    Content string
    // 可选：AI 侧生成的 tool_calls；plugins/ai 会把这些翻译成一次 Command Engine 调用
    ToolCalls []ToolCall
    ToolCallID string
}

type ToolCall struct {
    ID   string
    Name string
    Args json.RawMessage
}

type ChatRequest struct {
    Model    string
    Messages []ChatMessage
    Tools    []ToolSpec
    Temp     float32
    MaxTokens int
    // 供 MCP / provider 传自定义参数
    Extra map[string]any
}

type ChatResponse struct {
    Message  ChatMessage
    Usage    ChatUsage
    Finish   string // stop / length / tool_calls
}

type ChatStreamSink interface {
    OnDelta(delta string) error       // 文本增量
    OnToolCall(tc ToolCall) error     // tool call 增量
    OnFinish(final ChatResponse) error
}
```

## 4. plugins/ai 命令

**v0.4 骨架**只上三条命令，全部对齐 `ai.` 前缀：

| Command | 用途 | Streaming |
|---|---|---|
| `ai.chat` | 一次性对话 | ❌ |
| `ai.chat_stream` | 流式对话（含 tool-use loop） | ✅ |
| `ai.list_providers` | 返回已启用 provider + capabilities | ❌ |

**Provider 注册**：`InitRequest.Settings.providers` 数组，示例：

```yaml
providers:
  - name: mock         # v0.4 骨架自带；始终可用
    kind: mock
  - name: openai       # v0.4.1 引入真实实现
    kind: openai
    api_key_env: OPENAI_API_KEY
```

**tool-use 循环**由宿主侧编排，不在 AI 插件子进程内执行。宿主检测到
`ToolCall` 后通过 `command.Engine.Run` 调用 allowlist 中的只读 Command，拿到结果拼成
`role=tool` message，再调用 AI 插件续写。详见 [ADR-0001](./adr/0001-host-side-ai-tool-orchestration.md)。

## 5. 权限与审计

- **默认 Permission**：`ai.chat` = `PermRead`；`ai.chat_stream` = `PermExecute`
- **Tool-use 时**：真实 Command 的 Permission 一样生效；Dangerous 命令仍需
  `Confirmed=true`，AI 无权绕过
- **Caller**：Engine 会把 `Caller.Type = CallerAI` 传给下游 Command，供审计过滤

## 6. v0.4 交付范围（本文档一并落地）

- [x] `docs/ai-plugin.md`（本文件）
- [x] `sdk/ai.go` —— Provider / ChatMessage / ChatRequest / ... 骨架
- [x] `plugins/ai/` 骨架：`main.go` / `providers.go` / `commands.go`
- [x] 单测：Provider 注册、mock provider 的 chat / stream、ai.list_providers
- [x] 更新 `roadmap.md` / `README.md`

**v0.4.1 承接**：

- OpenAI / Anthropic Provider 真实实现（含 rate limit / retry）
- 宿主侧 tool-use 完整闭环（`ToolCall` → Engine.Run → 续写）
- CLI AI 入口与 Desktop AI Chat 面板

**v0.5 承接**：

- MCP Server / Client 双向对接（既能作为 MCP 客户端调外部 MCP Server，也能把 MOW
  自己的 Command 暴露成 MCP Server）
- Embedding / vector store（RAG）
