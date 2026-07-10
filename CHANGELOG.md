# Changelog

所有对本项目的重要变更都将记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 计划中

- v0.3.1 稳定性补丁：`plugins/docker` 覆盖率补齐（错误路径 / 连接取消 / TLS / registry auth 脱敏 / 并发流关闭）；Workflow JSONL 历史文件锁 / 轮转 / 保留策略 / 损坏行恢复；Windows `npipe://` 真实实现；Docker `exec` TLS raw-hijack 支持；真实 Docker Engine E2E（Linux CI）。

## [v0.3.0] - 2026-07-10

v0.3 主线：**Docker Plugin + Docker Dashboard + Workflow 引擎增强**。完整验收见 [docs/v0.3-acceptance-checklist.md](./docs/v0.3-acceptance-checklist.md)。

### 新增 — Docker Plugin（`plugins/docker`）

- 独立 gRPC 子进程插件，复用 `sdk/pluginserve`，进程隔离、独立崩溃
- 传输协议一次覆盖三种：`unix://` / `tcp://` / `tcp:// + TLS`（`ca` + `cert` + `key` 三件套）
- **v0.3 第一阶段**（lifecycle + 只读）：`docker.list` / `docker.inspect` / `docker.start` / `docker.stop` / `docker.restart` / `docker.logs`（流式；8 字节 mux 头解码，TTY 原始透传）
- **v0.3 第三阶段**（完整生命周期 + 镜像分发 + 交互式 exec）：
  - `docker.rm`（**Dangerous** 权限 · Command Engine 二次确认 + 应用层 `Confirmed=true` 双重护栏）
  - `docker.pull` / `docker.push`（流式 progress · chunked JSON lines · `X-Registry-Auth` 头；`errorDetail` 触发 `sdk.Error` 不调 Finish）
  - `docker.exec`（双向流：`create` → `start` hijack → mux/raw → exit code；TTY 场景原始透传；`SignalWinch` 走 `POST /exec/{id}/resize`）
  - 只读补齐：`docker.images` / `docker.volumes` / `docker.networks`
- 错误码体系：`DOCKER_DIAL_FAILED` / `DOCKER_NOT_FOUND` / `DOCKER_CONFLICT` / `DOCKER_UNAUTHORIZED` / `DOCKER_ENGINE_ERROR` / `DOCKER_REGISTRY_ERROR` / `CONFIRMATION_REQUIRED` 等
- `core/connection.DockerCredentials`（`connection.Type = "docker"`）：host / api_version / TLS 三件套；`Sealer` (AES-256-GCM) 加密到 `Target.EncryptedCredentials`

### 新增 — Docker Dashboard（`apps/desktop`）

- **v0.3 第二阶段**：容器列表（state 徽标 · `all=true` 默认可切换）→ Inspect 抽屉（只读 JSON）→ 流式 logs 面板（`docker:logs:<sid>:{stdout,stderr,exit}` 事件契约）→ 生命周期弹窗（`Confirmed=true` 才下发）
- **v0.3 第三阶段**：Remove 弹窗（force / volumes 可选，`running/restarting` 自动预勾 force；Dangerous 双重护栏）+ Exec 交互式终端（xterm.js + 首帧 winch 同步尺寸）+ Images / Volumes / Networks 只读 Tab
- Wails 后端 API：`DockerList` / `DockerInspect` / `DockerLifecycle` / `DockerLogsOpen`/`Close` / `DockerRm` / `DockerImages` / `DockerVolumes` / `DockerNetworks` / `DockerExecOpen`/`Write`/`Resize`/`Close`
- 前端 Tab 路由：仅在 active target 类型为 `docker` 时可点

### 新增 — Workflow 引擎增强（分五批合入）

