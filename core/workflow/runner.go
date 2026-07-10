// runner.go 实现 Workflow 主循环。
//
// v0.2 PR4 边界：
//   - 顺序执行，任一 Step 失败即中断（与 core/recipe 行为对齐）
//   - 单步流程：Interpolate(params, scope) → 判 Step.Kind →
//     调 CommandExecutor / RecipeExecutor → 反序列化 Data 挂到 scope.Steps
//   - OnStep 回调三阶段：Start / Finish / Error
//   - 不做并行 / 分支 / 回滚 / 重试（后续 PR）
//
// 为避免与 core/command、core/recipe 形成硬耦合，Runner 只依赖两个小接口
// （CommandExecutor / RecipeExecutor），生产环境由上层做 Adapter 注入。
// 这样测试时可用内存 fake 独立验证主循环行为。

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// -----------------------------------------------------------------------------
// 依赖抽象
// -----------------------------------------------------------------------------

// StepOutput 是执行器（Command / Recipe）返回给 Runner 的结果。
//
// Data：JSON 原始字节，成功时应可反序列化为 map[string]any；
//       若为空或不是 JSON 对象，则视为该 Step 无 out 字段，可为空 map。
type StepOutput struct {
	AuditID string
	Data    json.RawMessage
}

// CommandRunOptions 是执行单条 Command 时透传给底层引擎的参数。
type CommandRunOptions struct {
	TargetID string
	Timeout  time.Duration
	Caller   any // 用 any 以避免引入 sdk 依赖；上层 Adapter 自行断言
}

// CommandExecutor 抽象了一次 Command 调用。
//
// 生产环境典型实现：包装 *command.Engine.Run。
// cmdID 使用全限定形式 "<plugin>.<command>"，由 Adapter 负责拆分。
type CommandExecutor interface {
	RunCommand(ctx context.Context, cmdID string, params map[string]any, opts CommandRunOptions) (*StepOutput, error)
}

// RecipeExecutor 抽象了一次 Recipe 调用。
//
// 生产环境典型实现：包装 recipe.Runner.Run，并把 recipe.Result 序列化为 JSON。
type RecipeExecutor interface {
	RunRecipe(ctx context.Context, recipeID string, params map[string]any, opts CommandRunOptions) (*StepOutput, error)
}

// -----------------------------------------------------------------------------
// OnStep 回调
// -----------------------------------------------------------------------------

// StepPhase 标识 OnStep 回调的阶段。
type StepPhase string

const (
	// PhaseStart：Step 开始前触发，Result 为 nil。
	//
	// 注意：即使 Step 会被 when 跳过，也会触发一次 PhaseStart，紧跟 PhaseSkip。
	// 这样上层 UI 无需在渲染前预测未来行为。
	PhaseStart StepPhase = "start"
	// PhaseFinish：Step 成功完成后触发，Result 已填充且 OK=true。
	PhaseFinish StepPhase = "finish"
	// PhaseError：Step 失败后触发，Result 已填充且 OK=false，Err 非 nil。
	PhaseError StepPhase = "error"
	// PhaseSkip：Step 因 when 求值为 false 而被跳过，Result 已填充且
	// OK=true、Skipped=true；Err 为 nil。
	PhaseSkip StepPhase = "skip"
	// PhaseRetry：Step 一次执行失败但仍在 retry 预算内即将退避重试。
	//   - Result 是"该次失败"的中间态（OK=false，ErrorCode/Msg 已填充）
	//   - Err 是本次的原始错误
	//   - Attempt / MaxAttempts / NextBackoff 见 StepEvent
	// 若 backoff 期间 ctx 被取消，随后不会再触发 PhaseError；
	// runStep 会以最后一次错误直接返回。
	PhaseRetry StepPhase = "retry"
)

// StepEvent 是单个 Step 生命周期事件。
type StepEvent struct {
	Phase  StepPhase
	Index  int
	Step   Step
	Result *StepResult // Finish / Error / Skip / Retry 时填充
	Err    error       // Error / Retry 时填充

	// Attempt 是当前尝试次数（1 起）。仅 PhaseRetry 有意义：表示"刚失败的这次是第几次"。
	Attempt int
	// MaxAttempts 是策略允许的总尝试次数。仅 PhaseRetry 有意义。
	MaxAttempts int
	// NextBackoff 是即将 sleep 的时长；PhaseRetry 场景使用。
	NextBackoff time.Duration
}

// OnStepFunc 是 Runner 的观察回调。
// 回调应轻量、不阻塞；耗时操作请另起 goroutine。
type OnStepFunc func(ev StepEvent)

// -----------------------------------------------------------------------------
// Runner
// -----------------------------------------------------------------------------

// Runner 是 Workflow 执行引擎。
type Runner struct {
	cmd    CommandExecutor
	recipe RecipeExecutor
}

// RunnerOptions 构造 Runner 的可选参数。
type RunnerOptions struct {
	// Command / Recipe 至少一个非 nil；具体校验在 Run 时按 Step 类型触发。
	Command CommandExecutor
	Recipe  RecipeExecutor
}

