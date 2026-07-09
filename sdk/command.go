package sdk

import (
	"context"
	"encoding/json"
	"time"
)

// -----------------------------------------------------------------------------
// Command
// -----------------------------------------------------------------------------

// CommandHandler 是插件内一条 Command 的实现。
//
// 实现方式二选一（不得同时实现两者）：
//
//   - 一次性 Command：实现 Execute
//   - 流式 Command：实现 ExecuteStream；同时 Spec().Streaming = true
//
// 对于流式 Command，Execute 允许返回 ErrNotSupported。
type CommandHandler interface {
	// Spec 返回 Command 的静态定义（ID / 权限 / Schema / 是否流式 / ...）。
	Spec() CommandSpec

	// Execute 执行一次性 Command 并返回结构化结果。
	Execute(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, error)

	// ExecuteStream 执行流式 Command。
	// stream 提供发送输出 / 读取用户输入 / 感知取消的能力。
	// 返回 nil 表示成功结束；非 nil 表示失败。
	//
	// 非流式 Command 可直接返回 ErrNotSupported。
	ExecuteStream(ctx context.Context, stream Stream) error
}

// CommandSpec 描述一条 Command 的静态定义。
type CommandSpec struct {
	// ID 是 Plugin 内的 Command 标识（例："exec"、"upload"）。
	// 全限定 ID 由 Core 拼接为 "{plugin_id}.{command_id}"。
	ID string

	// Description 供 UI / AI 消费。建议一句话说明"做什么"。
	Description string

	// Permission 权限级别，参见 docs/permission.md。
	// Dangerous 级 Command 会被 Core 强制二次确认。
	Permission Permission

	// Streaming 标识该 Command 是否走 ExecuteStream。
	Streaming bool

	// InputSchema / OutputSchema 是 JSON Schema（RawMessage）。
	// Core 会在调用前后进行校验。
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage

	// ConnectionType 声明本 Command 需要的连接类型（例："ssh"）。
	// 若为空，Core 不会为其准备连接。
	ConnectionType string

	// DefaultTimeout 是建议超时，调用方可覆盖。0 表示不限。
	DefaultTimeout time.Duration

	// Idempotent 标识是否幂等，供 Workflow / AI 重试策略参考。
	Idempotent bool

	// Tags 用于 Marketplace 检索与 UI 分类。
	Tags []string
}

// ExecuteRequest 是 Command.Execute 的入参。
type ExecuteRequest struct {
	// AuditID 由 Core 生成，全局唯一，用于审计与追踪。
	AuditID string

	// Params 是 Command 参数（对应 CommandSpec.InputSchema）。
	// 建议 json.Unmarshal 到插件自定义结构体。
	Params json.RawMessage

	// Connection 是已建立的连接（若 CommandSpec.ConnectionType 非空）。
	Connection *Connection

	// Caller 记录调用来源，供审计与权限判定。
	Caller Caller

	// Timeout 是本次调用的超时；0 表示使用 CommandSpec.DefaultTimeout。
	// 已通过 ctx 传递，此字段仅作参考。
	Timeout time.Duration

	// Confirmed 标识 Dangerous Command 是否已经用户确认。
	// 若 Permission = Dangerous 且此值为 false，插件应直接返回 ErrConfirmationRequired。
	Confirmed bool
}

// ExecuteResponse 是 Command.Execute 的返回值。
type ExecuteResponse struct {
	// Data 是结构化输出（对应 CommandSpec.OutputSchema）。
	// 建议 json.Marshal 从插件自定义结构体生成。
	Data json.RawMessage

	// Attributes 是附加可观测字段（打点、Trace ID 等）。
	Attributes map[string]string
}

// -----------------------------------------------------------------------------
// Permission
// -----------------------------------------------------------------------------

// Permission 与 docs/permission.md 严格对齐。
type Permission int

const (
	PermUnspecified Permission = iota
	PermRead                   // 只读
	PermWrite                  // 写文件 / 修改配置
	PermExecute                // 执行命令
	PermDangerous              // 不可逆或高影响，必须二次确认
)

func (p Permission) String() string {
	switch p {
	case PermRead:
		return "read"
	case PermWrite:
		return "write"
	case PermExecute:
		return "execute"
	case PermDangerous:
		return "dangerous"
	default:
		return "unspecified"
	}
}

// -----------------------------------------------------------------------------
// Caller
// -----------------------------------------------------------------------------

// CallerType 表示调用来源。
type CallerType int

const (
	CallerUnspecified CallerType = iota
	CallerCLI
	CallerDesktop
	CallerAPI
	CallerAI
	CallerWorkflow
	CallerRecipe
)

// Caller 记录一次调用的来源与身份。
type Caller struct {
	Type          CallerType
	User          string
	SessionID     string // 例：AI 会话 ID
	ParentAuditID string // 上游 auditId，用于长链路追溯
}

// -----------------------------------------------------------------------------
// Recipe / Workflow 元信息（Plugin 侧只暴露 ID，实际 YAML 由 Core 解析）
// -----------------------------------------------------------------------------

type RecipeSpec struct {
	ID          string
	Description string
	Permission  Permission
	CommandIDs  []string
	Tags        []string
}

type WorkflowSpec struct {
	ID          string
	Description string
	Permission  Permission
	Tags        []string
}
