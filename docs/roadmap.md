# RFC: Roadmap

- 状态：Living
- 版本：v0.2
- 更新日期：2026-07-10
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

## v0.3 — Docker Plugin + Docker Dashboard + Workflow 增强 🎯 RC 就绪，待发布

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

## v0.3.1 — 稳定性补丁 🚧 进行中

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

## v0.4 — AI Plugin 🚧 骨架已合入

**v0.4.0 骨架**（本次交付）：

- ✅ `sdk/ai.go`：新增 [Provider / ChatMessage / ChatRequest / ChatResponse / ToolCall / ToolSpec / ChatStreamSink](../sdk/ai.go) 等抽象；字段命名对齐 OpenAI 兼容 API 惯例，便于真实 provider 直接透传
- ✅ [`plugins/ai`](../plugins/ai/) 新 module：
  - `main.go`：`AIPlugin` + Settings `providers[]` 装配；默认自带 `mock` provider（离线可用）
  - `providers.go`：`mockProvider` 实现 Chat / ChatStream；输出前缀 `[mock] `
  - `commands.go`：三条 Command —— `ai.list_providers` / `ai.chat` / `ai.chat_stream`
  - `commands_test.go`：覆盖率 **76.6%**，含 Init 装配 / 未知 kind / 重复 name / 稳定顺序 / echo 主路径 / 参数校验 / 未知 provider / stream delta+finish 等
- ✅ [docs/ai-plugin.md](./ai-plugin.md)：v0.4 设计文档
- ✅ [go.work](../go.work) 追加 `plugins/ai`；[ci.yml](../.github/workflows/ci.yml) 三个 build/vet/test 循环全部纳入

**v0.4.1 承接**：

- 真实 Provider：OpenAI / Anthropic（含 rate limit / retry / rate-limited 错误码）
- **tool-use 闭环**：`chat_stream` 检测 `ToolCall` → 借 Command Engine 执行 → 结果以 `role=tool` 消息回喂 provider 续写；Dangerous 命令仍走 `Confirmed=true` 二次确认
- Desktop AI Chat 面板（简单版）

**v0.5 承接**：

- MCP Server / Client 双向对接（既能作为 MCP 客户端调外部 MCP Server，也能把 MOW 自己的 Command 暴露成 MCP Server）
- Embedding / vector store（RAG）
- 通知 Provider（Webhook / Email / IM）作为独立 plugin

## v0.5 — 扩展生态

- PVE Plugin
- Kubernetes Plugin
- 数据库 Plugin
- Marketplace（插件市场雏形）
- Workflow 版本化 / 迁移

## MVP 起步指南（附录 A）

1. 建立 Git 仓库，先写 `docs/`，暂不写代码
2. 完成 `Architecture.md` 与 RFC 骨架
3. **MVP 只做一个 Plugin：SSH**，但 **Plugin Framework 必须写完整**
4. 先定义 **Core**（PluginManager / ConnectionManager / CommandEngine / WorkflowEngine / Config / Logger）
5. UI 只调用 Core，不写业务逻辑
6. Plugin SDK 是最重要的地方，先设计 SDK 再实现具体插件
7. **不要急着接 AI**，先让 SSH → Recipe → Workflow 全部跑通，AI 只是最后一步
