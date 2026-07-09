package command

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------------

// HandlerFunc 是 Middleware 内部的最小处理函数签名。
type HandlerFunc func(ctx context.Context, inv *Invocation) (*Response, error)

// Middleware 是 Command Engine 的横切拦截器。
// 典型实现：先做前置检查 → 调用 next → 做后置处理。
type Middleware func(next HandlerFunc) HandlerFunc

// chainMiddlewares 按注册顺序包裹终结点函数，
// 第一个注册的中间件位于最外层（最先执行）。
func chainMiddlewares(final HandlerFunc, mws []Middleware) HandlerFunc {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// -----------------------------------------------------------------------------
// ValidateMiddleware
// -----------------------------------------------------------------------------

// ValidateMiddleware 校验 Command 参数是否为合法 JSON。
// v0.1 仅做"是否可解析为 JSON 对象"的最基本校验；
// JSON Schema 校验将在后续引入（对齐 CommandSpec.InputSchema）。
func ValidateMiddleware() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, inv *Invocation) (*Response, error) {
			if err := validateParams(inv); err != nil {
				return nil, err
			}
			return next(ctx, inv)
		}
	}
}

func validateParams(inv *Invocation) error {
	if len(inv.Request.Params) == 0 {
		return nil
	}
	// 仅做基本 JSON 可解析校验（对象或 null）
	var m any
	if err := json.Unmarshal(inv.Request.Params, &m); err != nil {
		return sdk.NewError("PARAM_INVALID",
			"parameters are not valid JSON", err)
	}
	// TODO(v0.2): 若 inv.Spec.InputSchema 非空，执行 JSON Schema 校验
	return nil
}

// -----------------------------------------------------------------------------
// ResolveConnectionMiddleware
// -----------------------------------------------------------------------------

// ConnectionResolver 把 Request.TargetID 变成 sdk.Connection。
// 由 core/connection.Manager 提供 Open 方法的适配器实现。
type ConnectionResolver interface {
	Open(ctx context.Context, targetID string) (*sdk.Connection, error)
}

// ResolverFunc 让普通函数直接充当 ConnectionResolver。
type ResolverFunc func(ctx context.Context, targetID string) (*sdk.Connection, error)

// Open 实现 ConnectionResolver。
func (f ResolverFunc) Open(ctx context.Context, id string) (*sdk.Connection, error) {
	return f(ctx, id)
}

// ResolveConnectionMiddleware 在权限与审计之前把 TargetID 解析为 Connection。
//
// 规则：
//   - Request.Connection 已有 → 直接放行
//   - Command 未声明 ConnectionType → 跳过（无需连接）
//   - 声明了 ConnectionType 但 TargetID 与 Connection 都为空 → 报 CONNECTION_REQUIRED
//   - 有 TargetID 但 Resolver 为 nil → 报 RESOLVER_MISSING
//   - Resolver 成功返回 → 写回 inv.Request.Connection
func ResolveConnectionMiddleware(r ConnectionResolver) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, inv *Invocation) (*Response, error) {
			if err := resolveConnection(ctx, r, inv); err != nil {
				return nil, err
			}
			return next(ctx, inv)
		}
	}
}

func resolveConnection(ctx context.Context, r ConnectionResolver, inv *Invocation) error {
	if inv.Request.Connection != nil {
		return nil
	}
	if inv.Spec.ConnectionType == "" {
		return nil
	}
	if inv.Request.TargetID == "" {
		return sdk.ErrConnectionRequired
	}
	if r == nil {
		return sdk.NewError("RESOLVER_MISSING",
			"engine has no ConnectionResolver but request carries TargetID", nil)
	}
	conn, err := r.Open(ctx, inv.Request.TargetID)
	if err != nil {
		return sdk.NewError("CONNECTION_OPEN_FAILED", err.Error(), err)
	}
	inv.Request.Connection = conn
	return nil
}

// -----------------------------------------------------------------------------
// PermissionMiddleware
// -----------------------------------------------------------------------------

// Confirmer 用于 Dangerous 权限的二次确认。
// 由上层（UI 弹窗 / CLI TTY prompt / 策略中心）实现。
type Confirmer interface {
	// Confirm 询问是否允许执行本次 Dangerous 调用。
	// 返回 approved = true 表示批准；false 表示拒绝。
	Confirm(ctx context.Context, req ConfirmationRequest) (approved bool, err error)
}

// ConfirmationRequest 描述一次待确认调用的上下文。
type ConfirmationRequest struct {
	AuditID   string
	PluginID  string
	CommandID string
	Params    json.RawMessage
	Caller    sdk.Caller
	// Reason 是可选的执行理由（例如 AI 场景下的规划说明）。
	Reason string
}

// DenyConfirmer 是安全默认：一律拒绝。
// 生产环境应替换为真实实现。
type DenyConfirmer struct{}

