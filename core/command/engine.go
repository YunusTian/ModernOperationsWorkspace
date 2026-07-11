// Package command 实现 MOW 的统一命令执行入口（Command Engine）。
//
// Command Engine 是 Core 中唯一的对外调用入口：
//
//	UI / CLI / AI / API
//	          │
//	          ▼
//	   command.Engine.Run(ctx, req)
//	          │
//	          ├── Validate Middleware   （参数校验）
//	          ├── Permission Middleware （权限校验 / Dangerous 二次确认）
//	          ├── Audit Middleware      （审计日志）
//	          └── PluginManager.Command → Handler.Execute
//
// 详见 docs/command-engine.md。
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// Engine
// -----------------------------------------------------------------------------

// Engine 是 MOW 唯一的 Command 执行器。
// 并发安全；一个进程一份即可。
type Engine struct {
	pm       *plugin.Manager
	log      *logger.Logger
	audit    AuditSink
	confirm  Confirmer
	resolver ConnectionResolver

	// middlewares 是一次性 Command 的中间件链。
	// 顺序：先注册的先执行、后返回。
	middlewares []Middleware
}

// Spec 返回已注册 Command 的静态声明，供宿主侧目录、UI 和 AI 工具白名单使用。
// 调用方仍必须通过 Run / RunStream 执行，不能据此绕过权限链路。
func (e *Engine) Spec(pluginID, commandID string) (sdk.CommandSpec, error) {
	h, err := e.pm.Command(pluginID, commandID)
	if err != nil {
		return sdk.CommandSpec{}, err
	}
	return h.Spec(), nil
}

// Options 是 Engine 构造参数。
type Options struct {
	// Manager 必填，Engine 从中查找 Plugin 与 Command。
	Manager *plugin.Manager

	// Logger 供 Engine 内部使用；nil 时使用 logger.Default()。
	Logger *logger.Logger

	// Audit 审计接收器；nil 时使用 NopAudit（不落盘，仅打日志）。
	Audit AuditSink

	// Confirm 供 Dangerous 权限的二次确认；
	// nil 时使用 DenyConfirmer（一律拒绝，安全默认）。
	Confirm Confirmer

	// Resolver 把 Request.TargetID 解析为 sdk.Connection；
	// 一般传入 core/connection.Manager.Open 的适配器。
	// 若为 nil，仅接受 Request.Connection 直接传入；带 TargetID 但缺 Connection 的请求将报错。
	Resolver ConnectionResolver

	// ExtraMiddlewares 是用户自定义中间件（追加到内置链之后）。
	// 常见用途：限流、缓存、追踪、指标。
	ExtraMiddlewares []Middleware
}

// New 构造一个 Engine，默认注册 validate / permission / audit 三段中间件。
func New(opts Options) *Engine {
	if opts.Manager == nil {
		panic("command: Options.Manager is required")
	}
	log := opts.Logger
	if log == nil {
		log = logger.Default()
	}
	audit := opts.Audit
	if audit == nil {
		audit = NopAudit{}
	}
	confirm := opts.Confirm
	if confirm == nil {
		confirm = DenyConfirmer{}
	}

	e := &Engine{
		pm:       opts.Manager,
		log:      log.WithComponent("command.engine"),
		audit:    audit,
		confirm:  confirm,
		resolver: opts.Resolver,
	}
	e.middlewares = append(e.middlewares,
		ValidateMiddleware(),
		ResolveConnectionMiddleware(opts.Resolver),
		PermissionMiddleware(confirm),
		AuditMiddleware(audit),
	)
	e.middlewares = append(e.middlewares, opts.ExtraMiddlewares...)
	return e
}

// -----------------------------------------------------------------------------
// Run（一次性 Command）
// -----------------------------------------------------------------------------

// Run 执行一次性 Command。
// 若 Command 是流式（Spec.Streaming = true），会直接返回 ErrStreamingCommand。
func (e *Engine) Run(ctx context.Context, req Request) (*Response, error) {
	inv, err := e.prepare(req)
	if err != nil {
		return nil, err
	}
	if inv.Spec.Streaming {
		return nil, ErrStreamingCommand
	}

	// 终结点：调用真正的 Handler.Execute
	final := func(ctx context.Context, inv *Invocation) (*Response, error) {
		start := time.Now()
		resp, err := inv.Handler.Execute(ctx, &sdk.ExecuteRequest{
			AuditID:    inv.AuditID,
			Params:     inv.Request.Params,
			Connection: inv.Request.Connection,
			Caller:     inv.Request.Caller,
			Timeout:    inv.Request.Timeout,
			Confirmed:  inv.Request.Confirmed,
		})
		if err != nil {
			return nil, err
		}
		return &Response{
			AuditID:    inv.AuditID,
			Data:       resp.Data,
			Attributes: resp.Attributes,
			Duration:   time.Since(start),
		}, nil
	}

	handler := chainMiddlewares(final, e.middlewares)

	ctx, cancel := applyTimeout(ctx, inv.EffectiveTimeout())
	defer cancel()

	return handler(ctx, inv)
}

