// Package plugin 实现插件生命周期管理（Load / Enable / Disable / Unload）。
// v0.1 采用 hashicorp/go-plugin 作为 gRPC 子进程加载机制。
//
// 详见 docs/plugin-system.md。
package plugin