func (DenyConfirmer) Confirm(context.Context, ConfirmationRequest) (bool, error) {
	return false, sdk.ErrConfirmationRequired
}

// AllowConfirmer 一律放行，仅用于测试。
type AllowConfirmer struct{}

func (AllowConfirmer) Confirm(context.Context, ConfirmationRequest) (bool, error) {
	return true, nil
}

// PermissionMiddleware 检查 Command 权限：
//   - PermUnspecified：拒绝
//   - PermRead / PermWrite / PermExecute：放行
//   - PermDangerous：若 Request.Confirmed = true 直接放行；否则调用 Confirmer 询问
//
// 若 confirm 为 nil，等价于 DenyConfirmer。
func PermissionMiddleware(confirm Confirmer) Middleware {
	if confirm == nil {
		confirm = DenyConfirmer{}
	}
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, inv *Invocation) (*Response, error) {
			if err := checkPermission(ctx, confirm, inv); err != nil {
				return nil, err
			}
			return next(ctx, inv)
		}
	}
}

func checkPermission(ctx context.Context, confirm Confirmer, inv *Invocation) error {
	switch inv.Spec.Permission {
	case sdk.PermUnspecified:
		return sdk.NewError("PERMISSION_UNDECLARED",
			"command permission is not declared", nil)

	case sdk.PermDangerous:
		if inv.Request.Confirmed {
			inv.Confirmed = true
			return nil
		}
		ok, err := confirm.Confirm(ctx, ConfirmationRequest{
			AuditID:   inv.AuditID,
			PluginID:  inv.Request.PluginID,
			CommandID: inv.Request.CommandID,
			Params:    inv.Request.Params,
			Caller:    inv.Request.Caller,
		})
		if err != nil {
			return err
		}
		if !ok {
			return sdk.ErrConfirmationRequired
		}
		inv.Confirmed = true
		inv.Request.Confirmed = true
		return nil

	default:
		return nil
	}
}

// -----------------------------------------------------------------------------
// AuditMiddleware
// -----------------------------------------------------------------------------

// AuditRecord 是一次调用的审计快照。
//
// Params 已按 CommandSpec.InputSchema 中标记为 `x-mow-sensitive: true`
// 的字段做过脱敏，可直接落盘 / 日志；原始参数不会出现在这里。
type AuditRecord struct {
	AuditID    string
	PluginID   string
	CommandID  string
	Permission sdk.Permission
	Caller     sdk.Caller
	Params     json.RawMessage
	Metadata   map[string]any
	Confirmed  bool
	Streaming  bool
	StartedAt  time.Time
	Duration   time.Duration
	Err        error
}

// AuditSink 接收 Engine 产出的审计事件。
// 实现者需处理：结构化落盘、异步刷写、脱敏、TTL。
type AuditSink interface {
	Start(ctx context.Context, rec *AuditRecord)
	Finish(ctx context.Context, rec *AuditRecord)
}

// NopAudit 是最小实现：不落盘，只把事件转成 slog 输出。
// 默认注入的实现即 NopAudit。
type NopAudit struct{}

func (NopAudit) Start(context.Context, *AuditRecord)  {}
func (NopAudit) Finish(context.Context, *AuditRecord) {}

// AuditMiddleware 在 Command 执行前后写入 AuditSink。
// 无论成功 / 失败 / 取消，都会调用 Finish。
func AuditMiddleware(sink AuditSink) Middleware {
	if sink == nil {
		sink = NopAudit{}
	}
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, inv *Invocation) (*Response, error) {
			rec := newAuditRecord(inv)
			sink.Start(ctx, rec)

			start := time.Now()
			resp, err := next(ctx, inv)
			rec.Duration = time.Since(start)

			if err != nil {
				rec.Err = err
			}
			sink.Finish(ctx, rec)

			if err != nil {
				// 附带 AuditID 便于日志聚合
				return nil, &InvocationError{AuditID: inv.AuditID, Cause: err}
			}
			return resp, nil
		}
	}
}

func newAuditRecord(inv *Invocation) *AuditRecord {
	// 预初始化 Metadata，使得 audit record 与后续中间件共享同一 map，
	// 后续任何 SetMetadata 写入都能被 Finish 时 sink 读到。
	if inv.Request.Metadata == nil {
		inv.Request.Metadata = make(map[string]any)
	}
	return &AuditRecord{
		AuditID:    inv.AuditID,
		PluginID:   inv.Request.PluginID,
		CommandID:  inv.Request.CommandID,
		Permission: inv.Spec.Permission,
		Caller:     inv.Request.Caller,
		Params:     RedactParams(inv.Spec.InputSchema, inv.Request.Params),
		Metadata:   inv.Request.Metadata,
		Confirmed:  inv.Request.Confirmed || inv.Confirmed,
		StartedAt:  time.Now(),
	}
}

// IsError returns true when err represents a non-transport-level failure
// worth escalating (i.e. not a context cancel due to shutdown).
func IsError(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}
