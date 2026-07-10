# RFC: Docker Plugin

- 状态：Draft（v0.3 第一 + 第二阶段落地）
- 版本：v0.3
- 更新日期：2026-07-10
- 相关章节：[roadmap.md § v0.3](./roadmap.md#v03--docker-plugin--docker-dashboard--下一版) · [plugin-system.md](./plugin-system.md) · [permission.md](./permission.md) · [ui.md](./ui.md)

---

## 1. 定位

Docker Plugin 是 v0.3 的首个新增插件，把"容器管理"这一常见运维意图沉到 MOW 的领域模型下——UI / CLI / AI 通过同一份 Command 面对同一个 Docker Engine。

它作为独立进程 gRPC 插件运行（复用 SSH 插件同款的 hashicorp/go-plugin + `sdk/pluginserve`），进程隔离，独立崩溃。

## 2. v0.3 第一阶段（本 RFC 覆盖范围）

| Command ID | 权限 | 语义 | 幂等 |
|---|---|---|---|
| `docker.list` | Read | 列容器（`all` / `limit` / `labels` 过滤） | ✅ |
| `docker.inspect` | Read | 单个容器详情（原样透传 Engine 响应） | ✅ |
| `docker.start` | Execute | 启动容器；304 → `already_in_state=true` | — |
| `docker.stop` | Execute | 停止容器（可选 `timeout_sec`） | — |
| `docker.restart` | Execute | 重启容器（可选 `timeout_sec`） | — |
| `docker.logs` | Read | 流式日志（`follow` / `tail` / `stdout` / `stderr` / `timestamps` / `since` / `until` / `tty`） | — |

## 3. 第二批（暂不实现，见 [roadmap.md](./roadmap.md#v03--docker-plugin--docker-dashboard--下一版)）

- `docker.rm` / `docker.kill` → **Dangerous**，走 Command Engine 二次确认
- `docker.pull` / `docker.push`
- `docker.exec`（流式，交互式命令）
- Docker Dashboard 前端（v0.3 第二阶段）
- Recipe：`docker.status` / `docker.health`
- Workflow：`docker.deploy`

## 4. 传输协议

Docker Engine 有三种常见暴露方式，本 MVP 一次性覆盖：

| Host | 场景 | 备注 |
|---|---|---|
| `unix:///var/run/docker.sock` | 本机 daemon | 通过 `net.Dial("unix", ...)` 走 UDS |
| `tcp://host:2375` | 远端裸 TCP | 不建议生产使用 |
| `tcp://host:2376` + TLS | 生产远端 | 必须提供 `TLSCA` / `TLSCert` / `TLSKey` 三件套 |

未实现：`ssh://` 隧道模式（v0.3 第二阶段）、`npipe://`（Windows）。

## 5. 凭据模型

- 存储：`core/connection.DockerCredentials`（`connection.Type = "docker"`）
- 加密：与 SSH 一致，用 `Sealer` (AES-256-GCM) 加密到 `Target.EncryptedCredentials`
- 下发：`Manager.Open` 时解密后作为 `sdk.Connection.Credentials`（JSON）传给插件

字段：

```jsonc
{
  "host": "tcp://10.0.0.5:2376",
  "api_version": "1.44",            // 可选
  "tls_verify": true,
  "tls_ca":   "-----BEGIN CERTIFICATE-----\n...",
  "tls_cert": "-----BEGIN CERTIFICATE-----\n...",
  "tls_key":  "-----BEGIN PRIVATE KEY-----\n..."
}
```

## 6. 错误码

| Code | 触发场景 | Retryable |
|---|---|---|
| `PARAM_INVALID` | 参数缺失 / 结构不合法 | ❌ |
| `CONNECTION_INVALID` | Connection 类型不匹配 / host 空 | ❌ |
| `DOCKER_CLIENT_INVALID` | TLS 配置错误 / 不支持的 scheme | ❌ |
| `DOCKER_DIAL_FAILED` | 网络层拨号失败 | ✅ |
| `DOCKER_NOT_FOUND` | 404 | ❌ |
| `DOCKER_CONFLICT` | 409 | ❌ |
| `DOCKER_NOT_MODIFIED` | 304（start 已运行 / stop 已停止；由 lifecycle 转成 `already_in_state=true`） | ❌ |
| `DOCKER_BAD_REQUEST` | 400 | ❌ |
| `DOCKER_UNAUTHORIZED` | 401 / 403 | ❌ |
| `DOCKER_ENGINE_ERROR` | 5xx | ✅ |
| `DOCKER_READ_FAILED` | 响应读取失败 | — |
| `CANCELED` / `TIMEOUT` | ctx 结束 | — |

## 7. 权限与审计

- 权限模型完全复用 [permission.md](./permission.md)
- 第一阶段最高权限只到 `Execute`（start / stop / restart）——不涉及 Dangerous，Command Engine 的二次确认在这一批不会触发
- 审计：由 `core/command.AuditMiddleware` 统一记录；插件无需自己写审计

## 8. 用法示例

### 8.1 CLI 注册 Docker Target

```bash
# 本机
mow target add dk-local --type docker \
    --docker-host unix:///var/run/docker.sock

# 远端 TLS
mow target add dk-prod --type docker \
    --docker-host tcp://10.0.0.5:2376 \
    --docker-tls-verify \
    --docker-tls-ca-file ca.pem \
    --docker-tls-cert-file cert.pem \
    --docker-tls-key-file key.pem
```

### 8.2 CLI 调用 Command

```bash
# 列出所有容器
mow run docker.list --target dk-local --param all=true

# 查看容器详情
mow run docker.inspect --target dk-local --param id=nginx

# 启动 / 停止 / 重启
mow run docker.start   --target dk-local --param id=nginx
mow run docker.stop    --target dk-local --param id=nginx --param timeout_sec=5
mow run docker.restart --target dk-local --param id=nginx
```

### 8.3 流式日志

`docker.logs` 是流式 Command，CLI 的一次性调用会拿到默认（100 行）历史；未来 `mow logs` 子命令会包装 follow 模式。

## 9. 测试策略

- 单元测试用 `net/http/httptest` 起一个假 Docker Engine，覆盖：
  - `docker.list`：`all` 参数、`labels` 过滤、5xx 可重试错误
  - `docker.inspect`：正常透传 / 404 / 参数缺失
  - `docker.start` / `stop` / `restart`：正常 / 304 → `already_in_state=true` / 404 / `timeout_sec`
  - `docker.logs`：8 字节 mux 头解码（stdout / stderr 分帧）、TTY 原始透传、参数缺失、`SignalCancel` 中止
  - 凭据 / host 解析：`splitHost` / `normalizeAPIVersion` / `resolveTarget`

- E2E：留待 v0.3 第二阶段（Desktop Dashboard 一起做）；本阶段单测足以证明 wire 正确。

## 10. 未纳入本阶段（v0.3 第三阶段 / v0.4 承接）

- 第一阶段之外的插件 Command：`docker.rm` / `docker.kill` / `docker.pull` / `docker.push` / `docker.exec`
- 镜像 / 卷 / 网络 / Compose 视图（Dashboard 第三阶段）
- Workflow 与 Docker 联动的 `on_failure` / `retry` / `parallel` / `when`
- `ssh://` 隧道模式的 Docker Host
- Docker Recipe / Workflow 模板

## 11. Docker Dashboard（v0.3 第二阶段）

Dashboard 是 Docker Plugin 在桌面客户端的 UI 门面。设计原则："以容器列表 → inspect → logs → 明确确认后操作为主路径"，不铺开镜像 / 卷 / 网络 / Compose。

### 11.1 主路径（严格顺序）

```
Targets 页
  └── 选中 Docker 类型 target → 侧栏 Docker tab 亮起
       │
       ▼
    容器列表（DockerList，all=true 默认，可切换过滤 exited）
       │
       ├── 点击行 → Inspect 抽屉（DockerInspect，只读 JSON）
       │
       ├── 点击 Logs → 底部 Logs 面板（DockerLogsOpen）
       │        · 事件：docker:logs:<sid>:stdout|:stderr|:exit
       │        · 面板关闭 / 切换容器 → DockerLogsClose 主动收尾
       │
       └── 点击 Start / Stop / Restart
                · 弹窗弹出，展示 command + container + audit 铭牌
                · 用户按下 Confirm → DockerLifecycle(confirmed=true)
                · 后端在 Confirmed=false 时直接拒绝（应用层护栏）
```

### 11.2 权限与二次确认

- 生命周期动作（start / stop / restart）的插件权限是 `Execute`，Command Engine 的 Dangerous 二次确认不会触发
- 桌面客户端的 `Confirm` 是 `command.AllowConfirmer{}`（Engine 层不拦），因此 UI 必须自己弹窗
- `DockerLifecycle` 后端强制校验 `Confirmed=true`：任何绕过 UI 的直接调用都会被拒绝

### 11.3 后端 API（Wails 绑定）

| 方法 | 说明 |
|---|---|
| `App.UpsertDockerTarget(in)` | 保存 Docker Target（host / api_version / TLS） |
| `App.DockerList(targetID, {all, limit, labels})` | 列容器 |
| `App.DockerInspect(targetID, containerID)` | 单个容器详情（原样 JSON） |
| `App.DockerLifecycle(targetID, {action, container, timeout_sec, confirmed})` | start / stop / restart |
| `App.DockerLogsOpen(targetID, {container, follow, tail, ...})` → `sessionID` | 打开流式日志 |
| `App.DockerLogsClose(sessionID)` | 主动关闭 |

### 11.4 事件

| Event | Payload | 说明 |
|---|---|---|
| `docker:logs:<sid>:stdout` | `base64(bytes)` | 单帧 stdout |
| `docker:logs:<sid>:stderr` | `base64(bytes)` | 单帧 stderr |
| `docker:logs:<sid>:exit` | `{ audit_id, error? }` | 流式结束 / 出错 |

### 11.5 未纳入 Dashboard（第三阶段承接）

- 镜像 / 卷 / 网络 视图
- Compose 支持
- 容器创建（`docker.create`）
- 容器 exec 交互式终端（复用 Terminal xterm.js）
- 容器资源统计（`/containers/{id}/stats`）

## 12. v0.3 第三阶段：`docker.rm` / `docker.pull` / `docker.push` / `docker.exec`

本阶段补齐插件的"完整生命周期 + 镜像分发 + 交互式 exec"。仍**不引入** `github.com/docker/docker` 官方 SDK，全部用标准库 `net/http` 手写。

### 12.1 命令总览

| Command | Permission | Streaming | Engine 端点 |
|---|---|---|---|
| `docker.rm` | **Dangerous** | ✗ | `DELETE /containers/{id}?force&v` |
| `docker.pull` | Execute | ✓ | `POST /images/create?fromImage&tag&platform` |
| `docker.push` | Execute | ✓ | `POST /images/{name}/push?tag` |
| `docker.exec` | Execute | ✓（双向） | `POST /containers/{id}/exec` → `POST /exec/{id}/start`（hijack）|
| `docker.images` | Read | ✗ | `GET /images/json?all` |
| `docker.volumes` | Read | ✗ | `GET /volumes` |
| `docker.networks` | Read | ✗ | `GET /networks` |

### 12.2 `docker.rm`（不可逆）

参数：

```json
{ "id": "<container>", "force": false, "volumes": false }
```

护栏：

- `Permission=Dangerous` → Core 的 `AllowConfirmer` 中间件会拒绝 `Confirmed=false`
- 插件层再做一次防御：`req.Confirmed == false` 时返回 `CONFIRMATION_REQUIRED`，即便 middleware 被绕过也不动 Engine
- 只透传 `force` / `v`；**不暴露** `link=` 参数（误伤成本高）

### 12.3 `docker.pull` / `docker.push`（流式 progress）

参数：

```json
// pull
{ "from_image": "nginx", "tag": "1.25", "platform": "linux/amd64",
  "auth": { "username":"u", "password":"p", "serveraddress":"registry.example.com" } }

// push
{ "image": "registry.example.com/team/app", "tag": "v1",
  "auth": { "username":"u", "password":"p" } }
```

协议：

- 请求头 `X-Registry-Auth = base64.URLEncoding(json(auth))`
  - `auth=nil` 时插件自动填 `base64("{}")` —— Engine 硬要求头字段存在
  - `identitytoken` 用于短期令牌（ECR / GCR），与 username/password 二选一
- Engine 响应体是 chunked JSON lines：`{"status":"...","progressDetail":{...},"id":"..."}` / `{"errorDetail":{...},"error":"..."}`
- 插件按行 `json.Decode` → 每行 `s.Event(line)`；命中 `error` / `errorDetail` 立即返回 `sdk.Error`（不调 Finish）
- 错误分类：从错误字符串启发式判断
  - `unauthorized` / `authentication required` → `DOCKER_UNAUTHORIZED`
  - `not found` / `manifest unknown` → `DOCKER_NOT_FOUND`
  - `denied` → `DOCKER_FORBIDDEN`
  - 其它 → `DOCKER_REGISTRY_ERROR`

### 12.4 `docker.exec`（双向流）

参数：

```json
{
  "id": "<container>",
  "cmd": ["sh","-lc","top -b -n 1"],
  "user": "",
  "working_dir": "",
  "env": ["KEY=VAL"],
  "tty": true,
  "attach_stdin": true
}
```

三步走：

1. **create**：`POST /containers/{id}/exec` → 拿 `exec_id`
2. **start**：手工发送 HTTP `POST /exec/{id}/start` + `Upgrade: tcp` → 拿到 hijacked `net.Conn`
   - 无法用 `http.Client.Do`，因为 `Response.Body` 只读；插件在 `client.go: dialHijack` 里手写请求
   - MVP **不支持 TLS hijack**（TLS 场景需在 raw conn 之上再 handshake，后续再补）
3. **run**：
   - Server → Client：`tty=false` 走 8 字节 mux 帧（复用 `pumpMux`）；`tty=true` 原始字节透传
   - Client → Server：`s.Recv()` 上收到 `*sdk.Stdin` 直接 `netConn.Write`
   - `*sdk.Signal(SignalWinch)` → `POST /exec/{id}/resize?h=&w=` （fire-and-forget）
   - `SignalCancel/Int/Term/Kill` → cancel ctx → 关闭 conn
4. **退出码**：`GET /exec/{id}/json`.ExitCode → 作为 `Finish` 的 `exitCode` 与 `execResult.ExitCode`

安全边界：

- `attach_stdin=false` 时收到 `Stdin` 直接丢弃（不算错误）
- winch payload 校验：`rows>0 && cols>0` 才 resize
- resize 失败绝不影响主流

### 12.5 未纳入本阶段（v0.4 承接）

- exec 场景的 TLS hijack
- Detach key 转义（Ctrl-P Ctrl-Q）
- `docker.kill`（`POST /containers/{id}/kill?signal=`）
- `docker.create` / `docker.build` / `docker.tag`
- 镜像 / 卷 / 网络 / Compose 视图（Dashboard 侧）
- 容器 exec 交互式终端 UI（复用 xterm.js）

### 12.6 Dashboard 侧的补齐（v0.3 第三阶段 UI）

Dashboard 直接消费上面新增的插件命令：

**新增 Tab（顶部切换）**：

| Tab | 数据源 | 说明 |
|---|---|---|
| Containers | `docker.list` | 保留第二阶段 UI |
| Images | `docker.images` | 新增：只读；显示 repo:tag / short id / size / created |
| Volumes | `docker.volumes` | 新增：只读；显示 name / driver / scope / mountpoint / created |
| Networks | `docker.networks` | 新增：只读；显示 name / driver / scope（含 internal/attachable 徽标）/ subnets |

**新增 Actions（容器行）**：

- `Exec`：仅在 `running/restarting` 时可用；打开 `DockerExecDrawer`（xterm.js）
  - 用户在顶部输入框自定义命令（默认 `sh`），点 Start 建立 `docker.exec` 双向流
  - 首帧 winch 会把当前 xterm 尺寸同步给远端，避免第一行错位
  - 关闭抽屉自动 `DockerExecClose`
  - 事件契约：`docker:exec:<sid>:{stdout,stderr,event,exit}`
- `Remove`（红色）：打开二次确认弹窗
  - Force：勾选 → 请求 `force=true`（运行中容器自动预勾）
  - Also remove anonymous volumes → `volumes=true`
  - 应用层再判 `Confirmed=true` + Command Engine `Dangerous` 中间件 = 双重护栏

**新增 Wails 后端 API**：

| 方法 | 说明 |
|---|---|
| `App.DockerRm(targetID, {container, force, volumes, confirmed})` | 前端必须显式 `confirmed=true` |
| `App.DockerImages(targetID, {all})` | 只读 |
| `App.DockerVolumes(targetID)` | 只读 |
| `App.DockerNetworks(targetID)` | 只读 |
| `App.DockerExecOpen(targetID, {container, cmd, tty, attach_stdin, rows, cols, ...})` → sessionID | 会话开启 |
| `App.DockerExecWrite(sid, base64)` | 前端 → stdin |
| `App.DockerExecResize(sid, rows, cols)` | TTY 尺寸 |
| `App.DockerExecClose(sid)` | 幂等 |

**未纳入 Dashboard**：

- Compose 视图（编辑 / up / down）— 单独 RFC
- Image rm / volume prune / network rm — 与 `docker.rm` 语义不同（需要独立命令），v0.4
- Container `create` 表单（大量字段，需要专门抽屉设计），v0.4

## 13. 待讨论

- [ ] `docker.list` 是否要暴露完整 `filters` 语法（当前只支持 `labels`）
- [ ] `docker.inspect` 是否要在 Plugin 侧做字段裁剪，还是保持"原样透传 + UI 决定展示"
- [ ] `docker.logs` `tty` 是否要自动 inspect（省掉调用方声明的复杂度）
- [ ] 是否复用官方 `github.com/docker/docker` SDK；当前用 net/http 手写以避免上百 MiB 依赖
