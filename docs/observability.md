# RFC: 日志与可观测

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 十

---

## 1. 审计日志

所有操作必须记录：

```
[2026-07-09 12:34:56] user=alice
target=SSH:server01
plugin=ssh command=exec params={"cmd":"docker ps"}
result=Success duration=42ms
```

## 2. AI 触发的操作必须额外记录

- 原始 Prompt
- 选择的 Workflow / Recipe / Command 链路
- 每一步返回结果
- 最终解释

## 3. 三大目的

| 目的 | 说明 |
| --- | --- |
| **审计** | 谁、什么时候、对哪台机器做了什么 |
| **回放** | 可以重放某次 Workflow |
| **调试** | AI 决策路径可追溯 |

## 4. 分类

| 类别 | 说明 | 存储 |
| --- | --- | --- |
| Audit | 命令执行审计 | 结构化 JSON，长期保存 |
| Runtime | 应用运行日志 | 本地文件 + 滚动 |
| Diagnostic | 崩溃 / 异常 | Crash Report |
| AI Trace | Prompt / Tool Call / Response | 结构化，可回放 |

## 5. 技术选型（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| 日志库 | **`log/slog`（Go 1.21+ 标准库）** | 结构化 JSON，零依赖 |
| 日志格式 | JSON Lines | 便于机器解析 |
| 审计存储 | 本地 SQLite（`modernc.org/sqlite`，纯 Go 无 CGO） | append-only 表设计 |
| 日志滚动 | `lumberjack.v2` | 大小 / 时间双策略 |
| 脱敏 | 自研中间件（关键字段名单 + 正则） | 密码 / Token / 私钥 |
| 分布式追踪 | 预留 OpenTelemetry 接口，v0.2+ 引入 | |

## 6. 待讨论

- [ ] 审计日志是否上云 / 上远端 collector（企业场景）
- [ ] AI Trace 的存储上限与自动归档
- [ ] 回放模式的实现方式（dry-run / 影子执行）
- [ ] 敏感字段脱敏策略的可配置化
