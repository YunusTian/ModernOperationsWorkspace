# RFC: 项目愿景（Vision）

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 一、§ 十三

---

## 1. 一句话定位

> **AI is optional. Automation is essential.**
> AI 是可选能力，自动化才是核心能力。

## 2. 目标用户

- 开发者
- 运维工程师
- 需要在 Windows / Linux / macOS 上跨平台管理服务器的团队

## 3. 支持的协议与目标

- SSH
- Docker（本地 / TCP / TLS）
- PVE API
- Kubernetes（未来）
- 数据库（未来）
- HTTP API
- AI（可插拔 Provider）

## 4. 领域建模原则

用户表达的是**领域意图**，不是 Shell 命令。AI / GUI / CLI 面对同一份领域模型。

| 用户意图 | 错误绑定 | 正确抽象 |
| --- | --- | --- |
| 查看容器 | `docker ps` | Docker/Podman/K8s Plugin |
| 重启服务 | `systemctl restart` | Service Plugin |
| 查看日志 | `journalctl` | Log Plugin |
| 上传文件 | `scp` | File Plugin |

## 5. 长期平台愿景

- **Marketplace**：第三方发布插件
- **Workflow 分享**：形成"运维经验库"
- **Team Workspace**：团队共享服务器 / Recipe / Workflow
- **Provider 抽象**：AI、连接、通知均可替换

## 6. 非目标（Non-Goals）

- 不做通用 IDE
- 不做纯粹的 AI 聊天客户端
- 不绑定任何单一云厂商
- 不依赖任何单一 AI Provider

## 7. 技术栈锁定（v0.1）

一句话总结（详见 [architecture.md](./architecture.md)）：

> **Go 1.22+ Core** · **Wails v2 + React/TS Desktop** · **Cobra CLI** · **hashicorp/go-plugin (gRPC)** · **Monorepo + go.work**

## 8. 待讨论

- [ ] 是否面向企业提供 Team Workspace 的首个版本时间点
- [ ] Marketplace 的商业模式（开源 / 收费）
