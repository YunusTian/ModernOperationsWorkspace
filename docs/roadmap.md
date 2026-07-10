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

## v0.3 — Docker Plugin + Docker Dashboard 🎯 下一版

- **Docker Plugin**（作为独立进程 gRPC 插件）
  - 🔨 **第一阶段（MVP，已合入）**：`docker.list` / `docker.inspect` / `docker.start` / `docker.stop` / `docker.restart` / `docker.logs`（流式）— 详见 [docker-plugin.md](./docker-plugin.md)
  - 🔨 **第三阶段（已合入）**：`docker.rm`（Dangerous · 双重护栏）/ `docker.pull` / `docker.push`（流式 progress · X-Registry-Auth）/ `docker.exec`（双向流 · TTY / mux / resize / exit_code）— 详见 [docker-plugin.md §12](./docker-plugin.md#12-v03-第三阶段dockerrm--dockerpull--dockerpush--dockerexec)
- **Docker Dashboard**（Desktop 新增 Tab）
  - 🔨 **第二阶段（已合入）**：容器列表（含 state 徽标） → inspect 抽屉 → 流式 logs 面板 → start / stop / restart 二次确认弹窗 — 详见 [docker-plugin.md §11](./docker-plugin.md#11-docker-dashboardv03-第二阶段)
  - ⏳ 第三阶段：镜像 / 卷 / 网络 / Compose 视图；`docker.rm` 前置弹窗；容器 exec 交互式终端
- **Workflow 引擎增强**（分批推进 · v0.3 全部合入 ✅）
  - 🔨 **第一批（已合入）**：`when: <expr>` 条件分支 — 详见 [workflow.md §7.4.1](./workflow.md#741-when-条件分支v03-第一批)
  - 🔨 **第二批（已合入）**：`retry: { max, backoff, max_backoff, exponential }` 单 step 重试 — 详见 [workflow.md §7.4.2](./workflow.md#742-retry-单-step-重试v03-第二批)
  - 🔨 **第三批（已合入）**：执行历史持久化（JSONL 默认后端，`Store` 抽象保留 SQLite 切换空间）— 详见 [workflow.md §7.4.3](./workflow.md#743-执行历史持久化v03-第三批)
  - 🔨 **第四批（已合入）**：`on_failure` / `rollback` 声明式补偿 — 详见 [workflow.md §7.4.4](./workflow.md#744-on_failure--rollback-声明式回滚v03-第四批)
  - 🔨 **第五批（已合入）**：`parallel: true` 组内并行（fail-fast、事件序列化、组内禁止 out 互引）— 详见 [workflow.md §7.4.5](./workflow.md#745-parallel-true-组内并行v03-第五批)
  - v0.4+：单 step 级 `target` 覆盖 / `notify:` 通知 / Workflow 版本化 / `parallel_limit` / 嵌套并行组

## v0.4 — AI Plugin

- AI Plugin（作为独立 Plugin）
- Provider 抽象（ChatGPT / Claude / Gemini / Qwen / DeepSeek / Local）
- MCP 支持
- AI 只能调用已有 Recipe / Workflow / Command
- 通知 Provider（Webhook / Email / IM）

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
