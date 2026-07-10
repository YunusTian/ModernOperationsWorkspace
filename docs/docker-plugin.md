# RFC: Docker Plugin

- 状态：Draft（v0.3 第一阶段落地）
- 版本：v0.3
- 更新日期：2026-07-10
- 相关章节：[roadmap.md § v0.3](./roadmap.md#v03--docker-plugin--docker-dashboard--下一版) · [plugin-system.md](./plugin-system.md) · [permission.md](./permission.md)

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

## 10. 未纳入本阶段（v0.3 第二阶段承接）

- `docker.rm` / `docker.kill` / `docker.pull` / `docker.push` / `docker.exec`
- Docker Dashboard 前端
- Docker Recipe / Workflow
- Workflow 与 Docker 联动的 `on_failure` / `retry` / `parallel` / `when`
- `ssh://` 隧道模式的 Docker Host

## 11. 待讨论

- [ ] `docker.list` 是否要暴露完整 `filters` 语法（当前只支持 `labels`）
- [ ] `docker.inspect` 是否要在 Plugin 侧做字段裁剪，还是保持"原样透传 + UI 决定展示"
- [ ] `docker.logs` `tty` 是否要自动 inspect（省掉调用方声明的复杂度）
- [ ] 是否复用官方 `github.com/docker/docker` SDK；当前用 net/http 手写以避免上百 MiB 依赖
