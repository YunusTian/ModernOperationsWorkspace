# RFC: 权限模型

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 九

---

## 1. 权限等级

所有 Command **必须声明权限**：

| 权限 | 说明 | 示例 |
| --- | --- | --- |
| **Read** | 只读 | `docker.list`、`system.cpu` |
| **Write** | 写文件 / 修改配置 | `ssh.upload` |
| **Execute** | 执行命令 | `ssh.exec` |
| **Dangerous** | 不可逆或高影响 | `rm`、`docker rm -f`、重启节点 |

## 2. AI 调用规则

- **Read** → 可直接调用
- **Write / Execute** → 记录审计日志
- **Dangerous** → **必须弹窗询问 / 二次确认**

## 3. 二次确认协议草案

```text
ConfirmationRequest
├── auditId
├── command: { pluginId, commandId, params }
├── reason: string          # AI 给出的执行理由
└── impact: string          # 潜在影响描述

ConfirmationResponse
├── approved: bool
├── operator: string
└── ts
```

## 4. 技术选型（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| 权限声明位置 | Plugin YAML manifest + Command 结构体 tag | 双向校验 |
| 权限校验 | Command Engine 中间件 | 所有调用统一拦截 |
| 二次确认 UI | Wails 前端弹窗（桌面） / TTY prompt（CLI） | |
| Dangerous 白名单 | 用户可配置"免确认清单" | 高级用户提效 |

## 5. 待讨论

- [ ] 是否支持基于角色（RBAC）的多用户模型（v0.5+ Team Workspace）
- [ ] 团队场景下的审批链
- [ ] Dangerous 操作是否强制录像 / 快照
- [ ] 是否引入"只读模式"全局开关
- [ ] 二次确认的超时策略（AI 场景不能无限等待）
