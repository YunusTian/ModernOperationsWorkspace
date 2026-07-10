# Changelog

所有对本项目的重要变更都将记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

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

[Unreleased]: https://github.com/mow/mow/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/mow/mow/releases/tag/v0.1.0
