package sdk

import (
	hplugin "github.com/hashicorp/go-plugin"
)

// -----------------------------------------------------------------------------
// hashicorp/go-plugin 握手 & 服务集
// -----------------------------------------------------------------------------

// Handshake 是 Core 与 Plugin 之间的握手常量。
// ProtocolVersion 若不匹配，双方拒绝对话。
// MagicCookie 用于识别子进程是否为 MOW 插件（防止误启动）。
//
// 变更 ProtocolVersion 意味着旧插件将被拒绝，需要慎重。
var Handshake = hplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "MOW_PLUGIN",
	MagicCookieValue: "mow-plugin-magic-cookie-v1",
}

// PluginSetName 是 hashicorp/go-plugin 内部注册用的服务名。
// Core 与 Plugin 双方必须一致。
const PluginSetName = "mow_plugin"
