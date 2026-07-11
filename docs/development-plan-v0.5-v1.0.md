# MOW v0.5 → v1.0 开发计划

- 状态：Accepted
- 版本：v1
- 更新日期：2026-07-11
- 基线：v0.4.0 GA
- 相关文档：[Roadmap](./roadmap.md) · [设计原则](./design-principles.md) · [插件系统](./plugin-system.md) · [Workflow](./workflow.md)

## 1. 目标与产品边界

MOW 后续版本的产品定位保持为：

> Local-first、automation-first、AI-assisted 的现代运维工作台。

优先级固定为：

1. 可靠自动化
2. 安全、权限与审计
3. 插件平台与生态
4. AI 辅助能力

v0.5 到 v1.0 的核心任务，不是继续堆叠页面和命令，而是把 v0.4 已验证的 Core、Plugin、Workflow 和 AI 能力发展为可安装、可升级、可迁移、可诊断、可扩展的平台。

### 1.1 明确不做

- 不发展为通用聊天客户端
- 不在 v0.5 建设复杂的云端 Marketplace 服务
- 不把 MOW 变成完整 Kubernetes IDE
- 不承担大型 CMDB 的全部职责
- 不把任意 SQL 编辑器作为数据库插件的首要目标
- 不允许 AI 绕过 Command Engine、权限、确认、脱敏和审计
- 不在结构化数据和真实使用场景不足时提前建设通用 RAG 平台

## 2. 版本总览

| 版本 | 主题 | 核心交付 | 前置依赖 |
| --- | --- | --- | --- |
| v0.4.1 | GA 收尾 | 版本一致性、SDK 契约测试、安装验收、迁移验证 | v0.4.0 |
| v0.5.0 | Manifest 与包格式 | `plugin.json` schema、`mow plugin validate`、兼容性拒绝启用 | v0.4.1 |
| v0.5.1 | 插件生命周期 | install / enable / disable / upgrade / uninstall、本地 Catalog 雏形、校验与回退 | v0.5.0 |
| v0.5.2 | 配置 UI + PVE 参考实现 | Schema 驱动的 CLI/Desktop 配置、PVE 只读闭环验证平台 | v0.5.1 |
| v0.6 | Workflow 2.0 | 版本化、子工作流、审批、调度、通知、SQLite 历史 | v0.5.2 |
| v0.7 | 基础设施扩展 | PVE 正式插件、Kubernetes MVP | v0.5.2、v0.6 部分能力 |
| v0.8 | 可观测与诊断 | 审计查询、诊断中心、指标与 Trace | v0.6 |
| v0.9 | AI Operations 2.0 | Plan、Dry-run、MCP、本地模型、知识接入 | v0.8 |
| v1.0 | 稳定承诺 | SDK/Protocol 稳定、迁移、安装升级、长期验证 | v0.5～v0.9 |

## 3. v0.4.1 — GA 工程化收尾

### 3.1 目标

消除 v0.4.0 发布后的版本和文档漂移，验证发布产物能在全新环境中完成安装与首次运行，不增加新的产品功能。

### 3.2 开发范围

- 建立仓库唯一版本源
  - CLI、Desktop、前端、官方插件和 Release 从同一版本源读取
  - 构建时通过 ldflags 或生成文件注入版本
  - 禁止手工维护多份互相独立的 GA/alpha 状态
- 修正文档状态
  - `roadmap.md` 标记 v0.4.0 已发布
  - README、CHANGELOG、验收清单和 Release 内容相互链接
- SDK 契约测试
  - Plugin lifecycle
  - Metadata / CommandSpec 编解码
  - Stream 回调顺序
  - 错误码和兼容行为
- 发布产物 Smoke Test
  - 从 Release 产物安装 CLI 和官方插件
  - 首次启动、插件发现、mock AI、SSH/Docker 可选探测
  - checksum 验证
- 配置迁移验证
  - v0.3 配置升级到 v0.4
  - 缺失字段使用安全默认值
  - 旧配置失败时给出可操作错误

### 3.3 验收门槛

- [x] 仓库只存在一个权威应用版本源
- [x] SDK 核心契约和 gRPC 边界已有测试
- [x] Windows、Linux、macOS 发布产物启动 Smoke Test 已进入 Release 阻断矩阵
- [x] v0.3 → v0.4 配置迁移测试通过
- [x] roadmap、README、CHANGELOG、前端版本已同步为 v0.4.1；待 tag 验证
- [x] 未新增破坏性 SDK 或 Plugin Protocol 变更

