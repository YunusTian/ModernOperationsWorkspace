# Modern Operations Workspace（MOW）开发文档

> 一款 AI Native，但**不依赖 AI** 的现代化跨平台运维工作台。
>
> **Core First · AI Optional · Plugin Everything**

- 版本：v0.1 Draft
- 更新日期：2026-07-09
- 文档性质：架构 / 开发规范 / 路线图（RFC 起点）

---

## 目录

1. [项目愿景](#一项目愿景vision)
2. [设计哲学](#二设计哲学)
3. [总体架构](#三总体架构)
4. [核心模块](#四核心模块)
5. [Plugin SDK](#五plugin-sdk)
6. [AI 架构](#六ai-架构)
7. [目录结构](#七目录结构)
8. [插件开发规范](#八插件开发规范)
9. [权限模型](#九权限模型)
10. [日志与可观测](#十日志与可观测)
11. [Roadmap](#十一roadmap)
12. [设计原则（必须遵守）](#十二设计原则必须遵守)
13. [平台愿景（长期）](#十三平台愿景长期)

---

## 一、项目愿景（Vision）

打造一款真正适合开发者与运维工程师的**跨平台运维工作台**。

- **AI 只是一种交互方式，不是产品本身。**
- 无论 AI 是否可用，本软件都应具备完整的运维能力。
- 如果哪天完全不接入 AI，它依然是一款优秀的 SSH / Docker / PVE 运维工具；接入 AI 后，只是让操作更智能、更高效。

产品定位一句话（写在 README 首页）：

> **AI is optional. Automation is essential.**
> AI 是可选能力，自动化才是核心能力。

### 平台目标

- **操作系统**：Windows / Linux / macOS
- **接入协议 / 目标**：SSH · Docker · PVE · Kubernetes（未来）· 数据库（未来）· HTTP API · AI

### 领域建模（Domain-Driven，不是协议驱动）

用户表达的是**领域意图**，而不是 Shell 命令：

| 用户意图 | 错误绑定 | 正确抽象 |
| --- | --- | --- |
| 查看容器 | `docker ps` | Docker/Podman/K8s Plugin 都可实现 |
| 重启服务 | `systemctl restart` | Service Plugin 抽象 |
| 查看日志 | `journalctl` | Log Plugin 抽象 |
| 上传文件 | `scp` | File Plugin 抽象 |

**AI、GUI、CLI 看到的都是同一个领域模型，而不是一堆 Shell。**

---

## 二、设计哲学

### 1. AI 是增强，不是核心

所有 AI 能完成的操作，都必须能够通过传统方式（GUI / CLI / Recipe / Workflow）完成。

### 2. 执行链路唯一

无论谁触发操作，最终都走同一条执行链：

```
User / AI / CLI / GUI / API
              │
              ▼
        Command Engine
              │
              ▼
       Workflow / Recipe
              │
              ▼
           Plugin
              │
              ▼
        Connection
              │
              ▼
           Target
```

### 3. AI 永远不直接生成 Shell 去操作服务器

AI 只能：
- **理解用户意图**
- **选择或组合已经存在的 Recipe / Workflow / Command**
- **调用它们并解释结果**

这样做的好处：
- Token 更少（不需要让 LLM 逐条推理 shell）
- 更安全（Recipe 已被测试过，参数经过验证）
- 更快（毫秒级执行，无需 LLM 推理）
- 更可移植（换 ChatGPT / Claude / Gemini / Qwen / DeepSeek / 本地模型都不需要改底层）

### 4. 架构约束（最重要的一条）

> **Core 永远不依赖 AI，AI 永远依赖 Core。**

只要守住这一条，未来接入任何模型、增加任何插件，都不会推翻已有设计。

---

## 三、总体架构

```
                       UI Layer
                          │
       ┌──────────────────┼───────────────────┐
       │                  │                   │
    Terminal          Dashboard            AI Chat
       │                  │                   │
       └──────────────────┼───────────────────┘
                          │
                    Command Engine
                          │
                    Workflow Engine
                          │
                     Recipe Engine
                          │
                    Plugin Manager
                          │
        ┌──────────┬──────────┬──────────┐
        │          │          │          │
   SSH Plugin  Docker Plugin  PVE Plugin  AI Plugin
        │          │          │          │
        └──────────┴──────────┴──────────┘
                          │
                  Connection Manager
                          │
              SSH · HTTP · Docker Socket · WS ...
```

### 分层职责

| 层级 | 职责 |
| --- | --- |
| **UI Layer** | Terminal / Dashboard / AI Chat 等交互形式，不放业务逻辑 |
| **Command Engine** | 统一执行入口，找到并调用对应 Plugin 的 Command |
| **Workflow Engine** | 多步 Recipe 编排、失败回滚、重试、通知 |
| **Recipe Engine** | 预定义、经过验证的操作单元（可能包含多条 Command） |
| **Plugin Manager** | 插件生命周期、注册、版本、权限 |
| **Connection Manager** | 所有连接的建立、复用、密钥、断线重连 |

**重点原则**：GUI / CLI / AI / API 全部**只通过 Command Engine 与内核交互**，不允许绕过。

---

## 四、核心模块

### 4.1 Connection Manager

负责：所有连接管理。

**支持连接类型**：
- SSH
- Docker（本地 / TCP / TLS）
- PVE API
- HTTP
- WebSocket

**职责**：
- 建立连接 / 保持连接 / 自动重连
- 会话缓存与复用
- 密钥、凭据、加密存储
- **AI 不允许直接访问 Connection**

### 4.2 Plugin Manager

负责：插件生命周期。

生命周期：`Load → Enable → Disable → Unload`

插件必须注册：
- Metadata（元信息）
- Commands（能力）
- Recipes（预定义操作）
- Workflows（编排）
- Permission（权限声明）
- Settings（配置项）

### 4.3 Command Engine（最核心模块）

统一执行命令。

```
RunCommand(pluginId, commandId, params)
        │
        ▼
   PluginManager 查找 Plugin
        │
        ▼
   校验权限 & 参数
        │
        ▼
   执行 Command
        │
        ▼
   返回 Result（可序列化）
```

**Command 契约**：
- 每条 Command 都有 **唯一 ID**（例：`ssh.exec` / `docker.list` / `pve.startVM`）
- 输入 / 输出 / 错误 **全部可序列化**（便于 CLI、API、AI 调用）
- 支持 **同步 / 异步 / 流式**（如 Terminal 输出）

### 4.4 Recipe Engine

Recipe = **由若干 Command 组成的、预定义、已测试的操作**。

示例：
- `system.cpu` → `top -bn1 | head`
- `system.disk` → `df -h`
- `docker.status` → `docker ps` + `docker stats` + `docker images`

**特点**：
- 完全**不依赖 AI** 也可运行
- 参数经过验证，安全可控
- 是 AI 调用的**首选载体**

### 4.5 Workflow Engine

Workflow = **多个 Recipe / Command 的编排**。

示例（部署 .NET）：

```
上传 → 停止服务 → 备份 → 覆盖 → 启动 → 健康检查
                                        │
                                        ▼
                            成功 or Rollback / Retry / Notify
```

**能力**：
- 条件分支 / 并行 / 串行
- 失败回滚
- 重试策略
- 通知（邮件 / IM / Webhook）
- 变量与上下文传递

---

## 五、Plugin SDK

统一接口：

```
Plugin
├── Metadata      # id / name / version / author / description
├── Commands      # 最小执行单元
├── Recipes       # 预定义组合操作
├── Workflows     # 编排流程
├── Permission    # Read / Write / Execute / Dangerous
└── Settings      # 用户配置项（连接、路径、开关）
```

### 5.1 Metadata

- `id`（唯一标识，例：`ssh`、`docker`）
- `name` / `version` / `author` / `description`
- 依赖声明（依赖的 Core 版本、其他 Plugin）

### 5.2 Command

最小执行单元。示例：

- `ssh.exec`
- `ssh.upload` / `ssh.download`
- `docker.listContainer`
- `docker.pullImage`
- `pve.startVM`

要求：
- 输入 / 输出 / 错误全部**可序列化**
- 单一职责，不承担编排逻辑

### 5.3 Recipe

由多个 Command 组成，**无 AI 参与也能直接运行**。

示例：`server.status` → `cpu` + `memory` + `disk` + `network`

### 5.4 Workflow

多 Recipe 编排 + 分支 + 回滚 + 通知（见 4.5）。

### 5.5 Plugin 声明示例（YAML）

```yaml
plugin:
  id: ssh
  name: SSH
  version: 0.1.0
  author: mow

commands:
  - id: exec
    permission: execute
  - id: upload
    permission: write
  - id: download
    permission: read

recipes:
  - id: system.cpu
  - id: system.memory
  - id: system.disk

workflows:
  - id: deploy.dotnet
  - id: deploy.node
```

---

## 六、AI 架构

**AI 永远不是核心，AI 只是一种入口。**

### 6.1 调用链

```
User Prompt
    │
    ▼
Planner（LLM）─── 读取 Plugin Metadata / Recipe / Workflow 目录
    │
    ▼
选择 Workflow / Recipe / Command
    │
    ▼
Command Engine
    │
    ▼
Plugin → Connection → Target
```

### 6.2 AI 的职责

- 理解用户 → **选择或组合已有能力**
- 解释执行结果（自然语言总结）
- 提出下一步建议

### 6.3 AI 不能做的事

- 直接生成任意 Shell 并执行
- 绕过 Command Engine
- 直接访问 Connection Manager
- 未经权限确认执行 Dangerous 操作

### 6.4 AI 作为 Plugin

**AI 本身也是一个 Plugin**，Provider 可插拔：

```
AI Plugin
 └── Provider
      ├── ChatGPT
      ├── Claude
      ├── Gemini
      ├── Qwen
      ├── DeepSeek
      └── Local (Ollama / vLLM ...)
```

### 6.5 示例

用户：**"为什么 Docker 起不来？"**

AI 决策：

```
Run Recipe: docker.status
      ↓
Run Recipe: system.logs (docker)
      ↓
Run Recipe: system.systemd (docker)
      ↓
汇总分析并解释
```

---

## 七、目录结构

```
project/
├── docs/                  # 架构、RFC、规范
│   ├── vision.md
│   ├── architecture.md    # ← 本文档
│   ├── plugin-system.md
│   ├── command-engine.md
│   ├── workflow.md
│   ├── recipe.md
│   ├── ai.md
│   ├── ui.md
│   └── roadmap.md
├── apps/
│   ├── desktop/           # 桌面客户端（Terminal / Dashboard / AI Chat）
│   └── cli/               # 命令行入口
├── core/
│   ├── command/           # Command Engine
│   ├── workflow/          # Workflow Engine
│   ├── recipe/            # Recipe Engine
│   ├── plugin/            # Plugin Manager
│   ├── connection/        # Connection Manager
│   ├── config/
│   └── logger/
├── plugins/
│   ├── ssh/
│   ├── docker/
│   ├── pve/
│   └── ai/
├── sdk/                   # Plugin SDK（第三方开发者使用）
├── examples/              # Recipe / Workflow 示例
└── tests/
```

---

## 八、插件开发规范

### 必须遵守

- 插件**不得**直接操作 UI
- 插件**不得**依赖 AI
- 插件**只能**注册 Commands / Recipes / Workflows

### Docker Plugin 示例

```
Docker Plugin
├── Commands
│   ├── docker.list
│   ├── docker.pull
│   ├── docker.stop
│   ├── docker.logs
│   └── docker.rm            # permission: dangerous
├── Recipes
│   ├── docker.health
│   ├── docker.cleanup
│   └── docker.status
└── Workflows
    └── docker.deploy
```

---

## 九、权限模型

所有 Command **必须声明权限**：

| 权限 | 说明 |
| --- | --- |
| **Read** | 只读，例如 `docker.list`、`system.cpu` |
| **Write** | 写文件、修改配置，例如 `ssh.upload` |
| **Execute** | 执行命令，例如 `ssh.exec` |
| **Dangerous** | 不可逆或高影响操作，例如 `rm`、`docker rm -f`、重启节点 |

### AI 调用规则

- Read → 可直接调用
- Write / Execute → 记录审计日志
- **Dangerous → 必须弹窗询问 / 二次确认**

---

## 十、日志与可观测

所有操作必须记录：

```
[2026-07-09 12:34:56] user=alice
target=SSH:server01
plugin=ssh command=exec params={"cmd":"docker ps"}
result=Success duration=42ms
```

### AI 触发的操作必须额外记录

- 原始 Prompt
- 选择的 Workflow / Recipe / Command 链路
- 每一步返回结果
- 最终解释

### 目的

- **审计**：谁、什么时候、对哪台机器做了什么
- **回放**：可以重放某次 Workflow
- **调试**：AI 决策路径可追溯

---

## 十一、Roadmap

### v0.1（1~2 周） — 优秀的 SSH 客户端

- SSH 连接
- Terminal
- SFTP（上传 / 下载）
- 保存连接、密钥管理
- Plugin Framework（雏形）
- **不接入 AI**

### v0.2（2~3 周） — Command / Recipe / Workflow

- Command Engine
- Recipe Engine（`system.cpu` / `system.disk` / `docker.status` 等）
- Workflow Engine（部署 .NET / Node / Docker、备份数据库）

### v0.3（2 周） — Docker Plugin

- Docker Plugin（list / pull / stop / logs / rm）
- Docker Dashboard（GUI）

### v0.4（1 周） — AI Plugin

- AI Plugin（作为独立 Plugin）
- Provider 抽象（ChatGPT / Claude / Gemini / Qwen / DeepSeek / Local）
- MCP 支持
- AI 只能调用已有 Recipe / Workflow / Command

### v0.5 — 扩展生态

- PVE Plugin
- Kubernetes Plugin
- 数据库 Plugin
- Marketplace（插件市场雏形）

---

## 十二、设计原则（必须遵守）

写入 `CONTRIBUTING.md`，任何贡献者必须遵守：

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

---

## 十三、平台愿景（长期）

不把它定位成"运维工具"，而是"**运维平台（Platform）**"。

从 Day 1 起就预留扩展能力：

- **Marketplace**：第三方发布 Docker / Nginx / Redis / 云厂商插件
- **Workflow 分享**：导入 / 导出工作流，形成"运维经验库"
- **Team Workspace**（未来）：团队共享服务器、Recipe、Workflow
- **Provider 抽象**：AI（ChatGPT / Claude / 本地模型）、连接（SSH / WinRM / API）、通知（邮件 / Telegram / 企微）都只是可替换的 Provider

### 开发方式

- 每个模块（Plugin SDK / Workflow / Recipe / Command / AI Provider）都写成**独立 RFC 文档**
- 不"边聊边改架构"，先文档后代码
- 保持一致的设计语言，避免功能堆积导致混乱

---

## 附录 A：MVP 起步指南

1. 建立 Git 仓库，先写 `docs/`，暂不写代码
2. 完成本份 `architecture.md`（v0.1 Draft）
3. **MVP 只做一个 Plugin：SSH**，但 **Plugin Framework 必须写完整**
4. 先定义 **Core**（PluginManager / ConnectionManager / CommandEngine / WorkflowEngine / Config / Logger）
5. UI 只调用 Core，不写业务逻辑
6. Plugin SDK 是最重要的地方，先设计 SDK 再实现具体插件
7. **不要急着接 AI**，先让 SSH → Recipe → Workflow 全部跑通，AI 只是最后一步

## 附录 B：一句话产品原则

> **AI is optional. Automation is essential.**
>
> **Core 永远不依赖 AI，AI 永远依赖 Core。**
