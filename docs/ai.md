# RFC: AI 架构

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 六

---

## 1. 核心立场

**AI 永远不是核心，AI 只是一种入口。**

## 2. 调用链

```
User Prompt
    │
    ▼
Planner（LLM）─── 读取 Plugin Metadata / Recipe / Workflow 目录
    │
    ▼
选择 Workflow / Recipe / Command
    │
    ▼
Command Engine
    │
    ▼
Plugin → Connection → Target
```

## 3. AI 的职责

- 理解用户 → **选择或组合已有能力**
- 解释执行结果（自然语言总结）
- 提出下一步建议

## 4. AI 不能做的事

- 直接生成任意 Shell 并执行
- 绕过 Command Engine
- 直接访问 Connection Manager
- 未经权限确认执行 Dangerous 操作

## 5. AI 作为 Plugin

AI 本身也是一个 Plugin，Provider 可插拔：

```
AI Plugin
 └── Provider
      ├── ChatGPT
      ├── Claude
      ├── Gemini
      ├── Qwen
      ├── DeepSeek
      └── Local (Ollama / vLLM ...)
```

## 6. 示例

用户："为什么 Docker 起不来？"

AI 决策：

```
Run Recipe: docker.status
      ↓
Run Recipe: system.logs (docker)
      ↓
Run Recipe: system.systemd (docker)
      ↓
汇总分析并解释
```

## 7. Provider 抽象草案

```text
IAiProvider
├── Id
├── Chat(messages, tools) -> stream
├── Embed(text) -> vector
├── Capabilities        # tool_use / vision / streaming ...
└── Config              # apiKey / endpoint / model
```

## 8. 技术选型（v0.4 前不启动实现，此处仅锁定方向）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| AI Plugin 载体 | **独立 Plugin（hashicorp/go-plugin）** | 与其他 Plugin 一致 |
| Tool 调用协议 | **MCP（Model Context Protocol）** | 未来主流，官方生态兼容 |
| SDK | `sashabaranov/go-openai`（OpenAI 兼容协议） | 兼容 DeepSeek / Qwen / 本地 vLLM |
| Provider 抽象 | Core 定义接口，Plugin 实现 | ChatGPT / Claude / Gemini / Local |
| 上下文压缩 | 内置摘要策略 + 分片检索 | v0.4 起步 |

## 9. 待讨论

- [ ] Tool 定义如何自动从 Command / Recipe 目录生成
- [ ] 本地模型的分发与更新（是否内置 Ollama 集成）
- [ ] Prompt 版本化与回放的存储位置
- [ ] Token 计费与限额（企业场景）
- [ ] Dangerous 操作的 AI 二次确认 UI 交互
