// Package main 实现 mow-pve-plugin —— MOW 官方 Proxmox VE 只读参考插件（v0.5.2 P1）。
//
// 定位：
//   - 覆盖 cluster / node / QEMU vm / LXC 的只读列表 + start / stop / reboot
//   - 用途是「示范一个新插件如何走通 Manifest → settingsSchema → secret sidecar → CLI/Desktop UI」全链路
//   - 不做创建向导 / 存储迁移 / Dangerous 删除（延后到 v0.7）
//
// 传输协议：Proxmox VE REST API（HTTPS + `Authorization: PVEAPIToken=<id>=<secret>`）
// 凭据来源：Manifest.settingsSchema 里的 `endpoints[]`；`token_secret` 通过 v0.5.2 的
//   secret sidecar 存放到 <DataDir>/plugin-secrets/pve.json（0600），只在 Init 时合并回 Settings。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
	"github.com/mow/mow/sdk/version"
)

// PVEPlugin 是 MOW 官方 Proxmox VE 插件。
type PVEPlugin struct {
	dataDir string

	mu        sync.Mutex
	endpoints map[string]*endpoint // name -> resolved endpoint（含解析后的 secret）
	// defaultEndpoint 指向 endpoints[0].Name；命令未显式给 endpoint 时使用它。
	defaultEndpoint string
}

func newPVEPlugin() *PVEPlugin {
	return &PVEPlugin{endpoints: map[string]*endpoint{}}
}

func (p *PVEPlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{
		ID:              "pve",
		Name:            "Proxmox VE",
		Version:         version.Version,
		Author:          "mow",
		Description:     "Proxmox VE read-only reference plugin (cluster/node/vm/lxc list + start/stop/reboot)",
		CoreVersion:     ">=0.5.0,<0.6.0",
		ConnectionTypes: []string{"pve"},
	}
}

// Init 反序列化 settings，并把 token_secret_env 里的环境变量落成明文（内存）。
// 若 endpoints 为空，返回 nil（允许在 UI 引导用户填写前先安装并 enable）。
func (p *PVEPlugin) Init(_ context.Context, req sdk.InitRequest) error {
	p.dataDir = req.DataDir
	if len(req.Settings) == 0 {
		return nil
	}
	settings, err := parseSettings(req.Settings)
	if err != nil {
		return fmt.Errorf("pve: decode settings: %w", err)
	}
	resolved, defName, err := resolveEndpoints(settings.Endpoints, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("pve: invalid endpoints: %w", err)
	}
	p.mu.Lock()
	p.endpoints = resolved
	p.defaultEndpoint = defName
	p.mu.Unlock()
	return nil
}

func (p *PVEPlugin) Shutdown(_ context.Context) error { return nil }

// HealthCheck：只要有至少一个可用 endpoint 就视为 healthy。
// 我们不在这里发真实 HTTP 请求（避免启动即发外部调用），仅做配置层健康度评估。
func (p *PVEPlugin) HealthCheck(_ context.Context) sdk.HealthStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.endpoints) == 0 {
		return sdk.StatusDegraded
	}
	return sdk.StatusHealthy
}

func (p *PVEPlugin) Commands() []sdk.CommandHandler {
	return []sdk.CommandHandler{
		&clusterStatusCmd{p: p},
		&nodeListCmd{p: p},
		&vmListCmd{p: p},
		&vmStatusCmd{p: p},
		&lxcListCmd{p: p},
		&vmLifecycleCmd{p: p, action: "start"},
		&vmLifecycleCmd{p: p, action: "stop"},
		&vmLifecycleCmd{p: p, action: "reboot"},
		&lxcLifecycleCmd{p: p, action: "start"},
		&lxcLifecycleCmd{p: p, action: "stop"},
		&lxcLifecycleCmd{p: p, action: "reboot"},
	}
}

// resolveEndpoint 是所有命令的入口：按名字挑一个 endpoint（未指定则用默认）。
func (p *PVEPlugin) resolveEndpoint(name string) (*endpoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.endpoints) == 0 {
		return nil, errors.New("pve: no endpoints configured; add one under plugins.pve.settings.endpoints")
	}
	if name == "" {
		name = p.defaultEndpoint
	}
	e, ok := p.endpoints[name]
	if !ok {
		return nil, fmt.Errorf("pve: unknown endpoint %q", name)
	}
	return e, nil
}

func main() {
	pluginserve.Serve(newPVEPlugin())
}
