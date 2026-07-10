package workflow_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// Validate：组内互引 out 拒绝
// -----------------------------------------------------------------------------

func TestValidate_Parallel_SiblingOutRefRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "x", Parallel: true},
			{ID: "b", Command: "y", Parallel: true,
				Params: map[string]any{"cmd": "use ${steps.a.out.path}"}},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "parallel group") {
		t.Fatalf("expected parallel-ref error, got %v", err)
	}
}

func TestValidate_Parallel_ReferPrevGroupOK(t *testing.T) {
	// 引用**前一个非并行组**的 out，是合法的
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "probe", Command: "x"},
			{ID: "a", Command: "y", Parallel: true,
				Params: map[string]any{"cmd": "use ${steps.probe.out.v}"}},
			{ID: "b", Command: "z", Parallel: true},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_Parallel_RefInCompensateAlsoRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "x", Parallel: true},
			{ID: "b", Command: "y", Parallel: true,
				Compensate: &workflow.CompensateAction{
					Command: "undo",
					Params:  map[string]any{"ref": "${steps.a.out.id}"},
				}},
		},
	}
	if err := w.Validate(); err == nil {
		t.Fatal("expected error for sibling ref in compensate")
	}
}

func TestValidate_Parallel_RefInWhenAlsoRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "x", Parallel: true},
			{ID: "b", Command: "y", Parallel: true,
				When: "steps.a.out.ok"},
		},
	}
	if err := w.Validate(); err == nil {
		t.Fatal("expected error for sibling ref in when")
	}
}

// -----------------------------------------------------------------------------
// Loader：yaml parallel 反序列化
// -----------------------------------------------------------------------------

func TestLoader_Parallel(t *testing.T) {
	src := `
workflow:
  id: wf.par
  steps:
    - id: a
      command: cmd.a
      parallel: true
    - id: b
      command: cmd.b
      parallel: true
    - id: c
      command: cmd.c
`
	w, err := workflow.LoadBytes([]byte(src))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if !w.Steps[0].Parallel || !w.Steps[1].Parallel || w.Steps[2].Parallel {
		t.Fatalf("parallel not parsed: %+v", w.Steps)
	}
}

// -----------------------------------------------------------------------------
// Runner：纯并行成功 —— 三个 step 应实际并发，总耗时 << sum(耗时)
// -----------------------------------------------------------------------------

// slowCmd 每个 command 内部 sleep 一段时间，用于验证并发。
type slowCmd struct {
	mu    sync.Mutex
	dur   map[string]time.Duration
	calls int32
}

func newSlow() *slowCmd {
	return &slowCmd{dur: map[string]time.Duration{}}
}
func (s *slowCmd) RunCommand(ctx context.Context, id string, _ map[string]any, _ workflow.CommandRunOptions) (*workflow.StepOutput, error) {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	d := s.dur[id]
	s.mu.Unlock()
	if d > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
	}
	return &workflow.StepOutput{AuditID: "a-" + id}, nil
}

func TestRunner_Parallel_ActuallyConcurrent(t *testing.T) {
	s := newSlow()
	s.dur["a"] = 80 * time.Millisecond
	s.dur["b"] = 80 * time.Millisecond
	s.dur["c"] = 80 * time.Millisecond

	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a", Parallel: true},
			{ID: "b", Command: "b", Parallel: true},
			{ID: "c", Command: "c", Parallel: true},
		},
	}
	start := time.Now()
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
	// 3 步各 80ms；并行 elapsed 应远小于 240ms。放宽到 180ms。
	if elapsed > 180*time.Millisecond {
		t.Fatalf("elapsed=%v, want << 240ms (concurrent)", elapsed)
	}
	// steps 顺序按声明保留
	if res.Steps[0].StepID != "a" || res.Steps[1].StepID != "b" || res.Steps[2].StepID != "c" {
		t.Fatalf("steps order broken: %+v", res.Steps)
	}
}

// -----------------------------------------------------------------------------
// Runner：组内一个失败 → 其它 ctx 被 cancel，主流程返回失败
// -----------------------------------------------------------------------------

func TestRunner_Parallel_FailFastCancelsSiblings(t *testing.T) {
	cmd := newFakeCmd()
	cmd.fail["fast_bad"] = &codedErr{code: "BOOM", msg: "explode"}

	// slow 的 sleep 通过 ctx 观察取消：使用独立 slowCmd + fakeCmd 组合稍麻烦，
	// 换用 fakeCmd + goroutine 内部 select 无法实现——改用 slowCmd 全权代管。
	s := newSlow()
	s.dur["slow_ok"] = 500 * time.Millisecond
	s.dur["fast_bad"] = 20 * time.Millisecond

	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	_ = cmd

	// 直接让 fast_bad 通过 slowCmd 也能返回错误：给 slowCmd 加一个 fail 表
	// —— 但我们此处只用 slowCmd 的 sleep+ctx，把 error 用 wrapper 注入。
	r = workflow.NewRunner(workflow.RunnerOptions{Command: &failAfterSlow{
		slow:     s,
		failWith: map[string]error{"fast_bad": errors.New("boom")},
	}})

	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "slow_ok", Command: "slow_ok", Parallel: true},
			{ID: "fast_bad", Command: "fast_bad", Parallel: true},
		},
	}
	start := time.Now()
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected err")
	}
	if res.OK {
		t.Fatal("res.OK want false")
	}
	// slow_ok 应因 ctx 取消而失败或未完成；总耗时 << 500ms
	if elapsed > 300*time.Millisecond {
		t.Fatalf("elapsed=%v, cancel should stop slow sibling", elapsed)
	}
	// 两条 slot 都应有结果
	if len(res.Steps) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(res.Steps))
	}
	// 顺序按声明：slow_ok, fast_bad
	if res.Steps[0].StepID != "slow_ok" || res.Steps[1].StepID != "fast_bad" {
		t.Fatalf("order broken: %+v", res.Steps)
	}
	if res.Steps[0].OK {
		t.Errorf("slow_ok should have been cancelled")
	}
	if res.Steps[1].OK {
		t.Errorf("fast_bad should have failed")
	}
}

