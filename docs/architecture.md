# RFC: 总体架构

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 三、§ 七

---

## 1. 分层图

```
                       UI Layer
                          │
       ┌──────────────────┼───────────────────┐
       │                  │                   │
    Terminal          Dashboard            AI Chat
       │                  │                   │
       └──────────────────┼───────────────────┘
                          │
                    Command Engine
                          │
                    Workflow Engine
                          │
                     Recipe Engine
                          │
                    Plugin Manager
                          │
        ┌──────────┬──────────┬──────────┐
        │          │          │          │
   SSH Plugin  Docker Plugin  PVE Plugin  AI Plugin
        │          │          │          │
        └──────────┴──────────┴──────────┘
                          │
                  Connection Manager
                          │
              SSH · HTTP · Docker Socket · WS ...
```

## 2. 分层职责

| 层级 | 职责 |
| --- | --- |
| **UI Layer** | Terminal / Dashboard / AI Chat 等交互形式，不放业务逻辑 |
| **Command Engine** | 统一执行入口，找到并调用对应 Plugin 的 Command |
| **Workflow Engine** | 多步 Recipe 编排、失败回滚、重试、通知 |
| **Recipe Engine** | 预定义、经过验证的操作单元 |
| **Plugin Manager** | 插件生命周期、注册、版本、权限 |
| **Connection Manager** | 所有连接的建立、复用、密钥、断线重连 |

**重点原则**：GUI / CLI / AI / API 全部**只通过 Command Engine** 与内核交互，不允许绕过。

## 3. 目录结构

```
project/
├── docs/                  # 架构、RFC、规范
├── apps/
│   ├── desktop/           # 桌面客户端
│   └── cli/               # 命令行入口
├── core/
│   ├── command/           # Command Engine
│   ├── workflow/          # Workflow Engine
│   ├── recipe/            # Recipe Engine
│   ├── plugin/            # Plugin Manager
│   ├── connection/        # Connection Manager
│   ├── config/
│   └── logger/
├── plugins/
│   ├── ssh/
│   ├── docker/
│   ├── pve/
│   └── ai/
├── sdk/                   # Plugin SDK
├── examples/              # Recipe / Workflow 示例
└── tests/
```

## 4. 依赖方向约束

- `apps/*` → `core/*`（可）
- `plugins/*` → `sdk/*`（可）
- `core/*` → `plugins/*`（**禁止**）
- `core/*` → `ai plugin`（**禁止**）
- `plugins/*` → `apps/*`（**禁止**）

## 5. 待讨论

- [ ] Core 与 Plugin 之间的通信采用进程内还是子进程 IPC
- [ ] 是否引入 WASM 作为第三方插件沙箱
