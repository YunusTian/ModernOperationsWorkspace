# RFC: Roadmap

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 十一

---

## v0.1 — 优秀的 SSH 客户端

- SSH 连接
- Terminal
- SFTP（上传 / 下载）
- 保存连接、密钥管理
- Plugin Framework（雏形）
- **不接入 AI**

## v0.2 — Command / Recipe / Workflow

- Command Engine
- Recipe Engine（`system.cpu` / `system.disk` / `docker.status` 等）
- Workflow Engine（部署 .NET / Node / Docker、备份数据库）

## v0.3 — Docker Plugin

- Docker Plugin（list / pull / stop / logs / rm）
- Docker Dashboard（GUI）

## v0.4 — AI Plugin

- AI Plugin（作为独立 Plugin）
- Provider 抽象（ChatGPT / Claude / Gemini / Qwen / DeepSeek / Local）
- MCP 支持
- AI 只能调用已有 Recipe / Workflow / Command

## v0.5 — 扩展生态

- PVE Plugin
- Kubernetes Plugin
- 数据库 Plugin
- Marketplace（插件市场雏形）

## MVP 起步指南（附录 A）

1. 建立 Git 仓库，先写 `docs/`，暂不写代码
2. 完成 `Architecture.md` 与 RFC 骨架
3. **MVP 只做一个 Plugin：SSH**，但 **Plugin Framework 必须写完整**
4. 先定义 **Core**（PluginManager / ConnectionManager / CommandEngine / WorkflowEngine / Config / Logger）
5. UI 只调用 Core，不写业务逻辑
6. Plugin SDK 是最重要的地方，先设计 SDK 再实现具体插件
7. **不要急着接 AI**，先让 SSH → Recipe → Workflow 全部跑通，AI 只是最后一步
