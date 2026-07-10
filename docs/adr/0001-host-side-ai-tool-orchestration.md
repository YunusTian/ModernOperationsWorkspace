# ADR-0001：AI Tool-use 由宿主侧编排

- 状态：Accepted
- 日期：2026-07-11
- 决策范围：v0.4 AI tool-use

## 背景

MOW 插件运行在独立子进程中。AI Provider 和 `ai.chat*` 位于 AI 插件，Command Engine、连接解析、权限确认和审计位于宿主 Core。模型返回 tool call 后，需要调用 SSH、Docker、Recipe 等现有 Command，再把结果回喂模型。

## 决策

tool-use 循环由宿主侧 `core/ai` 编排器负责。AI 插件只负责 Provider 协议适配以及模型请求/响应，不直接执行其他 MOW Command。

宿主执行顺序：

1. 调用 AI 插件取得文本增量或结构化 tool call。
2. 仅从宿主维护的 allowlist 中解析工具名称。
3. 将 tool call 转为 `command.Request`。
4. 设置 `Caller.Type = CallerAI`，保留父 trace 和 audit 关联信息。
5. 通过同一 `command.Engine` 执行权限、参数、连接、确认与审计链路。
6. 对结果截断和序列化后，以 `role=tool` 消息再次调用 AI Provider。
7. 达到正常结束或任一资源上限时终止循环。

## 原因

- Command Engine 是项目约定的唯一执行入口。
- 只有宿主持有完整的插件注册表、Target 解析器、Confirmer 和 AuditSink。
- 不需要扩展插件协议为“插件反向调用宿主”，避免循环依赖和新的高权限 RPC 面。
- CLI、Desktop、未来 API 可以复用同一编排器，不在各入口重复实现安全策略。

## 安全约束

- v0.4 仅开放显式 allowlist 中的 `PermRead` Command。
- 禁止调用 `ai.*`，避免模型递归调用自身。
- 默认最大 8 轮，每轮最多 4 个 tool call；默认总超时 120 秒。
- 单个工具结果序列化后必须限制大小，默认 64 KiB。
- 模型提供的 tool 名称、参数和 Provider 返回内容均视为不可信输入。
- Dangerous Command 在 v0.4 中不进入工具目录；未来开放也必须走宿主确认器。

## 接口方向

新增宿主包 `core/ai`，依赖一个窄接口而不是具体 Engine：

```go
type CommandRunner interface {
    Run(context.Context, command.Request) (*command.Response, error)
}
```

编排器不依赖 CLI、Wails 或具体 Provider SDK。传输层应提供结构化事件：文本增量、tool call、tool result、usage、finish 和 error。

## 被否决方案

### AI 插件直接持有 Command Engine

子进程无法直接持有宿主对象；复制 Engine 会产生第二套插件注册、连接和审计状态。

### 扩展 gRPC 让插件反向调用宿主

可以实现，但会扩大协议、生命周期和信任边界。v0.4 没有足够收益支撑该复杂度。

### CLI/Desktop 自己执行 tool call

会导致入口间策略漂移，也让未来 API 和 Workflow 再实现一遍相同逻辑。

## 后果

- AI 插件保持低权限 Provider adapter。
- Core 新增 AI 编排领域，但仍不依赖任何厂商 SDK。
- Provider 流式协议需要允许宿主在 tool call 后发起下一轮模型调用。
- v0.4 实现 tool-use 前必须先完成结构化事件和循环边界测试。

