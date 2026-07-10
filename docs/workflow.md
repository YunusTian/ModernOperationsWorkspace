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
| `retry` | object | 可选；`{ max, backoff, max_backoff, exponential }`，见 §7.4.2。**v0.3 第二批已合入** |
| `compensate` | object | 可选；`{ command|recipe, params, timeout }`，配合顶层 `on_failure.rollback` 触发。**v0.3 第四批已合入** |

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

#### 7.4.2 `retry` 单 Step 重试（v0.3 第二批）

```yaml
steps:
  - id: health
    command: ssh.exec
    params: { cmd: "curl -sf http://localhost/healthz" }
    retry:
      max: 5             # 总尝试次数含首次
      backoff: 500ms     # 每次失败后等待
      max_backoff: 5s    # 封顶（仅 exponential 有意义）
      exponential: true  # 每次 × 2；否则固定 backoff
```

**语义边界**：

| 场景 | 是否重试 |
|---|---|
| 执行器返回业务错误（`STEP_FAILED` / `CodedError`） | ✅ 重试 |
| `when` 求值失败（`WHEN_EVAL`） | ❌ 直接中断，`Attempts=0` |
| 参数插值失败（`INTERPOLATE`） | ❌ 直接中断，`Attempts=0` |
| 未配置执行器（`NO_EXECUTOR` / `INVALID_STEP`） | ❌ 直接中断，`Attempts=1` |
| `ctx` 取消 / 超时（backoff 期间） | ❌ 立即返回最后一次执行错误 |

**字段规则**：

- `max ∈ [0, 20]`；`0` / `1` 等价于不重试；上限 `20` 是硬约束，防止 YAML 手误。
- `backoff ≥ 0`；`0` 表示不等待，立即重试。
- `exponential: true` 强制 `backoff > 0`；每次 `= backoff × 2`，被 `max_backoff` 封顶。
- 校验：`backoff ≤ max_backoff`；违规在 `LoadBytes` 阶段就报错。

**观测**：

- `StepResult.Attempts` 记录实际执行次数（`Skipped` 步为 `0`）。
- `StepEvent.Phase = PhaseRetry` 在**每次失败后即将 sleep 前**触发一次，携带：
  - `Attempt` — 刚失败的这次是第几次
  - `MaxAttempts` — 总预算
  - `NextBackoff` — 即将 sleep 的时长
  - `Err` — 该次的原始错误
- CLI：`↻ retry 1/3 after 500ms: connection refused`；Desktop：状态转 `retrying`，展示 `attempt 1/3, retry in 500ms — …`。

#### 7.4.3 执行历史持久化（v0.3 第三批）

Runner 完成一次 Run 后（无论成功 / 失败），会把结果快照写入一份**执行历史**。默认后端是 JSON Lines 文件 `<data_dir>/workflow-runs.jsonl`，与审计日志共享目录；后续可换成 SQLite 而无需修改 Runner / UI。

**核心接口**（[core/workflow/history](../core/workflow/history)）：

```go
type Store interface {
    Save(ctx, *Record) error
    List(ctx, ListOptions) ([]Record, error)
    Get(ctx, runID string) (*Record, error)
}
```

- `Record` 与 `workflow.Result` 对齐，另加 `RunID / StartedAt / FinishedAt / Duration / TargetID / Caller / Inputs / Error`。
- `RunID` 由 Runner 生成（`run-` + 16 字节 hex），供 UI 与 CLI 关联。
- `List` 按 `FinishedAt` 倒序（新在前），默认 100 条，硬上限 500；支持 `WorkflowID` 过滤。
- `Get` 找不到返回 `(nil, nil)`。

**写入语义**：

- `RunnerOptions.History` 非 `nil` 时自动写盘；未设置则完全禁用（等价于 `history.Noop()`）。
- 写盘错误**不会**冒泡到 `Runner.Run` 的返回值——历史落盘失败最多丢一行观测数据，不应破坏业务。
- 使用独立的 3s ctx 兜底，避免 caller ctx 已取消导致写盘失败。

**为什么先 JSONL 再 SQLite**：

- 零 CGO 依赖：Windows / macOS / Linux 交叉编译不受阻。
- 单文件 append-only：崩溃最多丢一行；跑几百次 Workflow 完全够用。
- 单行坏了自动跳过，其它行照读——`bufio.Scanner` 直接跳到下一行。
- 到万级行才需要切换 SQLite；届时替换 `Store` 实现即可，`Record` / `HistorySink` 接口不变。

**CLI**：

```bash
mow workflow history list              # 最近 30 条
mow workflow history list --workflow deploy.static-site --limit 100
mow workflow history list --json       # 机读格式
mow workflow history show run-<hex>
mow workflow history show run-<hex> --json
```

