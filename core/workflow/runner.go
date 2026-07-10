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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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
	// PhaseRollback：Workflow 主流程失败后，对某个已成功 step 执行的补偿动作。
	//   - Result 是补偿动作的独立 StepResult（StepID 与被回滚的 step 一致，前缀 "compensate:" 由上层展示自行处理）
	//   - Result.OK=true 表示补偿成功；false 表示补偿也失败（不再嵌套 rollback）
	//   - Skipped=true 表示该 step 没有声明 Compensate，静默跳过
	PhaseRollback StepPhase = "rollback"
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
	cmd     CommandExecutor
	recipe  RecipeExecutor
	history HistorySink
	nowFn   func() time.Time // 测试注入
}

// HistorySink 是 Runner 完成一次 Run 后可选调用的落盘接口。
//
// 用小接口而非直接 import history 包，避免 core/workflow 反过来依赖 history。
// history.Store 实现自然满足这个接口。
type HistorySink interface {
	SaveRun(ctx context.Context, snap *RunSnapshot) error
}

// RunSnapshot 是 Runner 传给 HistorySink 的一份结构化快照。
//
// 与 workflow.Result 语义一致，但把 Runner 侧才知道的字段（TargetID / Caller /
// Inputs / StartedAt / FinishedAt / Error）一并带上，避免 Sink 再翻译一遍。
type RunSnapshot struct {
	RunID      string
	WorkflowID string
	TargetID   string
	Caller     string
	Inputs     map[string]any
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
	OK         bool
	Error      string
	Steps      []StepResult
	Rollback   []StepResult
}

// RunnerOptions 构造 Runner 的可选参数。
type RunnerOptions struct {
	// Command / Recipe 至少一个非 nil；具体校验在 Run 时按 Step 类型触发。
	Command CommandExecutor
	Recipe  RecipeExecutor
	// History 可选：非 nil 时 Runner 会在 Run 结束后调用一次 SaveRun。
	// Sink 内部错误不会影响 Run 的返回值——写盘失败只记日志级别的容忍。
	History HistorySink
	// Now 用于测试注入固定时间；nil 时使用 time.Now。
	Now func() time.Time
}

// NewRunner 构造一个 Runner。
func NewRunner(opts RunnerOptions) *Runner {
	return &Runner{
		cmd:     opts.Command,
		recipe:  opts.Recipe,
		history: opts.History,
		nowFn:   opts.Now,
	}
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

	// CallerLabel 是一个可选的字符串描述（例："cli:alice"），仅用于历史归档；
	// 不参与执行语义。上层如需可从 Caller 派生后传入。
	CallerLabel string
}

