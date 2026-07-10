package connection

import (
	"errors"
	"fmt"
	"strings"
)

// -----------------------------------------------------------------------------
// Docker Credentials
// -----------------------------------------------------------------------------
//
// Docker Engine 有三种常见暴露方式，MOW 一次性覆盖：
//
//   - Unix socket：本机 daemon，Host = "unix:///var/run/docker.sock"
//   - TCP：       远端 daemon，Host = "tcp://host:2375"（不建议裸跑）
//   - TCP + TLS： 生产远端，   Host = "tcp://host:2376" + TLSCA/TLSCert/TLSKey
//
// 未来 SSH 隧道模式（DOCKER_HOST=ssh://user@host）留给下一批。
//
// 敏感字段（TLSKey / TLSKeyPassphrase）不得写入日志。

// DockerCredentials 是 Docker Target 的凭据明文结构。
//
//   - 存储：Manager 会用 AES-256-GCM 加密到 Target.EncryptedCredentials
//   - 下发：Manager.Open 时解密后作为 sdk.Connection.Credentials（JSON）传给插件
type DockerCredentials struct {
	// Host 是 Docker Engine 的访问端点：
	//   - unix:///var/run/docker.sock
	//   - tcp://host:2375
	//   - tcp://host:2376（TLS）
	// 必填。
	Host string `json:"host"`

	// APIVersion 是可选的 Docker API 版本号（例："1.44"）；空则由服务端协商。
	APIVersion string `json:"api_version,omitempty"`

	// TLSVerify 为 true 时启用 TLS 并强制校验服务端证书。
	TLSVerify bool `json:"tls_verify,omitempty"`

	// TLSCA / TLSCert / TLSKey 是 PEM 编码的证书 / 私钥（内联）。
	// 三者要么都提供，要么都不提供。
	TLSCA   string `json:"tls_ca,omitempty"`
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`
}

// Validate 校验 DockerCredentials 的基本字段。
func (c *DockerCredentials) Validate() error {
	if c == nil {
		return errors.New("docker credentials: nil")
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("docker credentials: host required")
	}
	// 常见格式白名单
	switch {
	case strings.HasPrefix(c.Host, "unix://"):
		// ok
	case strings.HasPrefix(c.Host, "tcp://"):
		// ok
	case strings.HasPrefix(c.Host, "npipe://"):
		// Windows named pipe
	default:
		return fmt.Errorf("docker credentials: unsupported host scheme %q (want unix:// / tcp:// / npipe://)", c.Host)
	}
	// TLS 材料完整性
	hasAny := c.TLSCA != "" || c.TLSCert != "" || c.TLSKey != ""
	hasAll := c.TLSCA != "" && c.TLSCert != "" && c.TLSKey != ""
	if hasAny && !hasAll {
		return errors.New("docker credentials: tls_ca / tls_cert / tls_key must be provided together")
	}
	if c.TLSVerify && !hasAll {
		return errors.New("docker credentials: tls_verify=true requires tls_ca / tls_cert / tls_key")
	}
	return nil
}
