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

## 5. 技术栈决策（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| Core 语言 | **Go 1.22+** | 单二进制、跨平台交叉编译 |
| 桌面 UI | **Wails v2** | WebView 内嵌 + Go 后端同进程 |
| 前端 | **React + TypeScript** | 生态最完整，配合 xterm.js / shadcn/ui |
| CLI | **Cobra** | 事实标准 |
| 配置 | **Viper + TOML** | |
| 日志 | **`log/slog`（Go 标准库）** | 结构化 JSON |
| 仓库布局 | **Monorepo + `go.work`** | apps / core / sdk / plugins |
| Plugin 加载 | **hashicorp/go-plugin（gRPC 子进程）** | Terraform 同款，安全隔离 |

## 6. 待讨论

- [ ] 是否引入 WASM 作为第三方插件沙箱（v1.0+ 再评估）
- [ ] 官方 Plugin 是否需要"进程内加载"作为高性能通道（默认全部走 gRPC）