// Run 顺序执行 Workflow。
//
// 语义：
//   - 先做 Workflow.Validate（防御性）
//   - 每步：Interpolate 参数 → 分派 Command / Recipe → 记录 StepResult
//   - 任一步失败 → 中断，返回 Result（OK=false）+ error
//   - 成功步的 out 字段挂到 scope.Steps[step.ID].Out，供后续 ${steps.<id>.out.*} 使用
//   - 若 RunnerOptions.History 非 nil，返回前会调用一次 SaveRun 落盘（失败不影响返回）
func (r *Runner) Run(ctx context.Context, w *Workflow, opts RunOptions) (*Result, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	scope := Scope{
		Inputs: cloneMap(opts.Inputs),
		Steps:  make(map[string]StepScope, len(w.Steps)),
	}

	now := r.now()
	runID := newRunID()
	start := now
	res := &Result{
		RunID:      runID,
		WorkflowID: w.ID,
		OK:         true,
		StartedAt:  start,
	}

	var runErr error
	defer func() {
		// FinishedAt / Duration 兜底填充；异常路径不至于漏字段。
		if res.FinishedAt.IsZero() {
			res.FinishedAt = r.now()
		}
		if res.Duration == 0 {
			res.Duration = res.FinishedAt.Sub(res.StartedAt)
		}
		// 主流程失败 + OnFailure.Rollback 声明存在 → 触发补偿。
		// rollback 内部错误不重置 res.OK / runErr —— 让上层知道 Workflow 仍是失败态。
		if runErr != nil && w.OnFailure != nil && len(w.OnFailure.Rollback) > 0 {
			res.Rollback = r.runRollback(ctx, w, res.Steps, scope, opts)
			// rollback 本身也可能改变 FinishedAt / Duration：更新一次。
			res.FinishedAt = r.now()
			res.Duration = res.FinishedAt.Sub(res.StartedAt)
		}
		r.saveHistory(ctx, res, opts, runErr)
	}()

	// 按并行组 顺序执行；每组内部要么单个顺序、要么并发调度
	groups := computeGroups(w.Steps)
	// 预分配 Steps：并发场景下按声明顺序回写，避免 append 竞态。
	res.Steps = make([]StepResult, len(w.Steps))
	stepDone := make([]bool, len(w.Steps))

	// 用一个 mutex 序列化 OnStep 回调与 scope 写入。
	var mu sync.Mutex
	var scopeMu sync.Mutex

	emitLocked := func(ev StepEvent) {
		if opts.OnStep == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		opts.OnStep(ev)
	}

	for _, g := range groups {
		// 顺序段：单个 step 直接跑
		if !g.Parallel {
			i := g.Indices[0]
			step := w.Steps[i]

			emitLocked(StepEvent{Phase: PhaseStart, Index: i, Step: step})
			sr := r.runStep(ctx, i, step, scope, opts)
			res.Steps[i] = sr
			stepDone[i] = true

			if !sr.OK {
				res.OK = false
				res.FinishedAt = r.now()
				res.Duration = res.FinishedAt.Sub(start)
				cause := errors.New(sr.ErrorMsg)
				emitLocked(StepEvent{
					Phase: PhaseError, Index: i, Step: step,
					Result: &res.Steps[i], Err: cause,
				})
				runErr = fmt.Errorf("step %q failed: %s", sr.StepID, sr.ErrorMsg)
				// 修剪未执行的空 slot
				res.Steps = res.Steps[:i+1]
				return res, runErr
			}
			if sr.Skipped {
				emitLocked(StepEvent{
					Phase: PhaseSkip, Index: i, Step: step, Result: &res.Steps[i],
				})
				continue
			}
			scope.Steps[step.ID] = StepScope{Out: decodeOut(sr.Data)}
			emitLocked(StepEvent{
				Phase: PhaseFinish, Index: i, Step: step, Result: &res.Steps[i],
			})
			continue
		}

		// 并行段：fail-fast + 组内 goroutine
		groupCtx, cancelGroup := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(len(g.Indices))
		for _, idx := range g.Indices {
			idx := idx
			step := w.Steps[idx]
			emitLocked(StepEvent{Phase: PhaseStart, Index: idx, Step: step})
			// 组内 step 使用一个隔离的 OnStep 代理：并发下用 mu 序列化
			innerOpts := opts
			innerOpts.OnStep = func(ev StepEvent) {
				emitLocked(ev)
			}
			// scope 只读拷贝：并发只读 inputs / 已完成 steps；组内成员不允许互引（Validate 已拦）
			scopeMu.Lock()
			scopeSnapshot := snapshotScope(scope)
			scopeMu.Unlock()

			go func() {
				defer wg.Done()
				sr := r.runStep(groupCtx, idx, step, scopeSnapshot, innerOpts)

				// 写 result slot
				res.Steps[idx] = sr
				stepDone[idx] = true

				// 成功：把 out 挂进 scope（后续组可见）
				if sr.OK && !sr.Skipped {
					scopeMu.Lock()
					scope.Steps[step.ID] = StepScope{Out: decodeOut(sr.Data)}
					scopeMu.Unlock()
				}
				// fail-fast：任一 step 失败立即取消组，避免慢兄弟继续。
				// 用 cancelGroup 幂等特性；反复 cancel 无副作用。
				if !sr.OK && !sr.Skipped {
					cancelGroup()
				}
			}()
		}
		wg.Wait()
		cancelGroup()

		// 组结束后按声明顺序 emit Finish/Skip/Error；聚合 error
		var firstFail *StepResult
		var firstFailIdx int
		for _, idx := range g.Indices {
			step := w.Steps[idx]
			sr := &res.Steps[idx]
			switch {
			case !sr.OK:
				if firstFail == nil {
					firstFail = sr
					firstFailIdx = idx
				}
				cause := errors.New(sr.ErrorMsg)
				emitLocked(StepEvent{
					Phase: PhaseError, Index: idx, Step: step, Result: sr, Err: cause,
				})
			case sr.Skipped:
				emitLocked(StepEvent{
					Phase: PhaseSkip, Index: idx, Step: step, Result: sr,
				})
			default:
				emitLocked(StepEvent{
					Phase: PhaseFinish, Index: idx, Step: step, Result: sr,
				})
			}
		}
		if firstFail != nil {
			res.OK = false
			res.FinishedAt = r.now()
			res.Duration = res.FinishedAt.Sub(start)
			// 修剪：并行组之外未执行的 slot；组内即便被 cancel 也保留失败态
			cut := 0
			for i := len(res.Steps) - 1; i >= 0; i-- {
				if stepDone[i] {
					cut = i + 1
					break
				}
			}
			if cut < len(res.Steps) {
				res.Steps = res.Steps[:cut]
			}
			runErr = fmt.Errorf("step %q failed: %s", firstFail.StepID, firstFail.ErrorMsg)
			_ = firstFailIdx
			return res, runErr
		}
	}

	res.FinishedAt = r.now()
	res.Duration = res.FinishedAt.Sub(start)
	return res, nil
}

