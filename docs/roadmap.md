# RFC: Roadmap

- 状态：Living
- 版本：v0.5-planning
- 更新日期：2026-07-11
- 相关章节：Architecture.md § 十一

---

## v0.1 — 优秀的 SSH 客户端 ✅

- SSH 连接
- Terminal
- SFTP（上传 / 下载）
- 保存连接、密钥管理
- Plugin Framework（雏形）
- **不接入 AI**

## v0.2 — Command / Recipe / Workflow ✅ 已发布

- Command Engine
- Recipe Engine（内置 `system.cpu` / `system.disk`）
- **Workflow Engine（YAML DSL + `${var}` 插值 + Runner）**
  - CLI：`mow workflow validate|run`
  - Desktop：`WorkflowPage`（拖拽 / 表单 / 实时日志流）
  - E2E：`deploy-static-site.yaml` 通过 fake SSH server 走通
- 边界：仅顺序执行 + 变量传递；`parallel` / `when` / `on_failure` / `retry` / `notify` / `rollback` 均**未实现**，见 [docs/workflow.md §7.5](./workflow.md#75-尚未实现-v03)。

## v0.3 — Docker Plugin + Docker Dashboard + Workflow 增强 ✅ 已发布

- **Docker Plugin**（作为独立进程 gRPC 插件）
  - 🔨 **第一阶段（MVP，已合入）**：`docker.list` / `docker.inspect` / `docker.start` / `docker.stop` / `docker.restart` / `docker.logs`（流式）— 详见 [docker-plugin.md](./docker-plugin.md)
  - 🔨 **第三阶段（已合入）**：`docker.rm`（Dangerous · 双重护栏）/ `docker.pull` / `docker.push`（流式 progress · X-Registry-Auth）/ `docker.exec`（双向流 · TTY / mux / resize / exit_code）— 详见 [docker-plugin.md §12](./docker-plugin.md#12-v03-第三阶段dockerrm--dockerpull--dockerpush--dockerexec)
- **Docker Dashboard**（Desktop 新增 Tab）
  - 🔨 **第二阶段（已合入）**：容器列表（含 state 徽标） → inspect 抽屉 → 流式 logs 面板 → start / stop / restart 二次确认弹窗 — 详见 [docker-plugin.md §11](./docker-plugin.md#11-docker-dashboardv03-第二阶段)
  - 🔨 **第三阶段（已合入）**：`docker.rm` 前置弹窗（force / volumes 可选，Dangerous 双重护栏）；容器 exec 交互式终端（xterm.js + TTY winch）；Images / Volumes / Networks 只读 Tab — 详见 [docker-plugin.md §12.6](./docker-plugin.md#126-dashboard-侧的补齐v03-第三阶段-ui)
- **Workflow 引擎增强**（分批推进 · v0.3 全部合入 ✅）
  - 🔨 **第一批（已合入）**：`when: <expr>` 条件分支 — 详见 [workflow.md §7.4.1](./workflow.md#741-when-条件分支v03-第一批)
  - 🔨 **第二批（已合入）**：`retry: { max, backoff, max_backoff, exponential }` 单 step 重试 — 详见 [workflow.md §7.4.2](./workflow.md#742-retry-单-step-重试v03-第二批)
  - 🔨 **第三批（已合入）**：执行历史持久化（JSONL 默认后端，`Store` 抽象保留 SQLite 切换空间）— 详见 [workflow.md §7.4.3](./workflow.md#743-执行历史持久化v03-第三批)
  - 🔨 **第四批（已合入）**：`on_failure` / `rollback` 声明式补偿 — 详见 [workflow.md §7.4.4](./workflow.md#744-on_failure--rollback-声明式回滚v03-第四批)
  - 🔨 **第五批（已合入）**：`parallel: true` 组内并行（fail-fast、事件序列化、组内禁止 out 互引）— 详见 [workflow.md §7.4.5](./workflow.md#745-parallel-true-组内并行v03-第五批)
  - v0.4+：单 step 级 `target` 覆盖 / `notify:` 通知 / Workflow 版本化 / `parallel_limit` / 嵌套并行组

- **发布前修正**（gating v0.3.0 tag，全部已完成 —— 详见 [v0.3 验收清单 §6](./v0.3-acceptance-checklist.md#6-发布前必修补丁gating-v030-tag)）
  - ✅ Release CI 产物缺口：追加 `plugins/docker` 全平台产物、`body_path` 按 tag 动态解析、追加 `.sha256` + `SHA256SUMS`
  - ✅ 跨平台承诺：Windows `npipe://` 与 TLS `docker.exec` 采用"应用层禁用 + 稳定错误码 + UI 提前拦截"三重护栏；真实实现推迟到 v0.3.1

## v0.3.1 — 稳定性补丁 ✅ 已发布

已完成：

- ✅ `plugins/docker` 覆盖率：59.6% → **76.0%**（v0.3.1 目标 ≥70%；新增 [coverage_test.go](../plugins/docker/coverage_test.go) + [hijack_tls_test.go](../plugins/docker/hijack_tls_test.go) + [npipe_test.go](../plugins/docker/npipe_test.go)）
- ✅ Workflow JSONL 历史：
  - 新增 [RotateOptions](../core/workflow/history/jsonl.go)（`MaxBytes` + `MaxKeep`）+ `NewJSONLStoreWithRotate`
  - `readAllWithRotated` 跨主文件 + `.1..N` 轮转文件全读取
  - 抗回归测试：`RotateAndReadAcrossFiles` / `RotateMaxKeepPrunesOldest` / `ConcurrentSaveNoInterleave` / `CorruptLineMixedWithRotatedFile` / `ReadEmptyLinesTolerated` / `RotateNoOpWhenDisabled` / `NegativeMaxKeepClamped`
- ✅ **Windows `npipe://` 真实实现**：引入 `github.com/Microsoft/go-winio v0.6.2`，拆分平台文件 [npipe_windows.go](../plugins/docker/npipe_windows.go) + [npipe_other.go](../plugins/docker/npipe_other.go)；`newEngineClient` 与 `docker.exec` 均已支持；桌面 `App.DescribeDockerTarget` 依 `runtime.GOOS` 决定 `exec_supported`；`TargetsPage` 输入框转为软提示（不再强拒），保持行为一致的双重护栏
- ✅ **TLS `docker.exec` raw-hijack**：`engineClient.tlsCfg` 保存 `tls.Config`；`dialHijack` 拨号后在 raw conn 之上做 `tls.Client(conn, cfg).HandshakeContext`，SNI/证书校验用 `buildTLSConfig` 里的 `ServerName`；`exec.go` 移除 TLS pre-guard；桌面 `DescribeDockerTarget` 移除 TLS 禁用分支；新增 [hijack_tls_test.go](../plugins/docker/hijack_tls_test.go) 覆盖成功 + 握手失败两条路径
- ✅ **Docker E2E 接入常规 pipeline**：[ci.yml](../.github/workflows/ci.yml) 的 `docker-e2e` job 从 `workflow_dispatch` 提升为 `push:main + PR + workflow_dispatch` 三源触发；PR 场景通过 `dorny/paths-filter@v3` 只在触及 `plugins/docker/**` / `tests/e2e/docker*_test.go` / `apps/desktop/docker_*.go` / `ci.yml` 时才跑；独立 `docker-e2e-${github.ref}` concurrency group 防止同 ref 抢 daemon
- ✅ **JSONL 跨进程文件锁**：新增 [flock_unix.go](../core/workflow/history/flock_unix.go)（`unix.Flock LOCK_EX`）+ [flock_windows.go](../core/workflow/history/flock_windows.go)（`windows.LockFileEx`）；`JSONLStore.Save` 打开 fd 后先尝试独占锁，写完解锁；文件锁失败静默降级到单进程 mutex 保护。抗回归：[flock_test.go](../core/workflow/history/flock_test.go) 通过 `os.Executable` 自我 `exec` 出 4 个 worker 进程各写 50 条，主进程验证共 200 行、每行独立解析成功
- ✅ 真实 Docker Engine E2E（v0.3 已合入 [tests/e2e/docker_e2e_test.go](../tests/e2e/docker_e2e_test.go)）覆盖 list / lifecycle / logs / pull / exec / rm

**v0.3.1 全部完成 —— 可以打 tag 发布。**

## v0.4 — AI 可用闭环 ✅ 已发布（v0.4.0）

正式发布标准已按 [v0.4 验收清单](./v0.4-acceptance-checklist.md) 全部完成：OpenAI-compatible Provider、宿主侧只读 tool-use、决策链审计、递归脱敏、CLI 与 Desktop 入口，以及 Windows/Linux E2E。

**v0.4.0 骨架**（本次交付）：

- ✅ `sdk/ai.go`：新增 [Provider / ChatMessage / ChatRequest / ChatResponse / ToolCall / ToolSpec / ChatStreamSink](../sdk/ai.go) 等抽象；字段命名对齐 OpenAI 兼容 API 惯例，便于真实 provider 直接透传
- ✅ [`plugins/ai`](../plugins/ai/) 新 module：
  - `main.go`：`AIPlugin` + Settings `providers[]` 装配；默认自带 `mock` provider（离线可用）
  - `providers.go`：`mockProvider` 实现 Chat / ChatStream；输出前缀 `[mock] `
  - `commands.go`：三条 Command —— `ai.list_providers` / `ai.chat` / `ai.chat_stream`
  - `commands_test.go`：覆盖率 **76.6%**，含 Init 装配 / 未知 kind / 重复 name / 稳定顺序 / echo 主路径 / 参数校验 / 未知 provider / stream delta+finish 等
- ✅ [docs/ai-plugin.md](./ai-plugin.md)：v0.4 设计文档
- ✅ [go.work](../go.work) 追加 `plugins/ai`；[ci.yml](../.github/workflows/ci.yml) 三个 build/vet/test 循环全部纳入

**v0.4.0 正式交付**：

- ✅ OpenAI-compatible Provider：一次性/流式 Chat、tool calls、错误映射和有上限退避重试
- ✅ **宿主侧 tool-use 闭环**：CommandSpec 派生工具、只读 allowlist、五道资源护栏、全链路审计；架构见 [ADR-0001](./adr/0001-host-side-ai-tool-orchestration.md)
- ✅ CLI：`ai providers` / `ai ask` / `ai chat`
- ✅ Desktop：AI Workspace、Ask、usage、Retry、配置状态提示
- ✅ 安全：递归脱敏、Dangerous/Streaming/Recursive AI 拒绝、fake-provider E2E

MCP、知识接入和更强的 AI Plan 能力调整到 v0.9；v0.5 优先完成插件平台化。

## v0.4.1 — GA 工程化收尾

实现完成，待 CI Release Smoke 与 tag；详见 [v0.4.1 验收清单](./v0.4.1-acceptance-checklist.md)。

- ✅ 唯一版本源与版本一致性
- ✅ SDK 契约测试
- ✅ Release 二进制三平台 Smoke Test
- ✅ v0.3/v0.4 配置迁移验证
- ✅ 文档和发布状态清理

## v0.5 — 插件平台化

v0.5 拆分为三个独立可 tag 的子版本，每个子版本都有独立的发布门槛，可独立回退。详见 [开发计划 §4](./development-plan-v0.5-v1.0.md#4-v05--插件平台化)。

### v0.5.0 — Plugin Manifest 与包格式（地基）✅ 已发布

- `plugin.json` schema 与 `sdk/manifest/plugin.schema.json`
- `mow plugin validate <package>` 命令，稳定错误码
- 运行时对 Manifest / Metadata 一致性和兼容范围的强校验（`PLUGIN_MANIFEST_MISMATCH` / `PLUGIN_INCOMPATIBLE`）
- 官方 SSH / Docker / AI 三个插件补齐 `plugin.json`
- 不做下载 / 安装 / Catalog / 配置 UI

### v0.5.1 — 插件生命周期（install / upgrade / uninstall + 本地 Catalog 雏形）

P1 已完成本地包生命周期地基：`list / install / enable / disable / doctor`、同盘临时目录校验后原子激活、独立启停状态和运行时禁用门禁。`search / update / uninstall`、Catalog、回退及 Desktop 管理页仍待后续批次。

- CLI 八条：`mow plugin list|search|install|update|enable|disable|uninstall|doctor`
- 本地 Catalog（静态 JSON + GitHub Release 产物）+ 官方 / 私有 URL
- SHA-256 校验、可信来源默认策略
- 升级失败自动回退旧二进制 + 旧 Manifest；卸载留存数据，`--purge` 才真删
- Desktop 插件管理页保持最小可用（列表 / 启用停用 / 卸载 / 诊断）

### v0.5.2 — Schema 驱动的配置 UI + PVE 参考实现（闭环验证）

- Manifest `settingsSchema` 驱动 CLI 交互式配置与 Desktop 表单
- Secret 字段隔离存储、脱敏输入、日志与配置文件均无明文
- PVE 只读参考插件：Cluster / Node / VM / LXC 列表 + start/stop/reboot
- 插件兼容矩阵进入 CI（SSH / Docker / AI / PVE 四款）
- 复杂 PVE 创建向导、存储迁移、Dangerous 删除延后到 v0.7

## v0.6 — Workflow 2.0

- Workflow 版本化、子工作流、审批、Dry-run
- 定时/Webhook 触发与通知插件
- `parallel_limit`、step target、暂停恢复
- SQLite 结构化历史与搜索统计

## v0.7 — 基础设施扩展

- PVE 正式插件
- Kubernetes MVP
- PostgreSQL / MySQL 只读诊断探索

## v0.8 — 可观测与诊断中心

- Command / Workflow / AI 统一审计查询
- Target 与插件健康状态
- 诊断包、Trace、指标和错误码聚合

## v0.9 — AI Operations 2.0

- Plan / Explain / Dry-run / 分步确认
- MCP Client / Server
- Ollama / 本地模型
- 面向运维手册、历史故障和 Workflow 的受控知识接入

## v1.0 — 稳定承诺

- SDK / Plugin Protocol / Manifest / Workflow DSL 稳定
- 安装、升级、回退、迁移和卸载
- 仓库外插件验证
- 跨平台长期运行与 RC 稳定期

v0.5～v1.0 的详细范围、非目标、依赖关系和发布门槛见 [开发计划](./development-plan-v0.5-v1.0.md)。

## MVP 起步指南（附录 A）

1. 建立 Git 仓库，先写 `docs/`，暂不写代码
2. 完成 `Architecture.md` 与 RFC 骨架
3. **MVP 只做一个 Plugin：SSH**，但 **Plugin Framework 必须写完整**
4. 先定义 **Core**（PluginManager / ConnectionManager / CommandEngine / WorkflowEngine / Config / Logger）
5. UI 只调用 Core，不写业务逻辑
6. Plugin SDK 是最重要的地方，先设计 SDK 再实现具体插件
7. **不要急着接 AI**，先让 SSH → Recipe → Workflow 全部跑通，AI 只是最后一步
