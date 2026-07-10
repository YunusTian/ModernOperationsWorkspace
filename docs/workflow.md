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

## 7. MVP 已实现字段（v0.2）

以下字段已在 `core/workflow` 落地并被 CLI / Desktop 消费；未列出者仍是 Roadmap。

### 7.1 顶层 `workflow`

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | ✅ | 全局唯一标识 |
| `name` | string |  | 展示名 |
| `description` | string |  | 多行描述 |
| `inputs` | `Input[]` |  | 见 7.2 |
| `steps` | `Step[]` | ✅ | 见 7.3；至少 1 项 |

### 7.2 `inputs[]`

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 必填；同一 workflow 内唯一 |
| `type` | enum | `string` / `int` / `bool` / `file`；空值等价 `string` |
| `required` | bool | `true` 时执行前必须提供 |
| `default` | any | 可选；配合 `required=false` 使用 |
| `description` | string | 前端表单提示语 |

### 7.3 `steps[]`

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 必填；同一 workflow 内唯一 |
| `command` | string | 全限定 `<plugin>.<command>`；与 `recipe` 二选一 |
| `recipe` | string | 内置 recipe id；与 `command` 二选一 |
| `params` | map | 传给 Command / Recipe 的入参；值可用 `${...}` 插值 |
| `timeout` | duration | 例：`5s` / `1m30s`；`0` 或缺省走底层默认 |

### 7.4 变量插值

- `${inputs.<name>}` — Workflow 输入
- `${steps.<id>.out.<field>}` — 已完成步骤的输出（`Step` 成功后其 `Data` 会被反序列化为 `map[string]any` 挂到 `out`）
- 支持完整 [expr-lang](https://github.com/expr-lang/expr) 表达式：`${inputs.port + 1}`、`${inputs.debug ? "on" : "off"}`
- 边界：整串仅一个 `${expr}` 保留原始类型；混合形态自动拼字符串；未定义变量报 `InterpolationError` 携带 offset

### 7.5 尚未实现（Roadmap）

- `onFailure` / `rollback` / `retry` / `notify`
- 分支 / 并行（`parallel: true`、`when: <expr>`）
- 每 Step 独立 `target`
- 状态持久化（SQLite）
- Workflow 版本化 / 迁移