// snapshotScope 生成一个只读快照，供并行组内的 step 使用。
//
// 并行组内不允许互引兄弟 out（Validate 已拦），因此 goroutine 只读快照就够。
// 主流程的 scope 会在组完成后由主协程串行合并成功 step 的 out。
func snapshotScope(s Scope) Scope {
	dup := Scope{
		Inputs: cloneMap(s.Inputs),
		Steps:  make(map[string]StepScope, len(s.Steps)),
	}
	for k, v := range s.Steps {
		// 只复制 map 顶层引用；out map 本身在写入后不再修改，浅拷贝安全。
		dup.Steps[k] = v
	}
	return dup
}

// -----------------------------------------------------------------------------
// history / 时间 / RunID
// -----------------------------------------------------------------------------

// now 返回当前时间，允许测试覆盖。
func (r *Runner) now() time.Time {
	if r.nowFn != nil {
		return r.nowFn()
	}
	return time.Now()
}

// newRunID 生成一个 URL-safe 的短随机 ID。
// 16 字节 = 32 hex，够小又足以避免同秒碰撞。
func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极少见；退化到时间戳保证仍然生成非空 ID。
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + hex.EncodeToString(b[:])
}

// saveHistory 是 Run 返回前的 hook；错误只写入 stderr 级别，不冒泡。
func (r *Runner) saveHistory(ctx context.Context, res *Result, opts RunOptions, runErr error) {
	if r.history == nil || res == nil {
		return
	}
	snap := &RunSnapshot{
		RunID:      res.RunID,
		WorkflowID: res.WorkflowID,
		TargetID:   opts.TargetID,
		Caller:     opts.CallerLabel,
		Inputs:     cloneMap(opts.Inputs),
		StartedAt:  res.StartedAt,
		FinishedAt: res.FinishedAt,
		Duration:   res.Duration,
		OK:         res.OK,
		Steps:      append([]StepResult(nil), res.Steps...),
		Rollback:   append([]StepResult(nil), res.Rollback...),
	}
	if runErr != nil {
		snap.Error = runErr.Error()
	}
	// 用一个独立 ctx，避免 ctx 已取消导致落盘也失败。
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = ctx // 保留参数以便未来做 trace 关联
	_ = r.history.SaveRun(saveCtx, snap)
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
// Rollback
// -----------------------------------------------------------------------------
//
// 触发条件：主流程失败（runErr != nil）+ Workflow.OnFailure.Rollback 非空。
//
// 语义：
//   - 逆序遍历 OnFailure.Rollback 中的 id 列表
//   - 只对"已经成功执行过"的 step 调用其 Compensate；Skipped / 失败 / 从未执行的都跳过
//   - Compensate 缺失：静默跳过（返回一条 Skipped=true 的记录，便于观测）
//   - Compensate 内部错误：记录到 Rollback 快照并继续下一个；不嵌套 rollback、不 retry
//
// scope 沿用主流程末态：允许 compensate 引用 ${steps.<id>.out.*}。
func (r *Runner) runRollback(ctx context.Context, w *Workflow, executed []StepResult, scope Scope, opts RunOptions) []StepResult {
	// 建立 step id → *Step 与 id → 主流程执行是否成功的映射
	stepByID := make(map[string]Step, len(w.Steps))
	for _, s := range w.Steps {
		stepByID[s.ID] = s
	}
	successOf := make(map[string]bool, len(executed))
	for _, sr := range executed {
		if sr.OK && !sr.Skipped {
			successOf[sr.StepID] = true
		}
	}

	ids := w.OnFailure.Rollback
	out := make([]StepResult, 0, len(ids))
	// 逆序遍历
	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]
		if !successOf[id] {
			// 从未成功 → 无副作用 → 无需回滚，且不记录一行冗余
			continue
		}
		step := stepByID[id]
		if step.Compensate == nil {
			// 允许"选择性补偿"：id 出现在 rollback 列表但没写 compensate 时，
			// 记一行 Skipped 便于 UI 观测，但不视为失败。
			skip := StepResult{StepID: id, OK: true, Skipped: true, Attempts: 0}
			out = append(out, skip)
			emit(opts.OnStep, StepEvent{
				Phase: PhaseRollback, Step: step, Result: &out[len(out)-1],
			})
			continue
		}
		sr := r.runCompensate(ctx, step, scope, opts)
		out = append(out, sr)
		var err error
		if !sr.OK && !sr.Skipped {
			err = errors.New(sr.ErrorMsg)
		}
		emit(opts.OnStep, StepEvent{
			Phase: PhaseRollback, Step: step, Result: &out[len(out)-1], Err: err,
		})
	}
	return out
}