## 4. v0.5 — 插件平台化

### 4.1 目标

让第三方插件能够被规范地开发、打包、发现、安装、配置、升级、禁用和卸载，并用 PVE 插件验证完整生命周期。

**v0.5 拆分为三个独立可 tag 的子版本**，每个子版本都有独立的发布门槛，可独立回退：

- **v0.5.0**：Plugin Manifest 与包格式（地基）
- **v0.5.1**：插件生命周期（install / upgrade / uninstall + 本地 Catalog 雏形）
- **v0.5.2**：Schema 驱动的配置 UI + PVE 参考实现（闭环验证）

### 4.2 v0.5.0：Plugin Manifest 与包格式

**范围**：只做 Manifest schema、`mow plugin validate` 命令，以及运行时对 Manifest 的强校验。**不做**下载、安装、Catalog、UI。

推荐包结构：

```text
plugin-package/
├── plugin.json
├── bin/
├── schemas/
├── docs/
├── recipes/
└── workflows/
```

Manifest 至少包含：

- 插件 ID、名称、版本、作者、许可证、主页
- Core / SDK / Protocol 兼容范围
- 支持的 OS / Architecture
- 可执行文件入口和 checksum
- Commands、Connection Types、Permissions 摘要
- 配置 JSON Schema
- Recipes / Workflows 资源清单
- 数据格式版本与迁移入口
- 签名和发布来源信息

#### 4.2.1 v0.5.0 发布门槛

- [ ] 提供正式 JSON Schema，位于 `sdk/manifest/plugin.schema.json`
- [ ] `mow plugin validate <package>` 能返回稳定错误码
- [ ] Manifest 与运行时 Metadata 不一致时拒绝启用，错误码 `PLUGIN_MANIFEST_MISMATCH`
- [ ] Core / SDK / Protocol 兼容范围不满足时在启动子进程前拒绝加载，错误码 `PLUGIN_INCOMPATIBLE`
- [ ] 官方 SSH / Docker / AI 三个插件补齐 `plugin.json` 并通过 validate
- [ ] SDK 契约测试覆盖 Manifest 反序列化、兼容范围解析、错误码稳定性
- [ ] 未新增破坏性 SDK 或 Plugin Protocol 变更（Manifest 仅补充，运行时协议不变）

### 4.3 v0.5.1：插件生命周期

**范围**：以 v0.5.0 的 Manifest 为地基，落地插件的 install / enable / disable / upgrade / uninstall CLI 与本地 Catalog 雏形。**不做** Desktop 配置 UI、不做正式 Catalog 服务。

CLI：

```text
mow plugin list
mow plugin search
mow plugin install
mow plugin update
mow plugin enable
mow plugin disable
mow plugin uninstall
mow plugin doctor
```

Desktop 插件管理页（v0.5.1 保持最小可用）：

- 已安装插件、版本和健康状态列表
- 启用、禁用、卸载三个操作
- 兼容性错误和诊断输出显示

安全要求：

- 下载后必须校验 SHA-256
- 默认只允许可信来源
- 卸载不得静默删除插件数据
- 升级失败必须能回退旧二进制和旧 Manifest
- 插件配置中的 secret 不得进入普通配置明文

本地 Catalog（v0.5.1 雏形）：

- 静态 JSON Catalog + GitHub Release 产物
- 官方 Catalog + 自定义私有 Catalog URL
- 平台、架构和兼容版本过滤
- 缓存和离线读取
- Catalog 更新失败不影响已安装插件

#### 4.3.1 v0.5.1 发布门槛

- [ ] CLI 八条子命令齐全，错误码稳定
- [ ] 至少一个插件能从本地 Catalog 完成 install → enable → upgrade → 回退 → uninstall 全链路
- [ ] 升级失败自动回退旧二进制 + 旧 Manifest，数据不丢
- [ ] 卸载留存插件数据目录，`--purge` 才真删
- [ ] Windows / Linux / macOS 三平台 install 路径一致
- [ ] SHA-256 校验失败拒绝安装并保留错误码