// NewRunner 构造一个 Runner。
func NewRunner(opts RunnerOptions) *Runner {
	return &Runner{cmd: opts.Command, recipe: opts.Recipe}
}

// RunOptions 是 Workflow 单次执行的可选参数。
type RunOptions struct {
	// Inputs：${inputs.*} 的取值来源。
	Inputs map[string]any
	// TargetID：所有 Step 共用的默认目标；Step 层暂无覆盖机制。
	TargetID string
	// Caller：透传给底层执行器，用于审计与来源追踪。
	Caller any
	// OnStep：观察回调；nil 时不触发。
	OnStep OnStepFunc
}

// Run 顺序执行 Workflow。
//
// 语义：
//   - 先做 Workflow.Validate（防御性）
//   - 每步：Interpolate 参数 → 分派 Command / Recipe → 记录 StepResult
//   - 任一步失败 → 中断，返回 Result（OK=false）+ error
//   - 成功步的 out 字段挂到 scope.Steps[step.ID].Out，供后续 ${steps.<id>.out.*} 使用
func (r *Runner) Run(ctx context.Context, w *Workflow, opts RunOptions) (*Result, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	scope := Scope{
		Inputs: cloneMap(opts.Inputs),
		Steps:  make(map[string]StepScope, len(w.Steps)),
	}

	start := time.Now()
	res := &Result{WorkflowID: w.ID, OK: true}

	for i, step := range w.Steps {
		emit(opts.OnStep, StepEvent{Phase: PhaseStart, Index: i, Step: step})

		sr := r.runStep(ctx, i, step, scope, opts)
		res.Steps = append(res.Steps, sr)

		if !sr.OK {
			res.OK = false
			res.Duration = time.Since(start)
			cause := errors.New(sr.ErrorMsg)
			emit(opts.OnStep, StepEvent{
				Phase: PhaseError, Index: i, Step: step,
				Result: &res.Steps[len(res.Steps)-1], Err: cause,
			})
			return res, fmt.Errorf("step %q failed: %s", sr.StepID, sr.ErrorMsg)
		}

		// 跳过：不写 scope.Steps.<id>.out（避免后续 step 误引用不存在的字段），
		// 只广播一次 PhaseSkip 供 UI/CLI 渲染。
		if sr.Skipped {
			emit(opts.OnStep, StepEvent{
				Phase: PhaseSkip, Index: i, Step: step,
				Result: &res.Steps[len(res.Steps)-1],
			})
			continue
		}

		// 成功：把 out 挂进作用域
		scope.Steps[step.ID] = StepScope{Out: decodeOut(sr.Data)}

		emit(opts.OnStep, StepEvent{
			Phase: PhaseFinish, Index: i, Step: step,
			Result: &res.Steps[len(res.Steps)-1],
		})
	}

	res.Duration = time.Since(start)
	return res, nil
}

// -----------------------------------------------------------------------------
// 单步执行
// -----------------------------------------------------------------------------

func (r *Runner) runStep(ctx context.Context, index int, step Step, scope Scope, opts RunOptions) StepResult {
	sr := StepResult{StepID: step.ID, Command: step.Command, Recipe: step.Recipe}
	stepStart := time.Now()
	defer func() { sr.Duration = time.Since(stepStart) }()

	// 0. When：可选条件表达式。空 → 无条件执行；false → 跳过；求值失败 → 中断。
	if step.When != "" {
		ok, err := EvalBool(step.When, scope)
		if err != nil {
			sr.ErrorCode = "WHEN_EVAL"
			sr.ErrorMsg = err.Error()
			return sr
		}
		if !ok {
			sr.OK = true
			sr.Skipped = true
			return sr
		}
	}

	// 1. 插值（重试无关：参数解析失败没有重试意义）
	interpolated, err := Interpolate(step.Params, scope)
	if err != nil {
		sr.ErrorCode = "INTERPOLATE"
		sr.ErrorMsg = err.Error()
		return sr
	}
	params, err := asStringMap(interpolated)
	if err != nil {
		sr.ErrorCode = "INTERPOLATE"
		sr.ErrorMsg = err.Error()
		return sr
	}

	// 2. 分派 + 重试循环
	execOpts := CommandRunOptions{
		TargetID: opts.TargetID,
		Timeout:  step.Timeout,
		Caller:   opts.Caller,
	}
	maxAttempts := step.Retry.attempts()
	var (
		out     *StepOutput
		lastErr error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		sr.Attempts = attempt
		out, lastErr = r.execOnce(ctx, step, params, execOpts)

		// 声明性错误（executor 缺失 / 步骤不合法）—— 重试也无用，直接返回。
		if lastErr != nil && isNonRetryableExecErr(lastErr) {
			sr.ErrorCode = execErrCode(lastErr)
			sr.ErrorMsg = lastErr.Error()
			return sr
		}

		if lastErr == nil {
			// 成功：清空之前失败态并跳出。
			sr.OK = true
			sr.ErrorCode = ""
			sr.ErrorMsg = ""
			if out != nil {
				sr.AuditID = out.AuditID
				sr.Data = out.Data
			}
			return sr
		}

		// 有更多重试机会 → PhaseRetry；否则跳出循环让上层走 PhaseError。
		if attempt < maxAttempts {
			// 先在 sr 上留下失败态方便回调观察
			sr.ErrorCode = errorCode(lastErr)
			sr.ErrorMsg = lastErr.Error()

			backoff := step.Retry.nextBackoff(attempt)
			emit(opts.OnStep, StepEvent{
				Phase: PhaseRetry, Index: index, Step: step,
				Result: &sr, Err: lastErr,
				Attempt: attempt, MaxAttempts: maxAttempts, NextBackoff: backoff,
			})
			if err := sleepCtx(ctx, backoff); err != nil {
				// ctx 已取消 / 超时：以最后一次执行错误返回，不再重试。
				return sr
			}
			continue
		}
	}

	// 走到这里说明用尽 max 次仍失败。
	sr.ErrorCode = errorCode(lastErr)
	sr.ErrorMsg = lastErr.Error()
	return sr
}

