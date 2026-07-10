// Package history 提供 Workflow 一次执行的持久化 / 查询能力。
//
// v0.3 第三批边界：
//   - Store 接口 只定义 Save / List / Get 三个动作
//   - JSONLStore 是默认实现：append-only JSON Lines，与审计日志共享 data_dir
//   - 未来切换 SQLite / OpenSearch 只需换实现，Runner 与上层无需感知
//
// 为什么先做 JSONL 不上 SQLite：
//   - 保持 core 零 CGO 依赖，Windows / macOS / Linux 都能直接编译
//   - 单文件 append-only 已经满足 "看历史" 这一核心用例
//   - 一旦引入 SQLite 依赖，跨平台交叉编译与 e2e 测试成本会显著抬升
//
// 数据模型与 core/workflow.Result 对齐；额外补 RunID / StartedAt / FinishedAt / TargetID / Inputs。
package history

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mow/mow/core/workflow"
)

// Record 是一次 Workflow 执行的持久化快照。
//
// 字段设计原则：
//   - 尽量与 workflow.Result 对齐，避免翻译层
//   - Inputs 允许 nil，用于隐私敏感场景；TargetID 同样可为空
//   - Steps 直接引用 workflow.StepResult；解码后 Data 保持 json.RawMessage
type Record struct {
	RunID      string                 `json:"run_id"`
	WorkflowID string                 `json:"workflow_id"`
	TargetID   string                 `json:"target_id,omitempty"`
	Caller     string                 `json:"caller,omitempty"`
	Inputs     map[string]any         `json:"inputs,omitempty"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt time.Time              `json:"finished_at"`
	Duration   time.Duration          `json:"duration"`
	OK         bool                   `json:"ok"`
	Error      string                 `json:"error,omitempty"`
	Steps      []workflow.StepResult  `json:"steps,omitempty"`
}

// ListOptions 控制 List 的分页 / 过滤行为。
//
// Limit 强制上限；超过 500 会被裁剪到 500 —— 单次拉太多既伤 UI 也伤存储。
// WorkflowID 为空表示不按 workflow 过滤。
type ListOptions struct {
	Limit      int
	WorkflowID string
}

// Store 是执行历史的持久化接口。
//
// 语义约定：
//   - Save 是幂等尽力型：同一 RunID 再次 Save 会追加一行；调用方通常只 Save 一次
//   - List 按 FinishedAt 倒序返回（最新在前）
//   - Get 找不到返回 (nil, nil) —— 与 Go 生态惯例保持一致
type Store interface {
	Save(ctx context.Context, rec *Record) error
	List(ctx context.Context, opts ListOptions) ([]Record, error)
	Get(ctx context.Context, runID string) (*Record, error)
}

// noopStore 是 Save / List / Get 都不做任何事的空实现，用于禁用历史。
type noopStore struct{}

func (noopStore) Save(context.Context, *Record) error              { return nil }
func (noopStore) List(context.Context, ListOptions) ([]Record, error) { return nil, nil }
func (noopStore) Get(context.Context, string) (*Record, error)     { return nil, nil }
func (noopStore) SaveRun(context.Context, *workflow.RunSnapshot) error { return nil }

// Noop 返回一个空存储。上层未启用历史时使用它避免 nil 判空。
func Noop() Store { return noopStore{} }

// SnapshotToRecord 把 workflow.RunSnapshot 转成 history.Record。
//
// 单独暴露是为了让 Store 之外的组件（例如 Desktop 侧的一次快照 → 前端预览）
// 也能复用同一份翻译逻辑，避免各 UI 层各自 fiddle 字段。
func SnapshotToRecord(s *workflow.RunSnapshot) *Record {
	if s == nil {
		return nil
	}
	return &Record{
		RunID:      s.RunID,
		WorkflowID: s.WorkflowID,
		TargetID:   s.TargetID,
		Caller:     s.Caller,
		Inputs:     s.Inputs,
		StartedAt:  s.StartedAt,
		FinishedAt: s.FinishedAt,
		Duration:   s.Duration,
		OK:         s.OK,
		Error:      s.Error,
		Steps:      s.Steps,
	}
}

// encodeRecord 是内部使用的 JSON 编码工具（保留字段顺序）。
func encodeRecord(rec *Record) ([]byte, error) {
	return json.Marshal(rec)
}
