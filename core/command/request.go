package command

import (
	"encoding/json"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// Request / Response
// -----------------------------------------------------------------------------

// Request 是 Command Engine 的入参。
type Request struct {
	// PluginID / CommandID：全限定 ID 为 {PluginID}.{CommandID}。
	PluginID  string
	CommandID string

	// Params 是 Command 参数（JSON），对应 CommandSpec.InputSchema。
	// 若为 nil，Engine 会替换为 `{}`。
	Params json.RawMessage

	// Connection 已经建立好的连接（可选）；由上层准备好后传入。
	// 若为 nil 且 TargetID 非空，Engine 会通过注入的 ConnectionResolver 解析。
	Connection *sdk.Connection

	// TargetID 是 Connection Manager 内的 Target 标识（可选）。
	// 与 Connection 二选一：都提供时以 Connection 为准。
	TargetID string

	// Caller 记录调用来源。UI / CLI / API / AI / Workflow / Recipe。
	Caller sdk.Caller

	// Timeout 覆盖默认超时；0 表示使用 CommandSpec.DefaultTimeout。
	Timeout time.Duration

	// Confirmed：Dangerous 权限下，是否已由 UI / 调用方完成二次确认。
	// 若 false，Engine 会调用 Confirmer 询问；若 Confirmer 拒绝则返回错误。
	Confirmed bool

	// AuditID：若调用方希望复用上游的 auditId（例如 Workflow / Recipe 内部），可传入。
	// 若为空，Engine 会自动生成。
	AuditID string
}

// Response 是 Command Engine 的返回值。
type Response struct {
	AuditID    string
	Data       json.RawMessage
	Attributes map[string]string
	Duration   time.Duration
}

// -----------------------------------------------------------------------------
// Invocation（中间件之间流转的上下文）
// -----------------------------------------------------------------------------

// Invocation 是一次调用的完整上下文，中间件之间通过它传递。
type Invocation struct {
	Request Request
	AuditID string
	Handler sdk.CommandHandler
	Spec    sdk.CommandSpec

	// Confirmed 由 PermissionMiddleware 在完成二次确认后置位，
	// 用于后续 Middleware / 审计参考。若 Request.Confirmed 已经为 true，则不会重复询问。
	Confirmed bool
}

// FQID 返回 "{plugin}.{command}" 全限定 ID。
func (i *Invocation) FQID() string { return i.Request.PluginID + "." + i.Request.CommandID }

// EffectiveTimeout 计算最终生效的超时（Request.Timeout 优先）。
func (i *Invocation) EffectiveTimeout() time.Duration {
	if i.Request.Timeout > 0 {
		return i.Request.Timeout
	}
	return i.Spec.DefaultTimeout
}