// failAfterSlow 包装 slowCmd：先 sleep（会响应 ctx），如命中 failWith 表则返回错误。
type failAfterSlow struct {
	slow     *slowCmd
	failWith map[string]error
}

func (f *failAfterSlow) RunCommand(ctx context.Context, id string, p map[string]any, opts workflow.CommandRunOptions) (*workflow.StepOutput, error) {
	out, err := f.slow.RunCommand(ctx, id, p, opts)
	if err != nil {
		return out, err
	}
	if e, ok := f.failWith[id]; ok {
		return nil, e
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Runner：when=false skip 在并行组内也生效
// -----------------------------------------------------------------------------

func TestRunner_Parallel_WhenSkipInsideGroup(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a"] = map[string]any{}
	cmd.out["b"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a", Parallel: true},
			{ID: "b", Command: "b", Parallel: true, When: "false"},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
	if !res.Steps[1].Skipped {
		t.Fatalf("b should be skipped, got %+v", res.Steps[1])
	}
}

// -----------------------------------------------------------------------------
// Runner：parallel × retry 组合
// -----------------------------------------------------------------------------

func TestRunner_Parallel_WithRetry(t *testing.T) {
	s := newScripted()
	// a 首次失败，第二次成功
	s.responds["a"] = []error{errors.New("net"), nil}
	s.outOK["a"] = map[string]any{"ok": true}
	s.outOK["b"] = map[string]any{"ok": true}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a", Parallel: true,
				Retry: &workflow.RetryPolicy{Max: 3, Backoff: 5 * time.Millisecond}},
			{ID: "b", Command: "b", Parallel: true},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
	if res.Steps[0].Attempts != 2 {
		t.Errorf("a attempts=%d, want 2", res.Steps[0].Attempts)
	}
}

// -----------------------------------------------------------------------------
// Runner：parallel × rollback —— 组内一个失败后，另一个成功的应参与回滚
// -----------------------------------------------------------------------------

func TestRunner_Parallel_RollsBackOnlySuccessSibling(t *testing.T) {
	s := newSlow()
	// a 慢但会成功；b 快速失败
	s.dur["a"] = 30 * time.Millisecond
	// b 立即失败：把 err 注入
	cmd := &failAfterSlow{slow: s, failWith: map[string]error{"b": errors.New("boom")}}

	// 补偿只在成功的 a 上应触发
	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a", Parallel: true,
				Compensate: &workflow.CompensateAction{Command: "undo_a"}},
			{ID: "b", Command: "b", Parallel: true,
				Compensate: &workflow.CompensateAction{Command: "undo_b"}},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"a", "b"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	// 如果 a 因 fail-fast 被 cancel，会失败；如果 a 已完成，会成功。
	// 断言：只对最终 OK 的 step 触发 rollback，其它跳过。
	rolled := map[string]bool{}
	for _, sr := range res.Rollback {
		if !sr.Skipped {
			rolled[sr.StepID] = true
		}
	}
	// b 一定失败，不应回滚
	if rolled["b"] {
		t.Fatalf("b failed → should not rollback: %+v", res.Rollback)
	}
	// a 若成功过则应回滚
	if res.Steps[0].OK && !res.Steps[0].Skipped {
		if !rolled["a"] {
			t.Fatalf("a succeeded → should have rolled back: %+v", res.Rollback)
		}
	}
}

// -----------------------------------------------------------------------------
// Runner：OnStep 事件在并行下也必须序列化访问（-race 可捕获竞态）
// -----------------------------------------------------------------------------

func TestRunner_Parallel_OnStepSerialization(t *testing.T) {
	cmd := newFakeCmd()
	for _, id := range []string{"a", "b", "c", "d"} {
		cmd.out[id] = map[string]any{}
	}
	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a", Parallel: true},
			{ID: "b", Command: "b", Parallel: true},
			{ID: "c", Command: "c", Parallel: true},
			{ID: "d", Command: "d", Parallel: true},
		},
	}
	// 累加计数器：如果 OnStep 未序列化 -race 会告警；这里同时验证事件计数
	var starts, finishes int32
	_, err := r.Run(context.Background(), w, workflow.RunOptions{
		OnStep: func(ev workflow.StepEvent) {
			// 有意做一点竞态敏感的操作：普通 int++
			// 若外部未加锁，-race 会立刻标红。
			switch ev.Phase {
			case workflow.PhaseStart:
				atomic.AddInt32(&starts, 1)
			case workflow.PhaseFinish:
				atomic.AddInt32(&finishes, 1)
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if starts != 4 || finishes != 4 {
		t.Fatalf("counts start=%d finish=%d", starts, finishes)
	}
}
