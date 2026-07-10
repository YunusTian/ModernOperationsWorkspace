// Package main 实现 mow-plugin-docker —— 官方 Docker 插件（v0.3 第一阶段 MVP）。
//
// v0.3 第一阶段交付：
//   - docker.list：容器列表（含 all=true 过滤）
//   - docker.inspect：容器详情
//   - docker.start / docker.stop / docker.restart：容器生命周期
//   - docker.logs：流式日志（follow / tail / stdout / stderr）
//
// 高风险命令（docker.rm / docker.push / docker.exec 等）留待第二批。
//
// 传输协议：Docker Engine HTTP API（unix / tcp / tcp+TLS）
// 凭据来源：sdk.Connection.Credentials（由 core/connection.Manager 下发）
package main

import (
	"context"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
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
		Version:         "0.1.0",
		Author:          "mow",
		Description:     "Docker container lifecycle and log streaming",
		CoreVersion:     ">=0.1.0,<0.2.0",
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
	}
}

func main() {
	pluginserve.Serve(newDockerPlugin())
}
