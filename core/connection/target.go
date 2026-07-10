package connection

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type 是连接类型枚举（与 sdk.Metadata.ConnectionTypes 对齐）。
type Type string

const (
	TypeSSH    Type = "ssh"
	TypeDocker Type = "docker"
	TypeHTTP   Type = "http"
	TypeWS     Type = "ws"
	TypePVE    Type = "pve"
)

// Target 描述一个可连接的目标（例如一台 SSH 主机）。
//
// Target 只保存"连去哪里 / 用谁的身份"；不保存运行时会话。
// 凭据以密文形态放在 EncryptedCredentials 中，运行时由 Manager 解密后
// 通过 sdk.Connection.Credentials 明文快照下发给插件（仅进程内存在）。
type Target struct {
	// ID 全局唯一标识；由调用方或 Manager 生成。
	ID string `json:"id"`

	// Type 连接类型：ssh / docker / ...
	Type Type `json:"type"`

	// Name 人类可读名字（UI 展示用）。
	Name string `json:"name,omitempty"`

	// Host / Port / User 等元信息，供插件与 UI 使用。
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	User string `json:"user,omitempty"`

	// Tags 标签，供 UI 过滤与 Recipe 变量替换。
	Tags map[string]string `json:"tags,omitempty"`

	// EncryptedCredentials 是凭据的加密后字节。
	// 具体明文结构由 Type 决定，例如 SSHCredentials。
	// 不加密时（例如无口令的密钥代理场景）也允许为空。
	EncryptedCredentials []byte `json:"encrypted_credentials,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate 校验 Target 的基本字段。
func (t *Target) Validate() error {
	if t == nil {
		return errors.New("connection: target is nil")
	}
	if strings.TrimSpace(t.ID) == "" {
		return errors.New("connection: target.id is required")
	}
	if t.Type == "" {
		return errors.New("connection: target.type is required")
	}
	if t.Type == TypeSSH {
		if t.Host == "" {
			return errors.New("connection: ssh target requires host")
		}
		if t.User == "" {
			return errors.New("connection: ssh target requires user")
		}
	}
	// Docker target 的 host 由 DockerCredentials.Host 提供（可能是 unix:// 无 host 字段），
	// 因此这里不强制 Target.Host；具体校验在 DockerCredentials.Validate 中完成。
	return nil
}

// MetadataMap 生成下发给 sdk.Connection.Metadata 的字符串键值表。
// 敏感字段（凭据）不会写入。
func (t *Target) MetadataMap() map[string]string {
	m := map[string]string{
		"target_id": t.ID,
		"type":      string(t.Type),
	}
	if t.Name != "" {
		m["name"] = t.Name
	}
	if t.Host != "" {
		m["host"] = t.Host
	}
	if t.Port > 0 {
		m["port"] = fmt.Sprintf("%d", t.Port)
	}
	if t.User != "" {
		m["user"] = t.User
	}
	for k, v := range t.Tags {
		m["tag."+k] = v
	}
	return m
}

// -----------------------------------------------------------------------------
// SSH Credentials
// -----------------------------------------------------------------------------

// SSHAuthMethod 是 SSH 认证方式。
type SSHAuthMethod string

const (
	SSHAuthPassword   SSHAuthMethod = "password"
	SSHAuthPrivateKey SSHAuthMethod = "privatekey"
	SSHAuthAgent      SSHAuthMethod = "agent"
)

// SSHCredentials 是 SSH 目标的凭据明文结构。
//
// - 存储：Manager 会用 AES-256-GCM 加密到 Target.EncryptedCredentials
// - 下发：Manager.Open 时解密后作为 sdk.Connection.Credentials（JSON）传给插件
//
// 敏感字段（Password / PrivateKey / Passphrase）不得写入日志。
type SSHCredentials struct {
	Method SSHAuthMethod `json:"method"`

	// Password：SSHAuthPassword 时必填
	Password string `json:"password,omitempty"`

	// PrivateKey：SSHAuthPrivateKey 时必填（PEM 编码）
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`

	// KnownHostsMode：strict / accept-new / insecure-ignore
	//   - strict（默认）：必须命中 KnownHostsPath 中的记录
	//   - accept-new：未知主机首次接受并写入
	//   - insecure-ignore：跳过主机密钥校验（仅测试环境）
	KnownHostsMode string `json:"known_hosts_mode,omitempty"`
	KnownHostsPath string `json:"known_hosts_path,omitempty"`
}

// Validate 校验 SSHCredentials 的基本字段。
func (c *SSHCredentials) Validate() error {
	if c == nil {
		return errors.New("ssh credentials: nil")
	}
	switch c.Method {
	case SSHAuthPassword:
		if c.Password == "" {
			return errors.New("ssh credentials: password required")
		}
	case SSHAuthPrivateKey:
		if c.PrivateKey == "" {
			return errors.New("ssh credentials: private_key required")
		}
	case SSHAuthAgent:
		// no-op
	default:
		return fmt.Errorf("ssh credentials: unknown method %q", c.Method)
	}
	return nil
}

// MarshalCredentials 序列化任意凭据结构到 JSON。
// 独立成函数便于未来接入 Docker / PVE 等其他类型。
func MarshalCredentials(v any) ([]byte, error) {
	return json.Marshal(v)
}
