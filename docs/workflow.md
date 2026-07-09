# RFC: Workflow Engine

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 4.5

---

## 1. 定义

Workflow = **多个 Recipe / Command 的编排**。

## 2. 示例（部署 .NET）

```
上传 → 停止服务 → 备份 → 覆盖 → 启动 → 健康检查
                                        │
                                        ▼
                            成功 or Rollback / Retry / Notify
```

## 3. 核心能力

- 条件分支 / 并行 / 串行
- 失败回滚
- 重试策略
- 通知（邮件 / IM / Webhook）
- 变量与上下文传递

## 4. Workflow 声明草案（YAML）

```yaml
workflow:
  id: deploy.dotnet
  inputs:
    - name: package
      type: file
    - name: service
      type: string
  steps:
    - id: upload
      command: ssh.upload
      params: { file: "${package}", dest: "/opt/app/" }
    - id: stop
      command: ssh.exec
      params: { cmd: "systemctl stop ${service}" }
    - id: backup
      recipe: file.backup
    - id: start
      command: ssh.exec
      params: { cmd: "systemctl start ${service}" }
    - id: health
      recipe: http.healthcheck
      retry: { max: 3, backoff: 2s }
  onFailure:
    - rollback: [start, stop, upload]
    - notify: { channel: "webhook", target: "${notify.url}" }
```

## 5. 技术选型（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| DSL 格式 | **YAML**（首版） | 门槛低、可读、便于版本控制 |
| DSL 解析 | `gopkg.in/yaml.v3` | |
| 变量表达式 | `expr-lang/expr`（Go 表达式引擎） | 支持 `${var}` 与条件判断 |
| 状态持久化 | SQLite（Workflow 执行历史） | 与审计日志共享 |
| 通知 | Provider 抽象（Webhook / Email / IM 后续插件化） | |

## 6. 待讨论

- [ ] Rollback 是否自动生成还是手动声明（v0.1 手动）
- [ ] 并行步骤的资源竞争与取消传播
- [ ] Workflow 版本化与迁移
- [ ] 是否支持"人在回路"的手工确认节点
- [ ] 是否支持代码定义（Go / TS Fluent API）作为 YAML 的补充
