package grpcbridge

import (
	"context"

	hclog "github.com/hashicorp/go-hclog"
	hplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/mow/mow/sdk"
	pb "github.com/mow/mow/sdk/proto"
)

// -----------------------------------------------------------------------------
// hashicorp/go-plugin 桥接
// -----------------------------------------------------------------------------

// HcPlugin 是 hashicorp/go-plugin 所需的 GRPCPlugin 实现。
// 服务端与客户端使用同一个类型；服务端通过 Impl 字段注入 sdk.Plugin，
// 客户端通过 GRPCClient 得到桥接后的 sdk.Plugin。
type HcPlugin struct {
	hplugin.NetRPCUnsupportedPlugin // 我们只支持 gRPC

	// Impl 由服务端（插件进程）填入；客户端侧留 nil。
	Impl sdk.Plugin
}

// GRPCServer 注册 pb.PluginServer。
func (p *HcPlugin) GRPCServer(_ *hplugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterPluginServer(s, NewServer(p.Impl))
	return nil
}

// GRPCClient 返回一个 sdk.Plugin 视图；Core 拿到后当作本地插件使用。
func (p *HcPlugin) GRPCClient(_ context.Context, _ *hplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return NewClient(pb.NewPluginClient(c)), nil
}

// -----------------------------------------------------------------------------
// 便捷入口：插件进程使用
// -----------------------------------------------------------------------------

// ServeConfig 构造 hashicorp/go-plugin 的 ServeConfig；
// 由 sdk.Serve 调用；一般插件开发者无需直接使用。
func ServeConfig(p sdk.Plugin, logger hclog.Logger) *hplugin.ServeConfig {
	return &hplugin.ServeConfig{
		HandshakeConfig: sdk.Handshake,
		Plugins: hplugin.PluginSet{
			sdk.PluginSetName: &HcPlugin{Impl: p},
		},
		GRPCServer: hplugin.DefaultGRPCServer,
		Logger:     logger,
	}
}

// ClientConfig 构造 hashicorp/go-plugin 的 ClientConfig；
// 由 Core 侧（PluginManager 的 gRPC 加载器）调用。
//
// cmd 描述如何启动插件子进程（例如 exec.Command("./plugins/ssh") ）。
func ClientConfig(cmd hplugin.SecureConfig, logger hclog.Logger) *hplugin.ClientConfig {
	_ = cmd // v0.1：预留；实际实例化由调用方组合，避免绑定 exec.Cmd 到本包
	return &hplugin.ClientConfig{
		HandshakeConfig:  sdk.Handshake,
		Plugins:          hplugin.PluginSet{sdk.PluginSetName: &HcPlugin{}},
		AllowedProtocols: []hplugin.Protocol{hplugin.ProtocolGRPC},
		Logger:           logger,
	}
}
