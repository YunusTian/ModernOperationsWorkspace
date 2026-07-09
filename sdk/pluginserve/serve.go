// Package pluginserve 是插件进程的启动入口。
//
// 单独作为子包以避免根 sdk 包与 sdk/internal/grpcbridge 之间的循环依赖。
//
// 典型使用：
//
//	package main
//
//	import (
//		"github.com/mow/mow/sdk"
//		"github.com/mow/mow/sdk/pluginserve"
//	)
//
//	func main() {
//		pluginserve.Serve(&MyPlugin{})
//	}
package pluginserve

import (
	"fmt"
	"os"

	hplugin "github.com/hashicorp/go-plugin"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/internal/grpcbridge"
)

// Serve 校验插件并启动 gRPC 服务，阻塞至进程退出。
func Serve(p sdk.Plugin) {
	if err := sdk.Validate(p); err != nil {
		fmt.Fprintln(os.Stderr, "mow-plugin: validate failed:", err)
		os.Exit(1)
	}
	hplugin.Serve(grpcbridge.ServeConfig(p, nil))
}
