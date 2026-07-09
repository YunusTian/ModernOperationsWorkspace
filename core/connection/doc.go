// Package connection 管理所有连接（SSH / Docker / HTTP / WS）的目标（Target）
// 定义、凭据加密存储与会话下发。
//
// v0.1 只交付 SSH：
//   - Target CRUD（内存 + JSON 文件持久化）
//   - Credentials 加密存储（AES-256-GCM，主密钥落盘于 DataDir）
//   - Manager.Open 返回 sdk.Connection 供插件消费
//
// 真正的底层协议（crypto/ssh、docker/client 等）在插件内建立；
// Connection Manager 只负责"给谁 / 用什么凭据 / 在哪"。
//
// 禁止 AI 直接访问本包。详见 docs/connection-manager.md。
package connection