### 4.4 v0.5.2：Schema 驱动的配置 UI + PVE 参考实现

**范围**：以 Manifest 中的 `settingsSchema` 驱动 CLI / Desktop 配置体验；用 PVE 只读插件跑通整个平台闭环。**不做**复杂创建向导、存储迁移、Dangerous 删除。

配置 UI：

- CLI：`mow plugin config <id>` 交互式表单，敏感字段脱敏输入
- Desktop 插件配置页：Schema 驱动的表单、字段级校验、secret 隔离存储
- 配置改动即时校验，不重启插件

PVE 参考插件（只读闭环）：

- Cluster / Node / VM / LXC 只读列表
- 状态和资源摘要
- start / stop / reboot 三条基本生命周期命令
- API Token 配置和脱敏
- fake PVE API 契约测试
- 使用 Manifest、Catalog、安装、升级完整链路验证

#### 4.4.1 v0.5.2 发布门槛

- [ ] PVE 参考插件不依赖源码仓库内的特殊路径
- [ ] 第三方开发者仅依赖公开 SDK 和 v0.5 三文档即可完成一个新插件
- [ ] Manifest `settingsSchema` 能在 CLI 与 Desktop 端渲染为一致表单
- [ ] Secret 字段在配置文件和日志中均不出现明文
- [ ] 插件兼容矩阵进入 CI（至少 SSH / Docker / AI / PVE 四款）
- [ ] 配置、凭据和插件数据有明确生命周期文档

### 4.5 v0.5.3：插件开发者体验（v0.5.2 后追加，可延后到 v0.6 前）

- `mow plugin init` 脚手架
- 官方示例插件模板
- SDK conformance test suite
- fake Core / fake Stream 测试工具
- 本地调试和热重载模式
- Manifest lint 与打包命令
- 发布产物生成和 checksum
- 插件开发、调试、升级、迁移文档

复杂 PVE 创建向导、存储迁移和高危删除放到 v0.7。

### 4.6 v0.5 总体门槛（三子版本合计）

以下门槛在 v0.5.2 发布时最终校验一次，等价于旧的"v0.5 发布门槛"：

- [ ] 至少一个插件能从 Catalog 安装、启用、升级、回退、卸载
- [ ] PVE 参考插件不依赖源码仓库内的特殊路径
- [ ] 第三方开发者仅依赖公开 SDK 和文档即可完成插件
- [ ] 插件兼容矩阵进入 CI
- [ ] Windows/Linux/macOS 安装路径一致
- [ ] 配置、凭据和插件数据有明确生命周期

## 5. v0.6 — Workflow 2.0

### 5.1 目标

把 Workflow 从 YAML 执行文件升级为可版本化、可审批、可调度、可复用的自动化资产。

### 5.2 DSL 与执行能力

- 单 step `target` 覆盖
- 子工作流调用
- `parallel_limit`
- 嵌套并行组
- Workflow 参数 JSON Schema
- Dry-run / Plan
- 手工审批节点
- 暂停、恢复和取消
- 幂等键和重复执行保护
- 更明确的失败、补偿和部分成功状态

### 5.3 资产生命周期

```text
Draft → Published → Deprecated → Archived
```

- Workflow ID 与不可变版本号
- 草稿编辑不影响已发布版本
- 运行记录固定引用具体版本
- 版本 diff、回退和迁移
- 导入、导出和签名

### 5.4 触发与通知

- 本地定时调度
- Webhook 触发
- 手工触发
- 通知插件：Webhook 优先，Email / IM 后续
- 失败、成功、等待审批事件
- 重启后恢复调度状态

### 5.5 历史存储

- SQLite 作为默认结构化历史后端
- JSONL 保留为简单/兼容后端
- 搜索、过滤、分页和统计
- 运行详情、步骤结果和审计关联
- 数据迁移、备份、压缩和保留策略

### 5.6 AI 生成 Workflow 的安全流程

```text
AI 生成草稿 → Validate → Dry-run → 人工确认 → Publish → Run
```

AI 不得直接生成并发布可执行 Workflow。

### 5.7 v0.6 发布门槛

- [ ] 已发布 Workflow 可稳定回放具体版本
- [ ] 调度任务重启后不丢失、不重复执行
- [ ] 审批和 Dangerous Command 均不能被 AI 或 API 绕过
- [ ] SQLite 迁移和备份恢复通过故障测试
- [ ] 子工作流具备深度和循环限制