// -----------------------------------------------------------------------------
// RunStream（流式 Command）
// -----------------------------------------------------------------------------

// RunStream 执行流式 Command。stream 由调用方（UI / CLI）提供。
// Middleware 链不作用于流式（因流式语义与请求-响应模型不匹配）；
// 权限、审计、参数校验以显式内联方式在此路径上完成。
func (e *Engine) RunStream(ctx context.Context, req Request, stream sdk.Stream) error {
	inv, err := e.prepare(req)
	if err != nil {
		return err
	}
	if !inv.Spec.Streaming {
		return ErrNotStreamingCommand
	}

	// 内联校验（复用 Middleware 内的核心函数）
	if err := validateParams(inv); err != nil {
		return err
	}
	if err := resolveConnection(ctx, e.resolver, inv); err != nil {
		return err
	}
	if err := checkPermission(ctx, e.confirm, inv); err != nil {
		return err
	}

	// 将引擎生成的 AuditID 注入 stream，使插件端 s.AuditID() 与审计记录一致。
	if as, ok := stream.(AuditIDSetter); ok {
		as.SetAuditID(inv.AuditID)
	}

	// 审计：开始 / 结束
	rec := newAuditRecord(inv)
	rec.Streaming = true
	e.audit.Start(ctx, rec)

	start := time.Now()
	boundStream := &invocationStream{
		Stream:     stream,
		ctx:        ctx,
		auditID:    inv.AuditID,
		caller:     inv.Request.Caller,
		confirmed:  inv.Request.Confirmed,
		params:     inv.Request.Params,
		connection: inv.Request.Connection,
	}
	err = inv.Handler.ExecuteStream(ctx, boundStream)
	rec.Duration = time.Since(start)
	rec.Err = err
	e.audit.Finish(ctx, rec)
	return err
}

// -----------------------------------------------------------------------------
// 内部：准备 Invocation
// -----------------------------------------------------------------------------

// prepare 完成三件事：
//  1. 查找 CommandHandler
//  2. 读取 Spec
//  3. 生成 AuditID（若调用方未提供）
func (e *Engine) prepare(req Request) (*Invocation, error) {
	if req.PluginID == "" || req.CommandID == "" {
		return nil, ErrInvalidRequest
	}
	handler, err := e.pm.Command(req.PluginID, req.CommandID)
	if err != nil {
		return nil, err
	}
	spec := handler.Spec()

	auditID := req.AuditID
	if auditID == "" {
		auditID = NewAuditID()
	}
	if req.Params == nil {
		req.Params = json.RawMessage(`{}`)
	}

	return &Invocation{
		Request: req,
		AuditID: auditID,
		Handler: handler,
		Spec:    spec,
	}, nil
}

func applyTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// -----------------------------------------------------------------------------
// AuditIDSetter —— 流式 Command 的 AuditID 注入点
// -----------------------------------------------------------------------------

// AuditIDSetter 由 sdk.Stream 实现者可选实现。
// Engine.RunStream 在调用 ExecuteStream 前调用 SetAuditID，
// 把引擎统一生成的审计 ID 注入 stream，
// 使插件端 s.AuditID() 与审计记录中的 audit_id 保持一致。
type AuditIDSetter interface {
	SetAuditID(id string)
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrInvalidRequest 表示 PluginID / CommandID 为空等基本请求错误。
	ErrInvalidRequest = errors.New("command: invalid request")

	// ErrStreamingCommand 表示对流式 Command 调用了 Run。
	ErrStreamingCommand = errors.New("command: use RunStream for streaming command")

	// ErrNotStreamingCommand 表示对非流式 Command 调用了 RunStream。
	ErrNotStreamingCommand = errors.New("command: use Run for non-streaming command")
)

// InvocationError 是 Engine 层错误的统一形态，附加了 AuditID 便于追溯。
type InvocationError struct {
	AuditID string
	Cause   error
}

func (e *InvocationError) Error() string {
	if e.AuditID == "" {
		return e.Cause.Error()
	}
	return fmt.Sprintf("[audit=%s] %s", e.AuditID, e.Cause.Error())
}
func (e *InvocationError) Unwrap() error { return e.Cause }
