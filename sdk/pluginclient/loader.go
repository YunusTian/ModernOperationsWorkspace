// Package pluginclient 是 Core 侧加载并调用 MOW 插件的公共入口。
//
// 之所以独立成子包，是为了：
//   - 让 core 模块可以调用桥接层（sdk/internal/grpcbridge 属于 internal，禁止外部访问）
//   - 避免根 sdk 包与 grpcbridge 之间的循环依赖
//
// 典型使用（PluginManager 侧）：
//
//	lp, err := pluginclient.LoadFromBinary("./plugins/ssh/ssh.exe", nil)
//	if err != nil { ... }
//	defer lp.Close()
//	_ = mgr.Register(lp.Plugin)
package pluginclient

import (
	"errors"
	"fmt"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	hplugin "github.com/hashicorp/go-plugin"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/internal/grpcbridge"
)

// LoadedPlugin 表示一个已经启动的插件子进程。
// 使用完毕必须调用 Close，否则子进程不会被回收。
type LoadedPlugin struct {
	Plugin sdk.Plugin
	close  func()
}

// Close 关闭子进程与所有 gRPC 资源。多次调用无副作用。
func (l *LoadedPlugin) Close() {
	if l != nil && l.close != nil {
		l.close()
	}
}

// NewLoadedPlugin 构造一个 LoadedPlugin，主要供以下两类调用者使用：
//   - 测试代码（无需真正启动子进程即可组装出 LoadedPlugin）
//   - core / apps 侧希望以进程内实现（例如内置官方插件）复用同一入口的场景
//
// closeFn 会在 Close() 被调用时执行；可以为 nil。
func NewLoadedPlugin(p sdk.Plugin, closeFn func()) *LoadedPlugin {
	return &LoadedPlugin{Plugin: p, close: closeFn}
}

// LoadFromBinary 启动 path 所指的可执行文件作为插件子进程，
// 通过 hashicorp/go-plugin gRPC 建立通信，并返回可用的 sdk.Plugin。
func LoadFromBinary(path string, logger hclog.Logger) (*LoadedPlugin, error) {
	if path == "" {
		return nil, errors.New("plugin: binary path is empty")
	}
	if logger == nil {
		logger = hclog.NewNullLogger()
	}

	client := hplugin.NewClient(&hplugin.ClientConfig{
		HandshakeConfig:  sdk.Handshake,
		Plugins:          hplugin.PluginSet{sdk.PluginSetName: &grpcbridge.HcPlugin{}},
		Cmd:              exec.Command(path),
		AllowedProtocols: []hplugin.Protocol{hplugin.ProtocolGRPC},
		Logger:           logger,
	})

	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin: rpc handshake: %w", err)
	}
	raw, err := rpc.Dispense(sdk.PluginSetName)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin: dispense: %w", err)
	}
	p, ok := raw.(sdk.Plugin)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("plugin: unexpected type %T", raw)
	}
	return &LoadedPlugin{Plugin: p, close: client.Kill}, nil
}