**Desktop**：

- `WorkflowPage` 底部新增可折叠的 History 面板：
  - 列出最近 30 条：状态图标 / workflow / target / duration / finished / 步骤计数（含 ⤼ ↻ 标注）
  - 每次 Run 结束后自动刷新（不用手动）
  - 点击某行 → 抽屉展示 inputs / steps / audit id / error code

**前端 API**（Wails）：

| 方法 | 说明 |
|---|---|
| `App.ListWorkflowRuns({limit, workflow_id})` | 列表页 |
| `App.GetWorkflowRun(run_id)` | 详情抽屉 |


#### 7.4.4 `on_failure` / `rollback` 声明式回滚（v0.3 第四批）

```yaml
workflow:
  id: deploy.app
  steps:
    - id: upload
      command: ssh.upload
      params: { file: "${inputs.pkg}", dest: "/tmp/pkg" }
      compensate:
        command: ssh.exec
        params: { cmd: "rm -f /tmp/pkg" }
        timeout: 5s

    - id: deploy
      command: ssh.exec
      params: { cmd: "systemctl start myapp" }
      compensate:
        command: ssh.exec
        params: { cmd: "systemctl stop myapp && systemctl start myapp.old" }

    - id: health
      command: ssh.exec
      params: { cmd: "curl -sf http://localhost/healthz" }
      retry: { max: 3, backoff: 500ms }

  on_failure:
    rollback: [upload, deploy]
```

**触发条件**（严格）：

- Workflow 主流程返回错误 **且** `on_failure.rollback` 非空
- Runner **逆序遍历** rollback 列表；只对**执行成功过**的 step 调用其 `compensate`

**语义边界**：

| 场景 | 行为 |
|---|---|
| Workflow 全部成功 | ❌ 不触发；`Result.Rollback` 为空 |
| Step 被 `when` 跳过 (`Skipped`) | ❌ 不回滚（副作用未成立） |
| Step 执行失败（含 retry 用尽） | ❌ 不回滚该 step（未成功过） |
| rollback 列表含**未声明** `compensate` 的 id | 静默跳过；`Result.Rollback` 记一行 `Skipped=true` |
| `compensate` 执行失败 | ❌ **不嵌套 rollback**、❌ 不 retry；记入 `Result.Rollback` 后继续下一个 |
| Workflow 最终状态 | 保持失败（`res.OK=false`）——rollback 只是补偿观测，不改变结论 |

**Validate 规则**：

- `on_failure.rollback[i]` 必须存在于 `steps` 中；未知 id 直接拒
- `rollback` 列表内不能重复
- `compensate.command` / `compensate.recipe` 二选一
- 引用没有 `compensate` 的 step id 是**合法**的（允许"选择性补偿"）

**观测**：

- `Result.Rollback []StepResult` 保留全部补偿动作快照（按执行顺序，即声明的逆序）
- `StepEvent.Phase = PhaseRollback` 每个 compensate 完成后触发一次；`Result.OK` 表示补偿是否成功；`Skipped=true` 表示无 compensate
- CLI：`↩ rollback deploy 100ms` / `✗ rollback upload: rm failed` + summary `rolled_back=N`
- Desktop：主日志区插入独立行（`↩` 图标 + 蓝色 `rollback` 徽标）；History 抽屉底部新增独立 Rollback 表
- History：`Record.Rollback` 与 `Steps` 同源持久化，`show` 命令输出 `rollback:` 区块

**为什么 rollback 内部错误不再嵌套 rollback**：

补偿动作失败通常意味着"能自动做的都做过了"——继续嵌套只会让状态更混乱。当前策略是"记录、继续、通知用户手动介入"，为未来接入 `notify:` 通知渠道留了口子。

### 7.5 尚未实现（v0.3+）

按 **分批推进** 顺序落地，避免一次交付太大：

| 字段 / 特性 | 状态 | 说明 |
| --- | --- | --- |
| `when: <expr>` | 🔨 **v0.3 第一批（已合入）** | 条件分支；表达式复用 expr-lang，无 `${}` 包裹 |
| `retry: { max, backoff, max_backoff, exponential }` | 🔨 **v0.3 第二批（已合入）** | 单 step 重试；fixed / exponential，无 jitter |
| 执行历史持久化（JSONL） | 🔨 **v0.3 第三批（已合入）** | `<data_dir>/workflow-runs.jsonl`；SQLite 后端后续替换 |
| `on_failure` / `rollback` | 🔨 **v0.3 第四批（已合入）** | 手动声明式补偿；逆序遍历只回滚成功过的 step，不嵌套、不 retry |
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
