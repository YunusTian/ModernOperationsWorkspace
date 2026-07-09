// Package sdk 是 MOW Plugin SDK 的对外入口。
//
// 插件开发者只需依赖本 module，无需了解 gRPC / hashicorp/go-plugin 细节：
//
//	func main() {
//		sdk.Serve(&MyPlugin{})
//	}
//
// 详见 docs/plugin-system.md。
package sdk

import (
	"context"
	"encoding/json"
)

// -----------------------------------------------------------------------------
// Plugin 抽象
// -----------------------------------------------------------------------------

// Plugin 是每个插件必须实现的最小接口。
//
// 生命周期：
//
//	NewXxxPlugin() → Metadata() → Init() → [Command 调用...] → Shutdown()
//
// 实现建议：所有方法应快速返回（<30s），耗时操作请放到 Command 内部处理。
type Plugin interface {
	// Metadata 返回插件元信息与其提供的能力清单。
	// Core 在 Load 阶段调用一次；返回值应保持稳定（不随运行时变化）。
	Metadata() Metadata

	// Init 在插件启用时被调用。settings 由用户配置解码而来，
	// 结构由插件自行定义并在 Metadata 中通过 JSON Schema 描述。
	//
	// 一次进程生命周期内只会被调用一次。
	Init(ctx context.Context, req InitRequest) error

	// Shutdown 在插件停用或卸载时调用；请在此关闭连接、释放资源。
	// 超时由 Core 控制，插件应尽快返回。
	Shutdown(ctx context.Context) error

	// HealthCheck 供 Core 定期探活。默认可返回 StatusHealthy。
	HealthCheck(ctx context.Context) HealthStatus

	// Commands 返回本插件提供的所有 Command Handler。
	// 顺序不影响调用；同一 Command ID 不得重复。
	Commands() []CommandHandler
}

// -----------------------------------------------------------------------------
// Metadata
// -----------------------------------------------------------------------------

// Metadata 描述插件的静态元信息。
type Metadata struct {
	// ID 是插件的唯一标识，例："ssh"、"docker"、"pve"。
	// 必须与 plugin.yaml 中的 id 一致，且全局唯一。
	ID string

	// Name 用于 UI 展示，例："SSH"。
	Name string

	// Version 语义化版本，例："0.1.0"。
	Version string

	Author      string
	Description string
	Homepage    string
	License     string

	// CoreVersion 是本插件所依赖的 Core 版本范围（语义化范围）。
	// 例：">=0.1.0,<0.2.0"
	CoreVersion string

	// PluginDependencies 是本插件依赖的其他 Plugin ID。
	// Core 会在加载时保证依赖顺序。
	PluginDependencies []string

	// ConnectionTypes 声明本插件会用到的连接类型（例："ssh"、"docker"）。
	// 供 Core 在调用前建立 / 复用连接。
	ConnectionTypes []string

	// Recipes / Workflows 供元信息注册；实际内容由 Core 侧解析 YAML。
	Recipes   []RecipeSpec
	Workflows []WorkflowSpec

	// SettingsSchema 是 Init.settings 的 JSON Schema（可选）。
	// Core 会在 Enable 时按此 Schema 校验用户配置。
	SettingsSchema json.RawMessage
}

// InitRequest 是 Plugin.Init 的入参。
type InitRequest struct {
	// Settings 是用户为该 Plugin 配置的内容（JSON 编码）。
	// 建议通过 json.Unmarshal 解码到插件自定义的结构体。
	Settings json.RawMessage

	// CoreVersion 是当前运行的 Core 版本，插件可据此启用兼容分支。
	CoreVersion string

	// DataDir 是本插件的持久化数据目录（已保证存在且可写）。
	DataDir string
}

// HealthStatus 表示插件当前的健康状态。
type HealthStatus int

const (
	StatusUnknown HealthStatus = iota
	StatusHealthy
	StatusDegraded
	StatusUnhealthy
)

func (s HealthStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusDegraded:
		return "degraded"
	case StatusUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}
