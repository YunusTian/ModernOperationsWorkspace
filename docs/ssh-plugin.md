# SSH 插件 v0.1 交付清单

- 状态：已交付
- 版本：v0.1
- 更新日期：2026-07-10
- 相关章节：[Plugin System](./plugin-system.md)、[Roadmap](./roadmap.md#v01--优秀的-ssh-客户端)

---

## 1. 交付概览

| 维度 | 数据 |
|------|------|
| 插件 ID | `ssh` |
| 版本 | `0.1.0` |
| 语言 | Go |
| 代码量 | 6 文件 / 1436 行 |
| Command 数量 | 6 |
| Recipe 数量 | 1（`system.cpu`） |
| 鉴权方式 | 3（密码 / 私钥 / Agent） |
| 单元测试 | 12 PASS |
| E2E 测试 | 10 PASS |
| 手动验收 | 42 项 OK |

---

## 2. 文件清单

| 文件 | 行数 | 职责 |
|------|------|------|
| `main.go` | 70 | 插件入口、Command 注册、known_hosts 默认路径 |
| `commands.go` | 150 | `ssh.exec` 一次性命令执行、参数校验、会话管理 |
| `shell.go` | 247 | `ssh.shell` 交互式 PTY（流式 Command）、SIGWINCH/SIGINT |
| `sftp.go` | 468 | `sftp.list` / `sftp.upload` / `sftp.download` |
| `pool.go` | 429 | SSH 连接池（复用 + 引用计数 + 空闲 GC） |
| `credentials.go` | 72 | SSH 凭据解析（密码 / 私钥 / Agent）、known_hosts 模式 |

---

## 3. Command 矩阵

### 3.1 ssh.exec — 远端命令执行

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `cmd` | string | 是 | 远端 shell 命令行 |
| `stdin` | string | 否 | 可选标准输入 |

| 输出 | 类型 | 说明 |
|------|------|------|
| `stdout` | string | 标准输出 |
| `stderr` | string | 标准错误 |
| `exit_code` | int | 退出码（非零不报错，透传） |

| 权限 | ConnectionType |
|------|---------------|
| Execute | ssh |

### 3.2 ssh.shell — 交互式 PTY（流式）

| 参数 | 类型 | 必填 | 默认值 |
|------|------|------|--------|
| `term` | string | 否 | `xterm-256color` |
| `rows` | int | 否 | `24` |
| `cols` | int | 否 | `80` |
| `env` | map | 否 | — |

| 信号 | 说明 |
|------|------|
| `SignalWinch` | 窗口大小变更 |
| `SignalInt` | `SIGINT` |
| `SignalTerm` | `SIGTERM` |
| `SignalKill` / `SignalCancel` | `SIGKILL` + 关闭 session |

| 权限 | ConnectionType | Streaming |
|------|---------------|-----------|
| Execute | ssh | 是 |

### 3.3 ssh.ping — 端到端连通性检测

| 输出 | 说明 |
|------|------|
| `{"pong":"ok"}` | 不需 Connection，grpcbridge sanity check |

| 权限 |
|------|
| Read |

### 3.4 sftp.list — 列出远端目录

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `path` | string | 是 | 远端目录绝对路径 |

| 输出 | 类型 | 说明 |
|------|------|------|
| `path` | string | 查询路径 |
| `entries[]` | array | 文件列表（name/size/mode/mod_time/is_dir/is_link） |

| 权限 | ConnectionType |
|------|---------------|
| Read | ssh |

### 3.5 sftp.upload — 上传文件

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `remote_path` | string | 是 | 远端目标路径 |
| `local_path` | string | 二选一 | 插件进程可见的本地文件路径 |
| `content_b64` | string | 二选一 | base64 内联内容（适用小文件 / Recipe） |
| `mode` | string | 否 | 八进制权限位，默认 `0644` |
| `mkdir_all` | bool | 否 | 递归创建父目录 |
| `overwrite` | bool | 否 | 默认 `true`；`false` 时遇已存在文件报 `SFTP_EXISTS` |

| 输出 | 类型 | 说明 |
|------|------|------|
| `remote_path` | string | 写入的远端路径 |
| `bytes_sent` | int64 | 已传输字节数 |

| 权限 | ConnectionType |
|------|---------------|
| Write | ssh |

### 3.6 sftp.download — 下载文件

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `remote_path` | string | 是 | 远端文件路径 |
| `local_path` | string | 否 | 写出到本机路径；空则以 base64 内联返回 |
| `mode` | string | 否 | 本地文件权限，默认 `0644` |

| 输出 | 类型 | 说明 |
|------|------|------|
| `remote_path` | string | 远端文件路径 |
| `local_path` | string | 写出路径（仅 local_path 非空时返回） |
| `bytes_received` | int64 | 已接收字节数 |
| `content_b64` | string | base64 内容（仅 local_path 为空且 ≤1MiB 时返回） |

| 限制 | 说明 |
|------|------|
| 内联模式上限 | 1 MiB，超出返回 `SFTP_TOO_LARGE_FOR_INLINE` |

| 权限 | ConnectionType |
|------|---------------|
| Read | ssh |

---

## 4. 鉴权方式

| 方式 | 凭据字段 | 说明 |
|------|----------|------|
| 密码 | `method: "password"` + `password` | 用户名+密码 |
| 私钥 | `method: "privatekey"` + `private_key` | PEM 格式，支持 Ed25519 / RSA |
| 加密私钥 | `method: "privatekey"` + `private_key` + `passphrase` | 带口令的 PEM 私钥 |

| known_hosts 模式 | 说明 |
|------------------|------|
| `strict` | 默认模式，HostKey 必须在 known_hosts 中匹配 |
| `accept-new` | 首次自动追写 known_hosts，后续走 strict |
| `insecure-ignore` | 跳过 HostKey 校验（仅测试环境） |

---

## 5. 连接池

| 特性 | 说明 |
|------|------|
| 复用策略 | `(host, port, user)` + 凭据 hash 作为 key |
| 引用计数 | Acquire/Release 计数，引用为 0 时可被 GC |
| 空闲 GC | 后台定时扫描，超时关闭空闲连接 |
| 异常处理 | NewSession 失败自动 Evict，调用方可重试 |
| 并发安全 | `sync.Mutex` + `atomic` |

---

## 6. 测试覆盖

### 6.1 单元测试（12 PASS）

| 测试用例 | 覆盖 |
|----------|------|
| `TestSessionPool_ReusesClient` | 同 key 复用 client |
| `TestSessionPool_Stats` | 统计信息正确 |
| `TestSessionPool_ConcurrentAcquireRelease` | 并发 Acquire/Release 无 race |
| `TestSessionPool_EvictConcurrent` | 并发 Evict 安全 |
| `TestResolveTarget_HappyPath` | Target 解析正确 |
| `TestBuildAuthMethods_Password` | 密码鉴权构造 |
| `TestBuildAuthMethods_Empty` | 空凭据处理 |
| `TestBuildAuthMethods_UnknownMethod` | 未知鉴权方式错误 |
| `TestBuildHostKeyCallback_Insecure` | insecure-ignore 模式 |
| `TestBuildHostKeyCallback_StrictRequiresPath` | strict 需 known_hosts 路径 |
| `TestBuildHostKeyCallback_DefaultIsStrict` | 默认 strict 模式 |
| `TestBuildHostKeyCallback_AcceptNewRequiresPath` | accept-new 需路径 |

### 6.2 E2E 测试（10 PASS）

| 测试用例 | 场景 |
|----------|------|
| `TestSSHExec_EndToEnd` | 密码鉴权 + exec → stdout/stderr/exit_code |
| `TestSSHExec_ExitCodeNonZero` | 远端 exit≠0 透传退出码 |
| `TestSSHExec_Stdin` | params.stdin 输入 → 远端回读 |
| `TestSSHExec_ContextCancel` | 超时取消 → CANCELED |
| `TestSessionPool_Reuse` | 两次 exec 仅一次 TCP Accept |
| `TestSSHExec_PublicKey` | Ed25519 私钥鉴权 |
| `TestSSHExec_KeyPassphrase` | 加密私钥 passphrase 解密鉴权 |
| `TestSSHExec_MissingCmd` | 缺 cmd → PARAM_INVALID + 短路 |
| `TestSSHExec_AcceptNewAppendsKnownHosts` | accept-new 首次追写 → 第二次命中不重复 |
| `TestRecipe_SystemCPU_EndToEnd` | Recipe Runner ↔ Engine ↔ Plugin 全链路 |

### 6.3 手动验收（42 项 OK）

| 模块 | 覆盖项数 |
|------|----------|
| SSH exec（错误密码/主机不可达/strict 不匹配/evict/并发） | 5 |
| SFTP list（根目录/子目录/空目录/路径不存在/参数校验/文件属性/符号链接） | 7 |
| SFTP upload（content_b64/local_path/mkdir_all/overwrite/mode/参数校验/cancel/bytes_sent） | 12 |
| SFTP download（内联/local_path/路径不存在/超1MiB/参数校验/mode/cancel） | 7 |
| SSH ping | 1 |
| SSH shell（启动/stdin/stdout/sigwinch/sigint/默认参数/env/exit_code） | 8 |
| Recipe 自定义/YAML 定义/步骤失败 | 2 |

---

## 7. 错误码

| 错误码 | 出现位置 | 说明 |
|--------|----------|------|
| `PARAM_INVALID` | exec / sftp.* | 参数缺失或非法 |
| `CONNECTION_INVALID` | exec / shell / sftp.* | Connection 不可用 |
| `SSH_DIAL_FAILED` | exec / shell / sftp.* | 连接建立失败（retryable） |
| `SSH_SESSION_FAILED` | exec / shell | Session 创建失败（retryable） |
| `SSH_EXEC_FAILED` | exec | 命令执行协议错误 |
| `SSH_PTY_FAILED` | shell | PTY 请求失败 |
| `SSH_SHELL_FAILED` | shell | Shell 启动失败 |
| `CANCELED` | exec / shell / sftp.* | Context 取消 |
| `SFTP_OPEN_FAILED` | sftp.* | SFTP 客户端创建失败（retryable） |
| `SFTP_LIST_FAILED` | sftp.list | ReadDir 失败 |
| `SFTP_CREATE_FAILED` | sftp.upload | 创建远端文件失败 |
| `SFTP_WRITE_FAILED` | sftp.upload | 写入远端失败 |
| `SFTP_EXISTS` | sftp.upload | overwrite=false 且目标已存在 |
| `SFTP_MKDIR_FAILED` | sftp.upload | mkdir_all 递归创建失败 |
| `SFTP_OPEN_REMOTE_FAILED` | sftp.download | 打开远端文件失败 |
| `SFTP_READ_FAILED` | sftp.download | 读取远端失败 |
| `SFTP_TOO_LARGE_FOR_INLINE` | sftp.download | 内联模式文件超 1MiB |
| `SFTP_STAT_FAILED` | sftp.download | Stat 远端文件失败 |
| `LOCAL_OPEN_FAILED` | sftp.upload | 本地文件打开失败 |
| `LOCAL_CREATE_FAILED` | sftp.download | 本地文件创建失败 |
| `ENCODE_FAILED` | exec / sftp.* | 结果序列化失败 |

---

## 8. 技术栈

| 组件 | 选型 |
|------|------|
| SSH 协议 | `golang.org/x/crypto/ssh` |
| SFTP | `github.com/pkg/sftp` |
| 插件框架 | `hashicorp/go-plugin`（gRPC 子进程） |
| 连接池 | 自研（引用计数 + 空闲 GC） |
| 测试 SSH Server | `github.com/gliderlabs/ssh` |

---

## 9. 验收结论

| 维度 | 结果 |
|------|------|
| 自动化单元测试 | 12/12 PASS |
| 自动化 E2E 测试 | 10/10 PASS |
| 手动功能验收 | 42/42 OK |
| 代码规范 | 通过 golangci-lint |
| 并发安全 | 通过 `-race` |
| 安全扫描 | 通过 gosec |
| 跨平台 | Windows / Linux 双平台通过 |

**v0.1 SSH 插件交付验收通过，无阻塞项。**