// runCompensate 执行单个 Compensate 动作。
//
// 与 runStep 保持相似结构，但**永远不重试、不做 When 判断**。
// Params 插值使用与主流程相同的 scope，允许引用 ${steps.<id>.out.*}。
func (r *Runner) runCompensate(ctx context.Context, step Step, scope Scope, opts RunOptions) StepResult {
	comp := step.Compensate
	sr := StepResult{
		StepID:  step.ID,
		Command: comp.Command,
		Recipe:  comp.Recipe,
	}
	start := r.now()
	defer func() { sr.Duration = r.now().Sub(start) }()

	interpolated, err := Interpolate(comp.Params, scope)
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
	execOpts := CommandRunOptions{
		TargetID: opts.TargetID,
		Timeout:  comp.Timeout,
		Caller:   opts.Caller,
	}
	// 借用 execOnce 的分派逻辑：把 comp 临时"套壳"为一个 Step。
	fake := Step{Command: comp.Command, Recipe: comp.Recipe}
	out, err := r.execOnce(ctx, fake, params, execOpts)
	if err != nil {
		if isNonRetryableExecErr(err) {
			sr.ErrorCode = execErrCode(err)
		} else {
			sr.ErrorCode = errorCode(err)
		}
		sr.ErrorMsg = err.Error()
		return sr
	}
	sr.OK = true
	sr.Attempts = 1
	if out != nil {
		sr.AuditID = out.AuditID
		sr.Data = out.Data
	}
	return sr
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