// execOnce 是"一次真正的执行"：参数已解析，返回底层 executor 的原始错误。
//
// 分离这一层是为了让重试循环只关心成功 / 失败，无需重复参数解析开销。
func (r *Runner) execOnce(ctx context.Context, step Step, params map[string]any, execOpts CommandRunOptions) (*StepOutput, error) {
	switch {
	case step.Command != "":
		if r.cmd == nil {
			return nil, &execConfigErr{code: "NO_EXECUTOR", msg: "no CommandExecutor configured"}
		}
		return r.cmd.RunCommand(ctx, step.Command, params, execOpts)
	case step.Recipe != "":
		if r.recipe == nil {
			return nil, &execConfigErr{code: "NO_EXECUTOR", msg: "no RecipeExecutor configured"}
		}
		return r.recipe.RunRecipe(ctx, step.Recipe, params, execOpts)
	default:
		// Validate 已拦截，这里作为兜底防御。
		return nil, &execConfigErr{code: "INVALID_STEP", msg: "step has neither command nor recipe"}
	}
}

// -----------------------------------------------------------------------------
// 声明性 / 配置错误：与业务错误分开，永远不重试
// -----------------------------------------------------------------------------

// execConfigErr 是 execOnce 内部产生的"声明性错误"载体。
// 它实现 CodedError 以便被 errorCode() 提取，同时被 isNonRetryableExecErr 命中。
type execConfigErr struct {
	code string
	msg  string
}

func (e *execConfigErr) Error() string     { return e.msg }
func (e *execConfigErr) ErrorCode() string { return e.code }

// isNonRetryableExecErr 判断 execOnce 返回的错误是否绝不重试。
func isNonRetryableExecErr(err error) bool {
	_, ok := err.(*execConfigErr)
	return ok
}

// execErrCode 是 execConfigErr → code 的直取工具（用于快路径）。
func execErrCode(err error) string {
	if e, ok := err.(*execConfigErr); ok {
		return e.code
	}
	return errorCode(err)
}

// sleepCtx 在 ctx 未取消的前提下 sleep d；若 d<=0 立即返回 nil。
// ctx 结束时返回 ctx.Err()。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		// 即便零间隔也尊重 ctx 取消
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// -----------------------------------------------------------------------------
// 工具函数
// -----------------------------------------------------------------------------

// emit 是 OnStep 回调的空指针防护包装。
func emit(fn OnStepFunc, ev StepEvent) {
	if fn == nil {
		return
	}
	fn(ev)
}

// decodeOut 尝试把 Step 的 Data 反序列化为 map[string]any 作为 out 字段。
// 非 JSON 对象或空数据均返回空 map，不视为错误。
func decodeOut(data json.RawMessage) map[string]any {
	if len(data) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// asStringMap 把插值后的 params 转成 map[string]any（Step.Params 声明本就是它）。
// 若 v 为 nil，返回空 map。
func asStringMap(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("interpolated params must be map[string]any, got %T", v)
	}
	return m, nil
}

// cloneMap 浅拷贝，避免 Runner 修改调用方传入的 map。
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CodedError 由底层执行器返回的错误可选实现，用于把稳定错误码
// 透传到 StepResult.ErrorCode（例：sdk.Error 的 Code 字段可包一层）。
type CodedError interface {
	error
	ErrorCode() string
}

// errorCode 尝试从错误中提取一个"错误码"字符串。
//
// 优先级：
//  1. errors.As 到 CodedError
//  2. 无法解析时统一为 "STEP_FAILED"
func errorCode(err error) string {
	var c CodedError
	if errors.As(err, &c) && c.ErrorCode() != "" {
		return c.ErrorCode()
	}
	return "STEP_FAILED"
}
