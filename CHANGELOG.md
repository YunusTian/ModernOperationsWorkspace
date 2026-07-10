# Changelog

所有对本项目的重要变更都将记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Changed

- 为 v0.2.1 统一所有 Go module、`go.work`、CI 与项目文档的最低 Go 版本为 **1.25**。
- CI 现在会执行 Desktop 前端的 `npm ci && npm run build`，并对 CLI、Desktop、SDK 运行 `go vet` 与 `go test`。
- Command Engine 根据 `CommandSpec.InputSchema` 执行 JSON Schema 输入校验，并为无效 Schema 与不匹配参数提供稳定错误码。

## [v0.2.0] - 2026-07-10

### 新增

- **Workflow Engine（MVP）**：`core/workflow` 全新模块
  - 声明式数据模型：`Workflow` / `Step` / `Input` / `InputType` + `Validate()`
  - YAML DSL Loader：`LoadFile` / `LoadBytes` / `LoadReader`，严格模式（`yaml.Decoder.KnownFields(true)`）——未知字段一律报错
  - 变量插值：`${inputs.<name>}` / `${steps.<id>.out.<field>}`，基于 [`github.com/expr-lang/expr`](https://github.com/expr-lang/expr)，支持算术 / 三元 / 混合字符串；未定义变量报 `InterpolationError` 携带偏移量
  - Runner 主循环：顺序执行 + 任一失败中止 + Data 反序列化挂到 `scope.Steps[id].Out` 供后续步骤消费
  - OnStep 三阶段回调（`start` / `finish` / `error`）
  - 依赖倒置的 `CommandExecutor` / `RecipeExecutor` 接口，方便 CLI / Desktop 各自注入 Adapter
- **CLI**：新增 `mow workflow validate|run`
  - 彩色进度打印：`▶ upload (cmd:ssh.upload) ... ✓ 42ms`
  - `--input k=v` / `--inputs-json` / `--target` / `--json` / `--no-color`
  - Workflow 依赖插件自动 Enable（含 Recipe 内 step 的 Plugin）
- **Desktop**：新增 Workflow 页面
  - 拖拽或选择 `.yaml` → 后端 `WorkflowValidate` → 依据 `inputs` 生成表单
  - Run 后通过 wails 事件 `workflow:<sess>:step` / `:done` 实时推送日志
  - `int` / `bool` 输入前端强转
- **示例与文档**：
  - [`examples/workflows/deploy-static-site.yaml`](./examples/workflows/deploy-static-site.yaml) + README
  - [`docs/workflow.md`](./docs/workflow.md) 更新为 **Implemented (MVP)** 状态，新增 §7 MVP 已实现字段表 + §8 最小示例
  - [`docs/roadmap.md`](./docs/roadmap.md)：v0.2 打勾 ✅，v0.3 目标明确为 **Docker Plugin + Docker Dashboard + Workflow 增强**
  - [`docs/v0.2-acceptance-checklist.md`](./docs/v0.2-acceptance-checklist.md)：完整验收清单
- **E2E**：`tests/e2e/workflow_e2e_test.go` 复用 fake SSH server 跑通 `deploy-static-site.yaml` 的最小子集，断言插值命中 fake server 收到的命令行

### 测试

- `core/workflow` 单测：**48 项 PASS**，覆盖率 **91.2%**
- `core` 全量：7/7 包 PASS
- `tests/e2e` 全量：PASS（含 `TestWorkflow_DeployStaticSite_EndToEnd`）
- `plugins/ssh` 单测：PASS
- `go build ./...`：`apps/cli` / `apps/desktop` 均通过
- `go vet ./...`：三个 module 无告警
- 前端 `tsc --noEmit`：通过

### 依赖

- `core` 新增：`gopkg.in/yaml.v3 v3.0.1`、`github.com/expr-lang/expr v1.17.8`

### 边界（v0.2 未实现，见 [docs/workflow.md §7.5](./docs/workflow.md#75-尚未实现-v03)）

- `parallel: true` / `when: <expr>` / `on_failure` / `retry` / `notify` / `rollback`
- 单 step 级 `target` 覆盖
- 执行历史 SQLite 持久化
- 上述全部规划在 **v0.3+**

## [v0.1.0] - 2026-07-10

### 新增

- **Command Engine**：统一执行链路核心，支持参数校验、权限控制、审计日志、危险操作二次确认
- **Plugin Framework**：基于 hashicorp/go-plugin 的 gRPC 子进程插件系统，SDK 提供完整 Go 抽象与 protobuf 定义
- **SSH Plugin**：官方 SSH 插件，支持 exec / shell / SFTP / ping / 连接池，Ed25519 私钥鉴权与 passphrase 解密
- **CLI (Cobra)**：`mow target`（目标管理）、`mow ssh`（交互式 Shell + SIGWINCH）、`mow run`（单次执行）、`mow recipe`（Recipe 运行）
- **Desktop (Wails v2)**：React + TypeScript + xterm.js，Terminal / SFTP / Targets 三个页面
- **Recipe System**：内置 Recipe 注册与 Runner，支持步骤顺序执行与失败中止
- **Connection Manager**：支持 Upsert / Get / Open / Delete / Persist，Keystore 加密存储凭证
- **结构化日志**：基于 `log/slog`，支持多级别与 JSON 输出
- **跨平台支持**：Windows / Linux，CI 双平台通过（golangci-lint / go test / race / gosec）
- **E2E 测试体系**：Fake SSH Server + Test Rig，覆盖 exec / recipe / SFTP / Shell / 连接池等 23 个用例
- **16 篇设计文档**：架构总纲、各模块 RFC、验收清单、Roadmap 等

[Unreleased]: https://github.com/mow/mow/compare/v0.2.0...HEAD
[v0.2.0]: https://github.com/mow/mow/releases/tag/v0.2.0
[v0.1.0]: https://github.com/mow/mow/releases/tag/v0.1.0
