// Package main 实现 mow-plugin-docker —— 官方 Docker 插件。
//
// v0.3 第一阶段：
//   - docker.list / docker.inspect
//   - docker.start / docker.stop / docker.restart
//   - docker.logs（流式）
//
// v0.3 第三阶段（本轮）：
//   - docker.rm（Dangerous — 需 Confirmed=true）
//   - docker.pull / docker.push（流式 progress events，支持 X-Registry-Auth）
//   - docker.exec（双向流：create → start hijack → mux / raw → exit code）
//
// 传输协议：Docker Engine HTTP API（unix / tcp / tcp+TLS；exec 场景仅 unix / tcp 明文）
// 凭据来源：sdk.Connection.Credentials（由 core/connection.Manager 下发）
package main

import (
	"context"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
	"github.com/mow/mow/sdk/version"
)

// DockerPlugin 是 MOW 官方 Docker 插件。
type DockerPlugin struct {
	dataDir string
}

func newDockerPlugin() *DockerPlugin { return &DockerPlugin{} }

func (p *DockerPlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{
		ID:              "docker",
		Name:            "Docker",
		Version:         version.Version,
		Author:          "mow",
		Description:     "Docker container lifecycle and log streaming",
		CoreVersion:     ">=0.4.0,<0.5.0",
		ConnectionTypes: []string{"docker"},
	}
}

func (p *DockerPlugin) Init(ctx context.Context, req sdk.InitRequest) error {
	p.dataDir = req.DataDir
	return nil
}

func (p *DockerPlugin) Shutdown(ctx context.Context) error { return nil }

func (p *DockerPlugin) HealthCheck(ctx context.Context) sdk.HealthStatus {
	return sdk.StatusHealthy
}

func (p *DockerPlugin) Commands() []sdk.CommandHandler {
	return []sdk.CommandHandler{
		&listCmd{},
		&inspectCmd{},
		&startCmd{},
		&stopCmd{},
		&restartCmd{},
		&logsCmd{},
		// v0.3 第三阶段
		&rmCmd{},
		&pullCmd{},
		&pushCmd{},
		&execCmd{},
		// Dashboard 第三阶段只读列表
		&imagesCmd{},
		&volumesCmd{},
		&networksCmd{},
	}
}

func main() {
	pluginserve.Serve(newDockerPlugin())
}
