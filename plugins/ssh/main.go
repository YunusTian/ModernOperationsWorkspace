// Command mow-plugin-ssh 是 MOW 的官方 SSH 插件。
// v0.1 只提供一个 stub Command: ssh.ping，用于验证 grpcbridge 端到端可通。
// 真正的 ssh.exec / ssh.upload / ssh.download 将在 Connection Manager 就绪后实现。
package main

import (
	"context"
	"encoding/json"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
)

// -----------------------------------------------------------------------------
// Plugin
// -----------------------------------------------------------------------------

type SSHPlugin struct{}

func (p *SSHPlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{
		ID:              "ssh",
		Name:            "SSH",
		Version:         "0.1.0",
		Author:          "mow",
		Description:     "SSH connection & command execution (v0.1 stub)",
		CoreVersion:     ">=0.1.0,<0.2.0",
		ConnectionTypes: []string{"ssh"},
	}
}

func (p *SSHPlugin) Init(context.Context, sdk.InitRequest) error   { return nil }
func (p *SSHPlugin) Shutdown(context.Context) error                { return nil }
func (p *SSHPlugin) HealthCheck(context.Context) sdk.HealthStatus  { return sdk.StatusHealthy }
func (p *SSHPlugin) Commands() []sdk.CommandHandler                { return []sdk.CommandHandler{&pingCmd{}} }

// -----------------------------------------------------------------------------
// ssh.ping —— 端到端验证用
// -----------------------------------------------------------------------------

type pingCmd struct{}

func (c *pingCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "ping",
		Description: "returns pong; used for grpcbridge sanity check",
		Permission:  sdk.PermRead,
	}
}

func (c *pingCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	data, _ := json.Marshal(map[string]string{"pong": "ok"})
	return &sdk.ExecuteResponse{Data: data}, nil
}

func (c *pingCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

// -----------------------------------------------------------------------------
// Entry
// -----------------------------------------------------------------------------

func main() {
	pluginserve.Serve(&SSHPlugin{})
}