## 6. v0.7 — 基础设施扩展

### 6.1 PVE 正式版

- Cluster / Node / Storage / VM / LXC 详情
- start / stop / reboot / shutdown
- snapshot 创建、列表、回滚
- Task history 与长任务进度
- Console 接入
- Dangerous 删除与资源修改护栏
- 创建 VM/LXC 作为后期能力

### 6.2 Kubernetes MVP

- Kubeconfig / Context / Namespace Target
- Workload、Pod、Service 基础列表
- logs、exec、describe、events
- rollout status / restart
- scale
- YAML apply 归类为 Write 或 Dangerous
- 多集群切换与明确的当前上下文提示

非目标：

- 不复制完整 Kubernetes Dashboard
- 不在首版实现 Helm 管理平台
- 不提供无约束的集群管理员自动化

### 6.3 数据库插件探索

拆分 PostgreSQL 和 MySQL 插件，首版只做只读诊断：

- 连接和版本检查
- 活跃会话
- 锁和阻塞
- 慢查询摘要
- 数据库容量
- 备份状态

任意 SQL 编辑器、DDL 和数据修改不进入首版。

### 6.4 v0.7 发布门槛

- [ ] PVE 插件通过 v0.5 全部生命周期流程
- [ ] Kubernetes 所有写操作具有明确权限和确认级别
- [ ] 多集群/多 Target 操作不会隐式复用错误上下文
- [ ] 每个新插件具备 fake API 契约测试和可选真实 E2E

## 7. v0.8 — 可观测与诊断中心

### 7.1 目标

把现有 Command 审计、Workflow 历史、AI 决策链和插件健康状态整合为统一诊断入口。

### 7.2 范围

- Command 审计查询与过滤
- Workflow 运行统计
- AI tool-use 时间线
- Target 健康状态
- 插件健康和错误码聚合
- 日志关联与 trace ID
- 诊断包导出
- 指标快照
- OpenTelemetry Trace
- 可选 Prometheus exporter
- 数据保留、脱敏和导出策略

### 7.3 诊断包安全

- 默认递归脱敏
- 导出前展示内容清单
- 凭据、Token、私钥永不导出
- 支持用户追加脱敏规则
- 诊断包带版本、平台和 checksum

### 7.4 v0.8 发布门槛

- [ ] 单次用户操作可关联 Command、Plugin、Workflow、AI 审计链
- [ ] 常见失败能生成可操作诊断建议
- [ ] 诊断包通过敏感信息回归测试
- [ ] 可观测能力关闭时不影响核心功能

## 8. v0.9 — AI Operations 2.0

### 8.1 目标

从“AI 可以安全调用只读工具”升级为“AI 可以生成可解释、可预览、可审批的运维计划”。

### 8.2 计划与执行

- Plan 模式：只生成步骤和影响范围
- Explain 模式：解释 Command、Workflow 和风险
- Dry-run 结果展示
- 分步确认和审批
- Tool 执行时间线
- 预算：最大轮数、时间、Token、费用
- Provider fallback 与熔断
- 会话持久化、恢复和导出

### 8.3 Provider 与 MCP

- Ollama / 本地 OpenAI-compatible Provider
- Anthropic 原生 Provider，按真实需求决定
- MCP Client：接入外部只读工具和知识源
- MCP Server：暴露经过 allowlist 的 MOW Read Command
- MCP 工具仍需映射到 MOW 权限、审计和脱敏模型

### 8.4 知识接入

优先接入有明确价值的资料：

- 运维手册
- 历史故障记录
- Workflow / Recipe
- Command 文档
- Target 元数据

Embedding / Vector Store 必须是可替换的独立组件；没有检索质量评估和权限过滤前，不进入默认执行链路。

### 8.5 v0.9 发布门槛

- [ ] AI Plan 与实际执行严格分离
- [ ] MCP 工具不能绕过 Command Engine
- [ ] 知识检索遵守 Target、用户和数据源权限
- [ ] Provider fallback 不会导致重复执行工具
- [ ] 会话历史和模型输入均经过脱敏

## 9. v1.0 — 稳定承诺

### 9.1 稳定接口

