# RFC: Connection Manager

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 4.1

---

## 1. 职责

- 建立连接 / 保持连接 / 自动重连
- 会话缓存与复用
- 密钥、凭据、加密存储
- **禁止 AI 直接访问 Connection Manager**

## 2. 支持的连接类型

| 类型 | 描述 |
| --- | --- |
| SSH | 密码 / 密钥 / Agent |
| Docker | 本地 socket / TCP / TLS |
| PVE API | Token / Ticket |
| HTTP | 通用 HTTP 客户端 |
| WebSocket | 长连接 / 流式推送 |

## 3. 抽象接口（待细化）

```text
IConnection
├── Id
├── Type            # ssh / docker / http / ws / ...
├── Open()          # 建立连接
├── Close()
├── IsAlive
├── Reconnect()
└── Metadata        # host / port / user / tags
```

## 4. 凭据存储

- 存储层：加密后落盘（平台密钥库优先，退化到 AES + 主口令）
- 生命周期：进程内解密；不落明文日志
- 分类：密码 / 私钥 / Token / TLS 证书

## 5. 技术选型（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| SSH | **`golang.org/x/crypto/ssh`** + `pkg/sftp` | Go 官方 + 工业级 SFTP |
| Docker | **`github.com/docker/docker/client`** | Docker 官方 SDK |
| HTTP | Go 标准库 `net/http` | |
| WebSocket | `nhooyr.io/websocket` | 现代、简洁 |
| 凭据存储 | **`zalando/go-keyring`**（平台密钥库） + 回退 AES-256-GCM | Win Credential Manager / macOS Keychain / libsecret |
| 会话池 | Core 自研（基于 `sync.Map` + 引用计数） | |
| 重连策略 | 指数退避（`cenkalti/backoff/v4`） | |

## 6. 待讨论

- [ ] Session Pool 上限与 LRU 淘汰策略
- [ ] 最大重试次数与告警门槛
- [ ] 多跳（Jump Host）如何抽象为一等公民
- [ ] Linux 无桌面环境时的凭据存储回退策略（文件加密 + 主口令）
