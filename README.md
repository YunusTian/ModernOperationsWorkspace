<div align="center">

# Modern Operations Workspace（MOW）

**AI is optional. Automation is essential.**

一款 AI Native，但**不依赖 AI** 的现代化跨平台运维工作台。

`Core First` · `AI Optional` · `Plugin Everything`

[![License](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](./LICENSE)
[![Status](https://img.shields.io/badge/status-v0.5.3_released-brightgreen.svg)](./CHANGELOG.md)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)
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
| Core | Go 1.25+ |
| 桌面客户端 | Wails v2 + React + TypeScript + xterm.js + shadcn/ui |
| CLI | Cobra |
| Plugin 加载 | [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin)（gRPC 子进程） |
| 配置 / 日志 | Viper + `log/slog` |
| 仓库布局 | Monorepo + `go.work` |

## 目录结构

```
├── docs/            # 架构总纲与各模块 RFC（含 workflow / docker-plugin / ai-plugin）
├── apps/
│   ├── desktop/     # Wails 桌面客户端（Terminal / SFTP / Targets / Docker Dashboard / Workflow / History）
│   └── cli/         # Cobra CLI（target / ssh / run / recipe / workflow）
├── core/            # Core 模块（含 workflow / workflow/history 等 8 个子包）
│   ├── command/     # Command Engine + Middleware + Audit
│   ├── connection/  # Connection Manager + Keystore（含 docker credentials）
│   ├── plugin/      # Plugin Manager + Loader
│   ├── recipe/      # Recipe Registry + Runner
│   ├── workflow/    # Workflow YAML DSL + Runner + ${var} 插值（v0.2）
│   │   └── history/ # 执行历史 JSONL 持久化 + 轮转 + 跨进程锁（v0.3 / v0.3.1）
│   ├── config/      # 配置管理
│   └── logger/      # 结构化日志
├── sdk/             # Plugin SDK（gRPC + Protobuf + Go 抽象；含 sdk/ai.go v0.4 Provider 抽象）
├── plugins/
│   ├── ssh/         # 官方 SSH Plugin（exec / shell / sftp / ping）
│   ├── docker/      # 官方 Docker Plugin（v0.3：list / inspect / lifecycle / logs / rm / pull / push / exec / images / volumes / networks）
│   └── ai/          # 官方 AI Plugin（v0.4 骨架：mock provider + list_providers / chat / chat_stream）
├── examples/
│   ├── recipes/
│   └── workflows/   # deploy-static-site.yaml 等示例
├── tests/
│   └── e2e/         # 端到端测试（SSH E2E + Workflow E2E + Docker E2E · 后者需 MOW_DOCKER_E2E=1）
└── scripts/         # lint / race / CI 脚本
```

## 文档

- 📘 [Architecture.md](./Architecture.md) — 架构总纲
- 📝 [CHANGELOG.md](./CHANGELOG.md) — 版本变更日志（v0.1 → v0.5.3）
- 📁 [docs/](./docs) — 各模块 RFC 索引
  - [vision](./docs/vision.md) · [design principles](./docs/design-principles.md) · [architecture](./docs/architecture.md)
  - [command engine](./docs/command-engine.md) · [recipe](./docs/recipe.md) · [workflow](./docs/workflow.md)
  - [plugin system](./docs/plugin-system.md) · [ssh plugin](./docs/ssh-plugin.md) · [docker plugin](./docs/docker-plugin.md) · [ai plugin](./docs/ai-plugin.md) · [connection manager](./docs/connection-manager.md)
  - [permission](./docs/permission.md) · [observability](./docs/observability.md) · [ai](./docs/ai.md) · [ui](./docs/ui.md)
  - [roadmap](./docs/roadmap.md) · 验收清单：[v0.1](./docs/v0.1-acceptance-checklist.md) · [v0.2](./docs/v0.2-acceptance-checklist.md) · [v0.3](./docs/v0.3-acceptance-checklist.md) · [v0.4](./docs/v0.4-acceptance-checklist.md) · [v0.4.1](./docs/v0.4.1-acceptance-checklist.md) · [v0.5.0](./docs/v0.5.0-acceptance-checklist.md) · [v0.5.1](./docs/v0.5.1-acceptance-checklist.md) · [v0.5.2](./docs/v0.5.2-acceptance-checklist.md) · [v0.5.3](./docs/v0.5.3-acceptance-checklist.md)

## 快速开始

### 环境要求

- Go 1.25+
- Node.js 18+（仅桌面端）
- [Wails CLI](https://wails.io/docs/gettingstarted/installation)（仅桌面端）

### 运行

```powershell
# 1. 编译官方插件（SSH / Docker / AI）
cd plugins/ssh
go build -o ssh.exe .
cd ..\docker
go build -o docker.exe .
cd ..\ai
go build -o ai.exe .

# 2. 启动 CLI
cd ..\..\apps\cli
go run . --help                  # 查看帮助
go run . target add my-server `
  --host 192.168.1.100 `
  --port 22 `
  --user root `
  --password mypass
go run . ssh my-server           # 交互式 SSH Shell
go run . run my-server uptime    # 单次执行命令

# v0.2 / v0.3：Workflow（含 v0.3 新增的 parallel / when / retry / on_failure / rollback）
go run . workflow validate ..\..\examples\workflows\deploy-static-site.yaml
go run . workflow run ..\..\examples\workflows\deploy-static-site.yaml `
  --target=my-server `
  --input site=hello `
  --input local_dir=C:\dist `
  --input remote_dir=/var/www/hello `
  --input health_port=8080

# v0.3：查看 Workflow 执行历史（JSONL 持久化，支持轮转 + 跨进程锁）
go run . workflow history list --limit 20
go run . workflow history show <run-id>

# 3. 启动桌面客户端（含 Terminal / SFTP / Targets / Docker Dashboard / Workflow / History）
cd ..\desktop
wails dev

# 4. 运行全部测试
cd ..\..\tests\e2e
$env:MOW_SSH_PLUGIN = "../../plugins/ssh/ssh.exe"
go test -count=1 ./...
# 可选：Docker E2E（需要本机可达的 Docker daemon）
$env:MOW_DOCKER_E2E = "1"
$env:MOW_DOCKER_PLUGIN = "../../plugins/docker/docker.exe"
go test -count=1 -run TestDockerE2E ./...
```

### 运行截图

![MOW Desktop v0.1.0](images/v0.1.0.png)

### v0.1 交付状态

| 模块 | 文件 | 测试数 | 状态 |
|------|------|--------|------|
| Core | 18 文件 / 2,317 行 | 47 PASS | 已交付 |
| SDK | 13 文件 / 1,878 行 | — | 已交付 |
| SSH Plugin | 6 文件 / 1,436 行 | 12 UT + 10 E2E | 已交付 |
| CLI | 10 文件 / 1,249 行 | — | 已交付 |
| Desktop | 3 Go + 3 TSX | — | 已交付 |
| SFTP E2E | 新增 | 9 E2E | 已交付 |
| Shell E2E | 新增 | 4 E2E | 已交付 |
| 文档 | 16 篇 | — | 已交付 |

**自动化测试通过：76/76 | E2E 通过：23/23 | 手动验收：42/42**  
详见 [v0.1 验收清单](./docs/v0.1-acceptance-checklist.md)

### 最新交付（v0.5.3 已发布 · Release Smoke Patch）

- **v0.3.0**（[CHANGELOG](./CHANGELOG.md#v030---2026-07-10)）：Docker Plugin（unix / tcp / tcp+TLS · 13 条命令）+ Docker Dashboard + Workflow 五批增强（when / retry / on_failure / rollback / parallel / JSONL 历史）
- **v0.3.1**（[CHANGELOG](./CHANGELOG.md#v031---2026-07-10)）：稳定性补丁 —— `plugins/docker` 覆盖率 **76.0%**；JSONL 轮转 + 保留策略 + 损坏行恢复 + 跨进程锁（`flock` / `LockFileEx`）；Windows `npipe://` 真实实现（go-winio）；TLS `docker.exec` raw-hijack；Docker E2E 接入常规 CI pipeline
- **v0.4.0**（[CHANGELOG](./CHANGELOG.md#v040---2026-07-11) · [v0.4 验收清单](./docs/v0.4-acceptance-checklist.md)）：AI 可用闭环 —— OpenAI-compatible Provider + 宿主 tool-use Orchestrator + 决策链路审计 + CLI/Desktop 端到端
- **v0.4.1**（[验收清单](./docs/v0.4.1-acceptance-checklist.md)）：GA 收尾 —— 统一版本源、SDK 契约测试、三平台 Release 安装 Smoke、v0.3/v0.4 配置迁移
- **v0.5.0**（[验收清单](./docs/v0.5.0-acceptance-checklist.md)）：插件平台化 · 地基 —— Plugin Manifest + `mow plugin validate` + 包加载/真实 checksum + Manifest Gate 两道运行时关卡
- **v0.5.1**（[验收清单](./docs/v0.5.1-acceptance-checklist.md)）：插件平台化 · 生命周期 —— `install / update / uninstall / doctor` + 本地 Catalog + Desktop Marketplace + Release Catalog Workflow
- **v0.5.2**（[验收清单](./docs/v0.5.2-acceptance-checklist.md)）：插件平台化 · 闭环 —— Schema 驱动配置 UI（CLI + Desktop）+ Secret sidecar + PVE 只读参考插件（11 条命令）+ 四插件兼容矩阵 CI
- **v0.5.3**（[验收清单](./docs/v0.5.3-acceptance-checklist.md)）：Release Smoke Patch —— Windows install-smoke 的 catalog 平台过滤修复（`ConvertTo-LocalCatalog`）+ 离线 PowerShell 回归测试 + CI Windows 门禁；v0.5.2 的 patch，SDK / Manifest / Plugin Protocol 完全不变

## Roadmap

| 版本 | 目标 | 状态 |
| --- | --- | --- |
| **v0.1** | 优秀的 SSH 客户端 + Plugin Framework 雏形（不接入 AI） | ✅ 已发布 |
| **v0.2** | Command / Recipe / Workflow Engine（YAML DSL + `${var}` 插值 + Runner） | ✅ 已发布 |
| **v0.3** | Docker Plugin + Docker Dashboard + Workflow 增强（parallel / when / on_failure / retry / rollback / 执行历史 JSONL） | ✅ 已发布（v0.3.0） |
| **v0.3.1** | 稳定性补丁：Docker 覆盖率 76.0% · JSONL 轮转+跨进程锁 · Windows npipe（go-winio） · TLS exec raw-hijack · Docker E2E 接入 CI | ✅ 已发布 |
| **v0.4** | AI 可用闭环：OpenAI-compatible Provider + 宿主 tool-use Orchestrator + 决策链路审计 + CLI/Desktop 端到端 | ✅ 已发布（v0.4.0） |
| **v0.4.1** | GA 收尾：版本一致性、SDK 契约、安装 Smoke、配置迁移 | ✅ 已发布 |
| **v0.5.0** | 插件平台化 · 地基：Plugin Manifest + `plugin validate` + 包加载/真实 checksum/Release Smoke | ✅ 已发布 |
| **v0.5.1** | 插件平台化 · 生命周期：install / upgrade / uninstall + 本地 Catalog + Desktop Marketplace | ✅ 已发布 |
| **v0.5.2** | 插件平台化 · 闭环：Schema 驱动配置 UI + Secret sidecar + PVE 参考实现 | ✅ 已发布（tag `v0.5.2` 已推送；Windows install-smoke 由 v0.5.3 修复）|
| **v0.5.3** | Release Smoke Patch：Windows catalog 平台过滤修复（v0.5.2 的 patch，不引入新特性） | ✅ 已发布 |
| **v0.6** | Workflow 2.0：版本化、子工作流、审批、调度、通知、SQLite 历史 | 📋 计划中 |
| **v0.7** | 基础设施扩展：PVE 正式版 + Kubernetes MVP | 📋 计划中 |
| **v0.8** | 可观测与诊断中心 | 📋 计划中 |
| **v0.9** | AI Operations 2.0：Plan、MCP、本地模型、知识接入 | 📋 计划中 |
| **v1.0** | SDK/Protocol 稳定、安装升级迁移、长期验证 | 📋 计划中 |

详见 [docs/roadmap.md](./docs/roadmap.md) 与 [v0.5～v1.0 详细开发计划](./docs/development-plan-v0.5-v1.0.md)。

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
