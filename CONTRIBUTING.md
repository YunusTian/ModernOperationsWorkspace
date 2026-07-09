# Contributing to MOW

感谢参与 **Modern Operations Workspace（MOW）**。在提交代码或文档之前，请通读本指南。

---

## 一、核心设计原则（必须遵守）

以下 8 条原则源自 [docs/design-principles.md](./docs/design-principles.md)，任何 PR 在 Review 时都会以此为准绳。

| 原则 | 说明 |
| --- | --- |
| **Core First** | 核心能力先于 UI，所有界面都调用同一套 Core |
| **AI Optional** | AI 是可选能力，产品不能依赖 AI 才能使用 |
| **Plugin Everything** | 新能力优先做成插件，而不是直接修改 Core |
| **Workflow over Script** | 把经验沉淀为可复用 Workflow，而不是一次性脚本 |
| **API First** | Core 对外提供统一 API，CLI / GUI / AI 都通过 API 调用 |
| **Safety First** | 危险操作必须权限检查与二次确认 |
| **Observable** | 每个动作都可追踪、可审计、可回放 |
| **Domain Driven** | 抽象领域模型，而不是协议 / Shell |

### 强约束（PR 会被直接拒绝）

- ❌ Core 模块 import 图中出现 AI Plugin
- ❌ UI 层直接调用 Plugin / Connection
- ❌ 新能力直接改 Core，而不是做成 Plugin
- ❌ Command 未声明 `permission`
- ❌ Dangerous 操作未走二次确认

---

## 二、开发环境

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.22+ | Core / CLI / Plugins |
| Node.js | 20 LTS+ | Wails 前端 |
| pnpm | 9+ | 前端包管理 |
| Wails CLI | v2.x | 桌面客户端构建 |
| protoc | 25+ | 生成 gRPC 代码 |

安装 Wails 与 protoc 插件：

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

---

## 三、仓库布局（Monorepo + go.work）

```
apps/desktop     Wails 桌面客户端
apps/cli         Cobra CLI
core/            Core 各模块（command / workflow / plugin / connection / ...）
sdk/             Plugin SDK（gRPC proto + Go 抽象）
plugins/         官方 Plugin（ssh / docker / ...）
docs/            RFC / 架构文档
```

**依赖方向必须遵守**：

- `apps/*` → `core/*` ✅
- `plugins/*` → `sdk/*` ✅
- `core/*` → `plugins/*` ❌
- `core/*` → `ai plugin` ❌
- `plugins/*` → `apps/*` ❌

---

## 四、开发流程

### 4.1 提 Issue

- **Bug**：请附最小复现步骤、日志、系统信息
- **Feature**：先讨论是否符合"Plugin Everything"，能做成插件的不改 Core
- **RFC 级变动**：先在 `docs/` 提交新 RFC 或对已有 RFC 的 PR

### 4.2 分支策略

- `main`：稳定分支，只接受通过 CI 的 PR
- `feat/<name>`：新特性
- `fix/<name>`：Bug 修复
- `docs/<name>`：文档
- `rfc/<name>`：架构变更

### 4.3 Commit 规范（Conventional Commits）

```
<type>(<scope>): <subject>

<body>

<footer>
```

**type**：`feat` / `fix` / `docs` / `refactor` / `test` / `chore` / `perf` / `build` / `ci`

示例：

```
feat(plugin): add SSH plugin exec command
fix(core/connection): reconnect on EOF
docs(rfc): update plugin-system with go-plugin decision
```

### 4.4 PR Checklist

- [ ] 通过 `go build ./...` 与 `go test ./...`
- [ ] 通过 `go vet ./...` 与 `golangci-lint run`
- [ ] 前端通过 `pnpm typecheck` 与 `pnpm lint`
- [ ] 新增 Command 已声明 `permission`
- [ ] 新增 API 已在相关 RFC 中同步
- [ ] 涉及危险操作已实现二次确认
- [ ] 关键路径覆盖单元测试

---

## 五、代码风格

### Go

- 遵循 `gofmt` / `goimports`
- 优先使用 Go 1.22+ 语言特性
- 错误处理：显式返回 `error`，禁用 `panic` 于业务路径
- 日志：统一使用 `log/slog` 结构化输出
- 上下文：所有跨边界调用必须接受 `context.Context`

### TypeScript / React

- 严格模式（`strict: true`）
- 组件优先函数式 + Hooks
- 状态管理：全局状态用 Zustand，异步用 TanStack Query
- UI：Tailwind + shadcn/ui，避免引入其他组件库

### YAML（Recipe / Workflow）

- 使用 2 空格缩进
- `id` 使用 `kebab-case`，Command / Recipe ID 使用 `dot.case`（如 `docker.list`）

---

## 六、Plugin 开发速览

详细规范见 [docs/plugin-system.md](./docs/plugin-system.md)。一句话原则：

> **插件只能注册 Commands / Recipes / Workflows；不得直接操作 UI，不得依赖 AI。**

Plugin 通过 `hashicorp/go-plugin` 作为独立子进程运行，与 Core 通过 gRPC 通信。

---

## 七、许可证

本项目采用 **Apache License 2.0**。所有贡献均默认以同一许可证发布。

---

## 八、行为准则

保持技术讨论，尊重每一位贡献者。恶意行为、人身攻击、歧视性言论一律不接受。
