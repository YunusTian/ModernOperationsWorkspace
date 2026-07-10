// result.go 定义 Workflow 一次执行的结果模型。
//
// v0.2 PR1 骨架：仅定义数据结构，尚不接入 Runner。字段按 core/recipe 对齐，
// 便于后续与 recipe.Result 互操作。

package workflow

import (
	"encoding/json"
	"time"
)

// Result 是一次 Workflow 执行的汇总结果。
type Result struct {
	// WorkflowID：对应 Workflow.ID。
	WorkflowID string `json:"workflow_id"`
	// OK：所有步骤成功即为 true；任一步骤失败则为 false。
	OK bool `json:"ok"`
	// Steps：按执行顺序追加；未执行的步骤不出现。
	Steps []StepResult `json:"steps"`
	// Duration：整个 Workflow 的墙钟耗时。
	Duration time.Duration `json:"duration"`
}

// StepResult 是单步的执行结果。
//
// Command 与 Recipe 二选一，与 Step 声明保持一致；未使用的字段留空。
type StepResult struct {
	// StepID：Step.ID。
	StepID string `json:"step_id"`

	// Command / Recipe：命中的执行体标识（互斥）。
	Command string `json:"command,omitempty"`
	Recipe  string `json:"recipe,omitempty"`

	// OK：本步是否成功。
	OK bool `json:"ok"`

	// AuditID：单条 Command 场景下透传的审计 ID；Recipe 场景为空。
	AuditID string `json:"audit_id,omitempty"`

	// Data：Command 的原始输出，或 Recipe 汇总结果的 JSON。
	Data json.RawMessage `json:"data,omitempty"`

	// ErrorCode / ErrorMsg：失败时填充；成功时为空。
	ErrorCode string `json:"error_code,omitempty"`
	ErrorMsg  string `json:"error_msg,omitempty"`

	// Duration：本步的墙钟耗时。
	Duration time.Duration `json:"duration"`
}
