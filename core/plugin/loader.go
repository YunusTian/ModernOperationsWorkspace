package plugin

import (
	hclog "github.com/hashicorp/go-hclog"

	"github.com/mow/mow/sdk/pluginclient"
)

// -----------------------------------------------------------------------------
// LoadFromBinary：以子进程方式启动一个插件（Core 侧包装）
// -----------------------------------------------------------------------------

// LoadedPlugin 表示一个已经启动的插件子进程。
// 使用完毕必须调用 Close 释放子进程。
type LoadedPlugin = pluginclient.LoadedPlugin

// LoadFromBinary 启动 path 所指的可执行文件作为插件子进程，
// 返回可 Register 到 Manager 的适配器。
func LoadFromBinary(path string, logger hclog.Logger) (*LoadedPlugin, error) {
	return pluginclient.LoadFromBinary(path, logger)
}
