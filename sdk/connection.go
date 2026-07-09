package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
)

// -----------------------------------------------------------------------------
// Connection
// -----------------------------------------------------------------------------

// Connection 是 Core 分配给 Command 的连接抽象。
//
// v0.1 版本中，插件通过 Connection 提供的凭据自行建立底层协议（例如 crypto/ssh）。
// 未来版本可能引入 ConnectionHandle 服务，让 Plugin 通过 gRPC 反向调用 Core 完成 IO。
type Connection struct {
	// ID 是 Core 分配的连接标识，用于日志追踪。
	ID string

	// Type 是连接类型（例："ssh"、"docker"），与 CommandSpec.ConnectionType 对齐。
	Type string

	// Credentials 是本次连接的凭据快照（JSON 编码）。
	// 插件应通过对应的凭据结构体解码，例如 SSHCredentials。
	//
	// 敏感字段仅在进程内存在，Plugin 不得写入日志。
	Credentials json.RawMessage

	// Metadata 是连接的元信息（host / port / user / tags 等）。
	Metadata map[string]string
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// 标准错误。插件可直接返回或用 errors.Is 判定。
var (
	// ErrNotSupported 表示 Command 不支持当前调用方式。
	// 常见场景：一次性 Command 收到 ExecuteStream 调用，或反之。
	ErrNotSupported = &Error{Code: "NOT_SUPPORTED", Message: "operation not supported"}

	// ErrParamInvalid 表示参数校验失败。
	ErrParamInvalid = &Error{Code: "PARAM_INVALID", Message: "invalid parameters"}

	// ErrConfirmationRequired 表示 Dangerous 操作缺少用户二次确认。
	// 插件应在 Confirmed=false 时立即返回本错误。
	ErrConfirmationRequired = &Error{Code: "CONFIRMATION_REQUIRED", Message: "dangerous operation requires confirmation"}

	// ErrConnectionRequired 表示 Command 需要连接但未提供。
	ErrConnectionRequired = &Error{Code: "CONNECTION_REQUIRED", Message: "connection is required but not provided"}

	// ErrTimeout 表示执行超时。
	ErrTimeout = &Error{Code: "TIMEOUT", Message: "operation timed out"}

	// ErrCanceled 表示调用被取消（context canceled）。
	ErrCanceled = &Error{Code: "CANCELED", Message: "operation canceled"}
)

// Error 是插件对外的标准错误结构，序列化后与 proto Error 对齐。
type Error struct {
	// Code 是稳定错误码，例："SSH_AUTH_FAILED"、"PARAM_INVALID"。
	// 应保持向后兼容，供 UI / AI / Workflow 做条件判断。
	Code string

	// Message 是用户可读消息。
	Message string

	// Details 是结构化详情（栈、字段、Provider 原始错误）。
	Details map[string]any

	// Retryable 标识错误是否可重试，供 Workflow 决策。
	Retryable bool

	// Cause 是底层原因（不跨越 gRPC 边界，仅本地日志使用）。
	Cause error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Is 允许通过 errors.Is 匹配同 Code 的错误：
//
//	errors.Is(err, sdk.ErrTimeout)
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code
}

// NewError 构造一个 Error；常见搭配：
//
//	return nil, sdk.NewError("SSH_AUTH_FAILED", "authentication failed", err)
func NewError(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

// WithDetails 追加 Details 字段。链式风格：
//
//	return sdk.NewError("...","...",err).WithDetails(map[string]any{"host": host})
func (e *Error) WithDetails(d map[string]any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	for k, v := range d {
		e.Details[k] = v
	}
	return e
}

// WithRetryable 设置错误是否可重试。
func (e *Error) WithRetryable(v bool) *Error {
	e.Retryable = v
	return e
}
