// Package main 实现 mow-plugin-ssh —— 官方 SSH 插件。
//
// v0.1 交付：
//   - SSH 会话池（*ssh.Client 复用 + 引用计数 + 空闲 GC）
//   - ssh.exec：真正的远端命令执行（一次性、非交互）
//   - ssh.ping：保留，供 grpcbridge 端到端 sanity check
//
// 底层协议：golang.org/x/crypto/ssh
// 凭据来源：sdk.Connection.Credentials （由 core/connection.Manager 下发）
package main

import (
	"context"
	"path/filepath"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
)

// SSHPlugin 是 MOW 官方 SSH 插件。
type SSHPlugin struct {
	pool    *SessionPool
	dataDir string // Init 时写入；Command 内读取以推导默认 known_hosts 路径
}

func newSSHPlugin() *SSHPlugin {
	return &SSHPlugin{pool: NewSessionPool(SessionPoolOptions{})}
}

func (p *SSHPlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{
		ID:              "ssh",
		Name:            "SSH",
		Version:         "0.1.0",
		Author:          "mow",
		Description:     "SSH connection pool + command execution",
		CoreVersion:     ">=0.1.0,<0.2.0",
		ConnectionTypes: []string{"ssh"},
	}
}

func (p *SSHPlugin) Init(ctx context.Context, req sdk.InitRequest) error {
	p.dataDir = req.DataDir
	return nil
}
func (p *SSHPlugin) Shutdown(ctx context.Context) error               { p.pool.Close(); return nil }
func (p *SSHPlugin) HealthCheck(ctx context.Context) sdk.HealthStatus { return sdk.StatusHealthy }
func (p *SSHPlugin) Commands() []sdk.CommandHandler {
	return []sdk.CommandHandler{
		&pingCmd{},
		&execCmd{pool: p.pool, plugin: p},
	}
}

// defaultKnownHostsPath 返回插件级默认 known_hosts 路径。
// 一般为 <plugin data dir>/known_hosts；未提供 DataDir 时返回空串。
func (p *SSHPlugin) defaultKnownHostsPath() string {
	if p.dataDir == "" {
		return ""
	}
	return filepath.Join(p.dataDir, "known_hosts")
}

func main() {
	pluginserve.Serve(newSSHPlugin())
}
