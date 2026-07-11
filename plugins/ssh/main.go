// Package main 实现 mow-plugin-ssh —— 官方 SSH 插件。
//
// v0.1 交付：
//   - ssh.exec：远端命令执行（一次性、非交互）
//   - ssh.shell：交互式 PTY 会话（流式双向）
//   - sftp.list / sftp.upload / sftp.download：SFTP 文件操作
//   - ssh.ping：gRPC bridge 连通性检测
//   - SSH 会话池（*ssh.Client 复用 + 引用计数 + 空闲 GC）
//
// 底层协议：golang.org/x/crypto/ssh
// 凭据来源：sdk.Connection.Credentials （由 core/connection.Manager 下发）
package main

import (
	"context"
	"path/filepath"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
	"github.com/mow/mow/sdk/version"
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
		Version:         version.Version,
		Author:          "mow",
		Description:     "SSH remote execution, interactive shell, and SFTP file transfer",
		CoreVersion:     ">=0.4.0,<0.5.0",
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
		&shellCmd{pool: p.pool, plugin: p},
		&sftpListCmd{pool: p.pool, plugin: p},
		&sftpUploadCmd{pool: p.pool, plugin: p},
		&sftpDownloadCmd{pool: p.pool, plugin: p},
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
