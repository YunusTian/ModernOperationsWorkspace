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
  - ⏳ 第二阶段：`docker.pull` / `docker.push` / `docker.rm`（Dangerous） / `docker.exec`（流式）
- **Docker Dashboard**（Desktop 新增 Tab，v0.3 第二阶段）
  - 容器列表 + 状态徽标（running / exited / paused）
  - 端口 / 挂载 / 环境变量只读视图
  - 一键 logs / restart / rm（Dangerous 走二次确认）
- **Workflow 引擎增强**（与 Docker Plugin 联动）
  - `on_failure` / `rollback` 声明式回滚
  - `retry: { max, backoff }` 重试策略
  - 单 step 级 `target` 覆盖
  - `parallel: true` 组内并行
  - `when: <expr>` 条件分支
- **Workflow 执行历史持久化**（SQLite，与审计日志共享）

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