- Plugin Protocol 进入兼容承诺期
- SDK 公共 API 有语义化版本和废弃策略
- Manifest Schema 稳定
- Workflow DSL 有版本字段和迁移规则
- 配置与持久化数据有正式迁移框架

### 9.2 产品交付

- Windows / Linux / macOS 安装器或明确安装包
- 自动或半自动升级
- 离线安装和升级路径
- 完整卸载与数据保留选择
- 首次运行向导
- 插件、Target、AI Provider 配置诊断

### 9.3 可靠性

- 崩溃后不损坏配置、凭据、审计和 Workflow 历史
- 插件进程异常不会拖垮宿主
- 升级失败可回退
- 长时间运行和资源泄漏测试
- 跨版本兼容矩阵
- RC 稳定期和真实环境验证记录

### 9.4 文档

- 用户手册
- 管理与安全手册
- Plugin SDK 开发手册
- Workflow DSL 参考
- API / Command 参考
- 安装、升级、迁移和故障排查手册

### 9.5 v1.0 发布门槛

- [ ] SDK / Protocol / Manifest / Workflow DSL 均有稳定承诺
- [ ] 至少一个仓库外插件验证公开 SDK
- [ ] SSH、Docker、PVE 或 Kubernetes 真实链路经过长期验证
- [ ] 所有支持平台完成安装、升级、回退和卸载验收
- [ ] AI 无法绕过权限、确认、审计和脱敏的安全测试持续通过
- [ ] 完成至少一个 RC 周期且无阻断级数据损坏问题

## 10. 横向工程要求

以下要求贯穿所有版本，而不是单独延后处理。

### 10.1 测试金字塔

- Core / SDK：高覆盖单元测试和契约测试
- Plugin：fake server 契约测试
- Apps：CLI 命令测试、Desktop 后端测试、关键前端状态测试
- E2E：按变更路径选择性运行，主分支运行完整关键链路
- Release：从二进制产物开始的 Smoke Test

### 10.2 兼容性

- Core × Plugin Protocol
- Core × SDK
- 配置版本
- Workflow DSL 版本
- 数据库 schema 版本
- OS / Architecture

每次发布必须明确兼容矩阵，不以“能够编译”替代兼容验证。

### 10.3 安全

- Secret 只通过安全存储或环境变量引用
- 所有外部输入进行 schema 校验
- Dangerous 操作必须显式确认
- AI、Workflow、API 与 UI 共享相同权限模型
- 下载产物必须校验来源与完整性
- 审计和诊断导出默认脱敏

### 10.4 可观测性

- 稳定错误码
- Audit ID / Trace ID 关联
- 结构化日志
- 插件健康状态
- 用户可导出的诊断信息

### 10.5 文档与版本治理

- `roadmap.md` 只维护版本级状态
- 本文维护 v0.5～v1.0 的详细范围和门槛
- 每个版本建立独立 acceptance checklist
- 实现完成时同步更新 RFC、CHANGELOG 和验收清单
- 发布后不得继续在 roadmap 中保留“进行中”的旧叙述

## 11. 建议实施顺序

1. 完成 v0.4.1 工程化收尾。
2. 冻结 Manifest v1 草案并实现校验器。
3. 完成插件安装、启停、升级、回退和卸载。
4. 用 PVE 参考插件验证整个插件生命周期。
5. 建设静态 Catalog 和插件开发者工具。
6. 推进 Workflow 版本化、子工作流、审批和调度。
7. 完成 PVE 正式版和 Kubernetes MVP。
8. 建设统一诊断中心和结构化可观测链路。
9. 在结构化数据成熟后推进 AI Plan、MCP 和知识接入。
10. 冻结稳定接口，进入 v1.0 RC 与长期验证。

## 12. 版本规划调整规则

出现以下情况时，可以调整小版本范围：

- 真实用户验证表明某项能力价值不足
- 安全或数据迁移问题需要优先修复
- Plugin Protocol / Workflow DSL 需要破坏性调整
- 支持平台发生重大兼容变化

但不得绕过以下顺序约束：

- 插件生态扩张前先完成插件生命周期
- Workflow 调度前先完成版本化和持久化
- AI 执行能力扩张前先完成 Plan、审批和可观测
- v1.0 前必须完成迁移、安装升级和外部插件验证