1. **`when: <expr>` 条件分支**（[workflow.md §7.4.1](./docs/workflow.md#741-when-条件分支v03-第一批)）：表达式复用 expr-lang，无需 `${}` 包裹；`false` → `Skipped`（OK=true，不写 out）；求值失败 → `WHEN_EVAL` 中断
2. **`retry: { max, backoff, max_backoff, exponential }`**（[§7.4.2](./docs/workflow.md#742-retry-单-step-重试v03-第二批)）：`max ∈ [0, 20]`；`WHEN_EVAL` / `INTERPOLATE` 不参与重试；`StepResult.Attempts` + `PhaseRetry` 事件观测
3. **执行历史持久化 · JSONL**（[§7.4.3](./docs/workflow.md#743-执行历史持久化v03-第三批)）：`<data_dir>/workflow-runs.jsonl` append-only；`RunID = run-<16 hex>`；`Store` 抽象保留 SQLite 切换空间；写盘错误不冒泡到 `Runner.Run`；CLI `mow workflow history list|show` + Desktop History 面板 & 详情抽屉
4. **`on_failure` / `rollback` 声明式回滚**（[§7.4.4](./docs/workflow.md#744-on_failure--rollback-声明式回滚v03-第四批)）：逆序遍历，只对**成功过**的 step 调用 `compensate`；补偿失败不嵌套回滚 / 不 retry；`Result.Rollback` 全量快照 + `PhaseRollback` 事件
5. **`parallel: true` 组内并行**（[§7.4.5](./docs/workflow.md#745-parallel-true-组内并行v03-第五批)）：连续 `parallel: true` 归为同一组；fail-fast 取消同组兄弟；组内**禁止**引用兄弟 `steps.<sibling>.out.*`（`LoadBytes` / `Validate` 静态强制）；`OnStep` 回调 mutex 序列化；`Result.Steps` 按声明顺序追加

### 新增 — CLI / Desktop

- `apps/cli`：`mow workflow history list [--limit N] [--workflow ID] [--json]` / `mow workflow history show <run-id> [--json]`
- `apps/cli`：Workflow 进度打印新增 `⤼ skipped` / `↻ retry 1/3 after 500ms` / `↩ rollback deploy 100ms` 三类图标
- `apps/desktop`：`WorkflowPage` 底部新增可折叠的 History 面板，Run 结束自动刷新；点击历史行 → 抽屉展示 inputs / steps / audit id / error code / rollback 记录

### 变更

- `core/command`：根据 `CommandSpec.InputSchema` 执行 JSON Schema 输入校验，为无效 Schema 与不匹配参数提供稳定错误码
- CI（`.github/workflows/ci.yml`）：三个 module（core / apps / plugins）逐个 build / vet / test；追加 `plugins/docker` build & test；Desktop 前端强制 `npm ci && npm run build`；Linux + Windows 双矩阵；新增 **`docker-e2e` job**（`workflow_dispatch → only=docker-e2e/all` 手动触发；Linux 真实 daemon；预编译 plugin 传给测试；`label=mow-e2e=1` 兜底清理）
- Release（`.github/workflows/release.yml`）：新增 **Docker Plugin 跨 5 平台构建**；每个 `.tar.gz` 生成 `.sha256`，release job 汇总生成 `SHA256SUMS`；`body_path` 按 tag 动态解析（`v0.3.x → docs/v0.3-acceptance-checklist.md`），不再固定指向 v0.1
- E2E（`tests/e2e`）：新增 [docker_helpers_test.go](./tests/e2e/docker_helpers_test.go) 与 [docker_e2e_test.go](./tests/e2e/docker_e2e_test.go)，覆盖 `docker.list` / lifecycle / `docker.logs` / `docker.pull` / `docker.exec` / `docker.rm` 六个真实 daemon 场景；默认 `t.Skip`，需 `MOW_DOCKER_E2E=1` + 可达 daemon 才实跑
- 统一所有 Go module、`go.work`、CI 与文档最低 Go 版本至 **1.25**
- README / Roadmap / docs 状态：v0.3 从"下一版"调整为"RC（发布前修正中）"；`docs/workflow.md` 状态升级为 **Implemented (v0.3)**；`docs/docker-plugin.md` 状态升级为 **Implemented**；新增 [`docs/v0.3-acceptance-checklist.md`](./docs/v0.3-acceptance-checklist.md)

### 测试

- `core/workflow` 覆盖率 **92.5%**（v0.2：91.2%）
- `core/workflow/history` 覆盖率 **81.2%**
- `core/command` 覆盖率 **71.0%**（新增 InputSchema 校验路径）
- `plugins/docker` 覆盖率 **59.6%**（临时门槛；v0.3.1 目标 ≥ 70%）
- `plugins/ssh` 单测 PASS
- `tests/e2e` 全量 PASS（含 `TestWorkflow_DeployStaticSite_EndToEnd`）
- `go vet ./...` 全部 module 无告警
- 前端 `tsc --noEmit` 通过
- `go test -race`：本机跳过（无 gcc），CI 双平台矩阵兜底

### 已知边界（发布前保留 · v0.3.1 补齐）

- **Windows `npipe://`**：三重护栏
  - `TargetsPage` 保存前拦截 `npipe://` scheme + 输入框实时提示
  - 桌面后端 `DockerExecOpen` 二次校验
  - 插件层 `newEngineClient` / `docker.exec` 返回稳定错误码 `DOCKER_NPIPE_UNSUPPORTED` / `DOCKER_EXEC_NPIPE_UNSUPPORTED`
- **TLS + `docker.exec` 的 raw-hijack**：双重护栏
  - 桌面新增 `App.DescribeDockerTarget` 返回 `exec_supported` + `exec_unsupported_reason`；`DockerExecDrawer` 挂载即调用，`exec_supported=false` 时禁用 Start
  - 插件层 `docker.exec` 检测 `Scheme=tcp && (TLSVerify||TLSCA!="")` 立即返回 `DOCKER_EXEC_TLS_UNSUPPORTED`
- **Release CI 产物缺口**：已修复 —— [release.yml](./.github/workflows/release.yml) 追加 `plugins/docker` 全 5 平台构建、`.sha256` + `SHA256SUMS` 校验文件、`body_path` 按 tag 动态解析（`v0.3.x → docs/v0.3-acceptance-checklist.md`）

### 依赖

- `plugins/docker`：仅标准库 `net/http`（不引入官方 `github.com/docker/docker` SDK，避免上百 MiB 依赖）
- `core`：延续 v0.2 的 `gopkg.in/yaml.v3` + `github.com/expr-lang/expr`

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

[Unreleased]: https://github.com/mow/mow/compare/v0.3.0...HEAD
[v0.3.0]: https://github.com/mow/mow/releases/tag/v0.3.0
[v0.2.0]: https://github.com/mow/mow/releases/tag/v0.2.0
[v0.1.0]: https://github.com/mow/mow/releases/tag/v0.1.0
