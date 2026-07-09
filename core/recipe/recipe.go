// Package recipe 实现由多条 Command 组成的、预定义且已测试的操作单元。
//
// v0.1 边界：
//   - 静态注册（内置 recipe）
//   - 单一 TargetID，所有 step 复用同一目标
//   - 按顺序执行；任何 step 失败即中断
//   - Step 返回 sdk.ExecuteResponse.Data 原样透传（未做字段抽取）
//
// 未来（v0.2+）：
//   - YAML DSL + Loader
//   - 每步独立 TargetID / OutputMapping / retry / rollback
//   - 与 core/workflow 合流：Recipe = 简化 Workflow
//
// 详见 docs/recipe.md。
package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// Step 是 Recipe 内的一次 Command 调用。
type Step struct {
	// ID：Recipe 内的步骤标识，用于日志与结果映射（不影响执行）。
	ID string

	// Plugin / Command：全限定 Command。
	Plugin  string
	Command string

	// Params：Command 参数（对齐 CommandSpec.InputSchema）。
	// nil 时按空对象处理。
	Params map[string]any

	// Timeout：单步超时；0 表示走 CommandSpec 默认。
	Timeout time.Duration
}

// Recipe 是一组可命名调用的 Steps。
type Recipe struct {
	ID          string
	Name        string
	Description string
	Steps       []Step
}

// Validate 校验 Recipe 的基本字段。
func (r *Recipe) Validate() error {
	if r == nil {
		return errors.New("recipe is nil")
	}
	if r.ID == "" {
		return errors.New("recipe id is empty")
	}
	if len(r.Steps) == 0 {
		return errors.New("recipe has no steps")
	}
	for i, s := range r.Steps {
		if s.Plugin == "" || s.Command == "" {
			return fmt.Errorf("step[%d]: plugin/command required", i)
		}
	}
	return nil
}

// Result 是 Recipe 的一次执行结果。
type Result struct {
	RecipeID string        `json:"recipe_id"`
	OK       bool          `json:"ok"`
	Steps    []StepResult  `json:"steps"`
	Duration time.Duration `json:"duration"`
}

// StepResult 是单步的执行结果。
type StepResult struct {
	StepID    string          `json:"step_id"`
	Plugin    string          `json:"plugin"`
	Command   string          `json:"command"`
	OK        bool            `json:"ok"`
	AuditID   string          `json:"audit_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
	ErrorMsg  string          `json:"error_msg,omitempty"`
	Duration  time.Duration   `json:"duration"`
}

// RunOptions 是 Runner.Run 的入参。
type RunOptions struct {
	// TargetID：所有 Step 共用的目标；由 Engine.ResolveConnectionMiddleware 解析。
	TargetID string
	// Caller：调用来源（UI/CLI/AI/…）。
	Caller sdk.Caller
}

// Runner 依赖 Engine 执行 Recipe 的 Steps。
type Runner struct {
	engine *command.Engine
}

// NewRunner 构造 Runner。engine 必填。
func NewRunner(engine *command.Engine) *Runner {
	return &Runner{engine: engine}
}

// Run 依次执行 recipe 的每个 Step。
// 任一 Step 失败 → 中断并返回；已完成的步骤记录在 Result.Steps 中。
func (r *Runner) Run(ctx context.Context, recipe *Recipe, opts RunOptions) (*Result, error) {
	if err := recipe.Validate(); err != nil {
		return nil, err
	}
	start := time.Now()
	res := &Result{RecipeID: recipe.ID, OK: true}
	for _, step := range recipe.Steps {
		sr := r.runStep(ctx, step, opts)
		res.Steps = append(res.Steps, sr)
		if !sr.OK {
			res.OK = false
			res.Duration = time.Since(start)
			return res, fmt.Errorf("step %q failed: %s", sr.StepID, sr.ErrorMsg)
		}
	}
	res.Duration = time.Since(start)
	return res, nil
}

func (r *Runner) runStep(ctx context.Context, step Step, opts RunOptions) StepResult {
	sr := StepResult{StepID: step.ID, Plugin: step.Plugin, Command: step.Command}
	stepStart := time.Now()

	params := step.Params
	if params == nil {
		params = map[string]any{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		sr.ErrorCode = "PARAM_MARSHAL"
		sr.ErrorMsg = err.Error()
		sr.Duration = time.Since(stepStart)
		return sr
	}

	req := command.Request{
		PluginID:  step.Plugin,
		CommandID: step.Command,
		Params:    raw,
		TargetID:  opts.TargetID,
		Caller:    opts.Caller,
		Timeout:   step.Timeout,
	}
	resp, err := r.engine.Run(ctx, req)
	sr.Duration = time.Since(stepStart)
	if err != nil {
		var se *sdk.Error
		if errors.As(err, &se) {
			sr.ErrorCode = se.Code
		} else {
			sr.ErrorCode = "STEP_FAILED"
		}
		sr.ErrorMsg = err.Error()
		return sr
	}
	sr.OK = true
	if resp != nil {
		sr.AuditID = resp.AuditID
		sr.Data = resp.Data
	}
	return sr
}
