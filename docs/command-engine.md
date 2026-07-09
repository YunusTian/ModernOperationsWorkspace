# RFC: Command Engine

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 4.3

---

## 1. 定位

Command Engine 是**唯一**的执行入口。UI / CLI / API / AI 全部通过它调用 Plugin。

## 2. 执行流程

```
RunCommand(pluginId, commandId, params)
        │
        ▼
   PluginManager 查找 Plugin
        │
        ▼
   校验权限 & 参数
        │
        ▼
   执行 Command
        │
        ▼
   返回 Result（可序列化）
```

## 3. Command 契约

- 每条 Command 都有**唯一 ID**（例：`ssh.exec` / `docker.list` / `pve.startVM`）
- 输入 / 输出 / 错误**全部可序列化**（便于 CLI、API、AI 调用）
- 支持**同步 / 异步 / 流式**（如 Terminal 输出）

## 4. 接口草案

```text
CommandRequest
├── pluginId: string
├── commandId: string
├── params: object            # JSON Schema 校验
├── connectionId?: string
├── timeout?: duration
└── stream?: bool             # 是否流式返回

CommandResult
├── ok: bool
├── data?: object
├── error?: { code, message, details }
├── duration: ms
└── auditId: string
```

## 5. 权限与审计

- 执行前进行权限校验（详见 [permission.md](./permission.md)）
- 每次调用生成 `auditId`，写入审计日志（详见 [observability.md](./observability.md)）
- Dangerous Command 必须由调用方提供二次确认标记

## 6. 技术选型（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| 实现语言 | **Go 1.22+** | Core module `core/command` |
| 参数 Schema | **JSON Schema** | 与 gRPC + JSON 序列化对齐 |
| 流式传输 | **gRPC server-stream** | Terminal / Log 跟随天然适配 |
| 取消传播 | **`context.Context`** | Go 标准实践 |
| 序列化 | Protobuf（Plugin 边界）/ JSON（对外 API） | |

## 7. 待讨论

- [ ] Command 版本升级与向后兼容策略（`ssh.exec@v1` vs `ssh.exec@v2`）
- [ ] Timeout 与 Cancel 的组合语义边界（是否允许 Command 忽略 cancel）
- [ ] AuditId 的生成方式（ULID / UUIDv7）
