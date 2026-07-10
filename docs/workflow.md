# RFC: Workflow Engine

- 状态：**Implemented (MVP)**
- 版本：v0.2
- 更新日期：2026-07-10
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

> v0.2 已交付 **顺序执行 + 变量传递** 两项，其余能力见 §7.5 与 [docs/roadmap.md](./roadmap.md)。

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
      params: { file: "${inputs.package}", dest: "/opt/app/" }
    - id: stop
      command: ssh.exec
      params: { cmd: "systemctl stop ${inputs.service}" }
    - id: backup
      recipe: file.backup
    - id: start
      command: ssh.exec
      params: { cmd: "systemctl start ${inputs.service}" }
    - id: health
      recipe: http.healthcheck
      retry: { max: 3, backoff: 2s }      # v0.3+
  onFailure:                              # v0.3+
    - rollback: [start, stop, upload]
    - notify: { channel: "webhook", target: "${notify.url}" }
```

## 5. 技术选型（v0.2 落地）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| DSL 格式 | **YAML** | 门槛低、可读、便于版本控制 |
| DSL 解析 | `gopkg.in/yaml.v3`（严格模式 `KnownFields(true)`） | 未知字段直接报错，避免拼写错误静默生效 |
| 变量表达式 | `github.com/expr-lang/expr` | 支持 `${var}` 与条件、算术表达式 |
| 执行器 | `workflow.Runner` + `CommandExecutor` / `RecipeExecutor` 抽象 | CLI / Desktop 各自注入 Adapter |
| 状态持久化 | 暂无（v0.3+ 引入 SQLite） | 目前仅内存 Result |
| 通知 | 暂无（v0.3+ 引入 Provider 抽象） | |

## 6. 待讨论 / Roadmap

- [ ] Rollback 自动生成 vs 手动声明（v0.2 未实现，v0.3 手动优先）
- [ ] 并行步骤的资源竞争与取消传播
- [ ] Workflow 版本化与迁移
- [ ] 是否支持"人在回路"的手工确认节点
- [ ] 是否支持代码定义（Go / TS Fluent API）作为 YAML 的补充

---

## 7. MVP 已实现字段（v0.2）

以下字段已在 `core/workflow` 落地并被 CLI (`mow workflow run`) / Desktop (`WorkflowPage`) 消费。

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
| `when` | string | 可选；expr-lang 表达式，求值为 `false` 时跳过（`Skipped`），求值失败中断 Workflow。**v0.3 第一批已合入** |

### 7.4 变量插值

- `${inputs.<name>}` — Workflow 输入
- `${steps.<id>.out.<field>}` — 已完成步骤的输出（`Step` 成功后其 `Data` 会被反序列化为 `map[string]any` 挂到 `out`）
- 支持完整 [expr-lang](https://github.com/expr-lang/expr) 表达式：`${inputs.port + 1}`、`${inputs.debug ? "on" : "off"}`
- 边界：
  - 字符串里没有 `${}` → 原样返回
  - 整串仅一个 `${expr}` → 保留原始类型（int / bool / slice / …）
  - 混合形态 → 逐段替换后拼字符串
  - 未定义变量 / 语法错误 → `InterpolationError` 携带偏移量 + 表达式

#### 7.4.1 `when` 条件分支（v0.3 第一批）

```yaml
steps:
  - id: probe
    command: ssh.exec
    params: { cmd: "curl -sf http://localhost/healthz || echo unhealthy" }

  - id: repair
    command: ssh.exec
    when: 'contains(steps.probe.out.stdout, "unhealthy")'
    params: { cmd: "systemctl restart myapp" }

  - id: notify
    command: notify.webhook
    when: inputs.notify_on_skip == false || steps.repair.out.ok
    params: { message: "deployment finished" }
```

- 语法与 `${...}` 内一致，但整串就是表达式，**不需要** `${}` 包裹
- `false` → Step 记为 `Skipped`，`OK=true`，不写 `steps.<id>.out.*`
- 求值失败 → `ErrorCode=WHEN_EVAL`，Workflow 中断（防御式：与语法错等同）
- CLI 打印 `⤼ skipped (when=...)`；Desktop 用 `⤼` 图标 + `wf-log-skipped` 样式区分

### 7.5 尚未实现（v0.3+）

按 **分批推进** 顺序落地，避免一次交付太大：

| 字段 / 特性 | 状态 | 说明 |
| --- | --- | --- |
| `when: <expr>` | 🔨 **v0.3 第一批（已合入）** | 条件分支；表达式复用 expr-lang，无 `${}` 包裹 |
| `retry: { max, backoff }` | ⏳ v0.3 第二批 | 单 step 重试；先做 fixed / exponential backoff，暂不实现 jitter |
| 状态持久化（SQLite） | ⏳ v0.3 第三批 | 与审计日志共享存储；先只做执行历史查询 |
| `on_failure` / `rollback` | ⏳ v0.3 第四批 | 手动声明式回滚；`rollback` 是 `on_failure` 的语法糖 |
| `parallel: true` | ⏳ v0.3 第五批 | **最后做**，涉及取消传播、资源竞争、事件顺序、审计一致性、测试复杂度显著上升 |
| `notify: { channel, target }` | v0.4+ | 邮件 / IM / Webhook 通知 |
| 每 Step 独立 `target` | v0.4+ | v0.3 仍全 Workflow 共用一个 target |
| Workflow 版本化 / 迁移 | v0.4+ | 与 Marketplace 联动 |

> 严格模式（YAML 未知字段报错）不受影响：每一批合入前，写了新字段的 workflow 都会直接被拒绝，避免拼写歧义。

---

## 8. 最小可跑示例

见 [`examples/workflows/deploy-static-site.yaml`](../examples/workflows/deploy-static-site.yaml)。

```yaml
workflow:
  id: deploy.static-site
  name: Deploy Static Site
  description: 最小可运行的静态站点发布 Workflow：备份 → 上传 → 健康检查

  inputs:
    - name: site
      type: string
      required: true
      description: 站点名称（用于目录 / 备份文件命名）
    - name: local_dir
      type: string
      required: true
    - name: remote_dir
      type: string
      default: /var/www/site
    - name: health_port
      type: int
      default: 80

  steps:
    - id: backup
      command: ssh.exec
      params:
        cmd: "if [ -d ${inputs.remote_dir} ]; then tar -czf /tmp/backup-${inputs.site}.tgz -C ${inputs.remote_dir} .; else mkdir -p ${inputs.remote_dir}; fi"
      timeout: 60s

    - id: upload
      command: ssh.exec
      params:
        cmd: "mkdir -p ${inputs.remote_dir} && echo uploaded ${inputs.site} from ${inputs.local_dir} to ${inputs.remote_dir}"
      timeout: 60s

    - id: health
      command: ssh.exec
      params:
        cmd: "ss -ltn | grep -q ':${inputs.health_port} ' && echo healthy || echo unhealthy"
      timeout: 10s
```

### CLI 用法

```bash
# 只解析 + 校验
mow workflow validate examples/workflows/deploy-static-site.yaml

# 实际执行（需要已注册的 SSH Target）
mow workflow run examples/workflows/deploy-static-site.yaml \
  --target=srv1 \
  --input site=hello \
  --input local_dir=/home/me/dist \
  --input remote_dir=/var/www/hello \
  --input health_port=8080
```

### Desktop 用法

打开 **Workflow** 标签页 → 拖拽或选择 `.yaml` → 依据 `inputs` 声明填写表单 → **Run**，实时查看每一步 `▶/✓/✗` 的日志。
