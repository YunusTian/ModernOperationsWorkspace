<div align="center">

# Modern Operations Workspace（MOW）

**AI is optional. Automation is essential.**

一款 AI Native，但**不依赖 AI** 的现代化跨平台运维工作台。

`Core First` · `AI Optional` · `Plugin Everything`

[![License](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](./LICENSE)
[![Status](https://img.shields.io/badge/status-Draft_v0.1-orange.svg)](./Architecture.md)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8.svg)](https://go.dev)
[![Wails](https://img.shields.io/badge/Wails-v2-DF0000.svg)](https://wails.io)

</div>

---

## 项目定位

MOW 是一款面向**开发者与运维工程师**的跨平台运维工作台：

- 无论 AI 是否可用，本软件都具备完整的运维能力
- 即使完全不接入 AI，它依然是一款优秀的 **SSH / Docker / PVE** 运维工具
- 接入 AI 后，只是让操作更智能、更高效

> **Core 永远不依赖 AI，AI 永远依赖 Core。**

## 核心特性

- 🖥️ **跨平台**：Windows / Linux / macOS 一致体验
- 🔌 **Plugin Everything**：SSH / Docker / PVE / K8s / AI 都是插件
- ⚙️ **统一执行链路**：GUI / CLI / AI / API 全部走同一 Command Engine
- 📜 **Recipe & Workflow**：把运维经验沉淀为可复用、可编排的资产
- 🔐 **Safety First**：所有 Command 声明权限，危险操作强制二次确认
- 📊 **完全可观测**：审计、回放、AI 决策链路可追溯

## 技术栈

| 层 | 选型 |
| --- | --- |
| Core | Go 1.22+ |
| 桌面客户端 | Wails v2 + React + TypeScript + xterm.js + shadcn/ui |
| CLI | Cobra |
| Plugin 加载 | [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin)（gRPC 子进程） |
| 配置 / 日志 | Viper + `log/slog` |
| 仓库布局 | Monorepo + `go.work` |

## 目录结构

```
├── docs/            # 架构总纲与各模块 RFC
├── apps/
│   ├── desktop/     # Wails 桌面客户端
│   └── cli/         # Cobra CLI
├── core/            # Core 各模块
├── sdk/             # Plugin SDK（gRPC + Go 抽象）
├── plugins/         # 官方 Plugin（v0.1 仅 ssh）
├── examples/        # Recipe / Workflow 示例
└── tests/
```

## 文档

- 📘 [Architecture.md](./Architecture.md) — 架构总纲
- 📁 [docs/](./docs) — 各模块 RFC 索引
  - [vision](./docs/vision.md) · [design principles](./docs/design-principles.md) · [architecture](./docs/architecture.md)
  - [command engine](./docs/command-engine.md) · [recipe](./docs/recipe.md) · [workflow](./docs/workflow.md)
  - [plugin system](./docs/plugin-system.md) · [connection manager](./docs/connection-manager.md)
  - [ai](./docs/ai.md) · [ui](./docs/ui.md) · [permission](./docs/permission.md) · [observability](./docs/observability.md)
  - [roadmap](./docs/roadmap.md)

## 快速开始

> ⚠️ 本项目当前处于 **v0.1 Draft**，代码尚未开始实现。

一旦 v0.1 骨架落地，运行方式将会是：

```powershell
# 桌面客户端
cd apps/desktop
wails dev

# CLI
cd apps/cli
go run . --help
```

## Roadmap

| 版本 | 目标 |
| --- | --- |
| **v0.1** | 优秀的 SSH 客户端 + Plugin Framework 雏形（不接入 AI） |
| **v0.2** | Command / Recipe / Workflow Engine |
| **v0.3** | Docker Plugin + Docker Dashboard |
| **v0.4** | AI Plugin + Provider 抽象（含 MCP 支持） |
| **v0.5** | PVE / K8s / DB Plugin + Marketplace 雏形 |

详见 [docs/roadmap.md](./docs/roadmap.md)。

## 参与贡献

欢迎所有形式的贡献——**尤其欢迎新的 Plugin**。请先阅读 [CONTRIBUTING.md](./CONTRIBUTING.md)。

## 设计原则速览

| 原则 | 说明 |
| --- | --- |
| Core First | 核心能力先于 UI |
| AI Optional | AI 是可选能力 |
| Plugin Everything | 新能力优先做成插件 |
| Workflow over Script | 沉淀为可复用 Workflow |
| API First | Core 对外统一 API |
| Safety First | 危险操作强制二次确认 |
| Observable | 可追踪、可审计、可回放 |
| Domain Driven | 抽象领域模型，而非 Shell |

## License

Licensed under the [Apache License, Version 2.0](./LICENSE).
