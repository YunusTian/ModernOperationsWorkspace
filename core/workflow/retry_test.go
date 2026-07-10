package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// RetryPolicy 静态：Validate / attempts / nextBackoff
// -----------------------------------------------------------------------------

func TestRetryPolicy_Validate(t *testing.T) {
	tests := []struct {
		name    string
		p       *workflow.RetryPolicy
		wantErr bool
	}{
		{"nil", nil, false},
		{"max=0 ok", &workflow.RetryPolicy{}, false},
		{"max=1 ok", &workflow.RetryPolicy{Max: 1}, false},
		{"max=3 backoff=1s ok", &workflow.RetryPolicy{Max: 3, Backoff: time.Second}, false},
		{"max<0", &workflow.RetryPolicy{Max: -1}, true},
		{"max>20", &workflow.RetryPolicy{Max: 21}, true},
		{"backoff<0", &workflow.RetryPolicy{Max: 3, Backoff: -time.Second}, true},
		{"exp needs backoff", &workflow.RetryPolicy{Max: 3, Exponential: true}, true},
		{"backoff>max_backoff", &workflow.RetryPolicy{Max: 3, Backoff: 5 * time.Second, MaxBackoff: time.Second}, true},
		{"exp ok", &workflow.RetryPolicy{Max: 4, Backoff: 100 * time.Millisecond, MaxBackoff: time.Second, Exponential: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Runner + retry：核心行为
// -----------------------------------------------------------------------------

// scriptedCmd 让同一个 cmdID 依次返回不同的错误 / 成功。
type scriptedCmd struct {
	mu       sync.Mutex
	responds map[string][]error // id -> 按次序的错误；nil 表示成功
	callN    map[string]int
	outOK    map[string]map[string]any
}

func newScripted() *scriptedCmd {
	return &scriptedCmd{
		responds: map[string][]error{},
		callN:    map[string]int{},
		outOK:    map[string]map[string]any{},
	}
}
func (s *scriptedCmd) RunCommand(_ context.Context, id string, params map[string]any, _ workflow.CommandRunOptions) (*workflow.StepOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.callN[id]
	s.callN[id] = n + 1
	seq := s.responds[id]
	if n < len(seq) && seq[n] != nil {
		return nil, seq[n]
	}
	m := s.outOK[id]
	if m == nil {
		m = map[string]any{}
	}
	data, _ := json.Marshal(m)
	return &workflow.StepOutput{AuditID: "a-" + id, Data: data}, nil
}

// -----------------------------------------------------------------------------
// 测试：max=3 首次失败第二次成功
// -----------------------------------------------------------------------------

func TestRunner_Retry_SucceedOnSecondAttempt(t *testing.T) {
	s := newScripted()
	s.responds["flaky"] = []error{errors.New("io error")} // 第 1 次失败；第 2 次落到 nil -> 成功
	s.outOK["flaky"] = map[string]any{"ok": true}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "flaky", Retry: &workflow.RetryPolicy{Max: 3}},
		},
	}

	var retries []workflow.StepEvent
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		OnStep: func(ev workflow.StepEvent) {
			if ev.Phase == workflow.PhaseRetry {
				retries = append(retries, ev)
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK || !res.Steps[0].OK {
		t.Fatalf("expected step OK, got %+v", res.Steps[0])
	}
	if res.Steps[0].Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2", res.Steps[0].Attempts)
	}
	// PhaseRetry 只应发生 1 次（第一次失败后）
	if len(retries) != 1 || retries[0].Attempt != 1 || retries[0].MaxAttempts != 3 {
		t.Fatalf("retries = %+v", retries)
	}
	// 成功时清理错误码
	if res.Steps[0].ErrorCode != "" || res.Steps[0].ErrorMsg != "" {
		t.Errorf("success should clear error fields: %+v", res.Steps[0])
	}
}

// -----------------------------------------------------------------------------
// 测试：用尽 max 次仍失败 → PhaseError，Attempts=max
// -----------------------------------------------------------------------------

func TestRunner_Retry_ExhaustedFails(t *testing.T) {
	s := newScripted()
	s.responds["always"] = []error{
		&codedErr{code: "NET", msg: "1st"},
		&codedErr{code: "NET", msg: "2nd"},
		&codedErr{code: "NET", msg: "3rd"},
	}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "always", Retry: &workflow.RetryPolicy{Max: 3}},
		},
	}

	var (
		retries    int
		errPhases  int
		startCount int
	)
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		OnStep: func(ev workflow.StepEvent) {
			switch ev.Phase {
			case workflow.PhaseStart:
				startCount++
			case workflow.PhaseRetry:
				retries++
			case workflow.PhaseError:
				errPhases++
			}
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if res.OK {
		t.Fatal("res.OK want false")
	}
	if res.Steps[0].Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", res.Steps[0].Attempts)
	}
	if !strings.Contains(res.Steps[0].ErrorMsg, "3rd") {
		t.Errorf("ErrorMsg should reflect last attempt: %q", res.Steps[0].ErrorMsg)
	}
	if res.Steps[0].ErrorCode != "NET" {
		t.Errorf("ErrorCode = %q, want NET", res.Steps[0].ErrorCode)
	}
	// PhaseStart=1, PhaseRetry=2, PhaseError=1
	if startCount != 1 || retries != 2 || errPhases != 1 {
		t.Errorf("phase counts: start=%d retry=%d error=%d", startCount, retries, errPhases)
	}
}

// -----------------------------------------------------------------------------
// 测试：backoff 时长 —— fixed
// -----------------------------------------------------------------------------

func TestRunner_Retry_FixedBackoffDuration(t *testing.T) {
	s := newScripted()
	s.responds["net"] = []error{errors.New("1"), errors.New("2"), nil}
	s.outOK["net"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "net", Retry: &workflow.RetryPolicy{
				Max: 3, Backoff: 40 * time.Millisecond,
			}},
		},
	}

	start := time.Now()
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	total := time.Since(start)

	if res.Steps[0].Attempts != 3 {
		t.Fatalf("Attempts=%d", res.Steps[0].Attempts)
	}
	// 至少 2 次 backoff × 40ms = 80ms
	if total < 80*time.Millisecond {
		t.Fatalf("total=%v, want >= 80ms", total)
	}
	// 上限宽松：< 1s
	if total > time.Second {
		t.Fatalf("total=%v, unreasonably long", total)
	}
}

// -----------------------------------------------------------------------------
// 测试：exponential + max_backoff 封顶
// -----------------------------------------------------------------------------

func TestRunner_Retry_ExponentialCapped(t *testing.T) {
	s := newScripted()
	s.responds["net"] = []error{errors.New("1"), errors.New("2"), errors.New("3"), nil}
	s.outOK["net"] = map[string]any{}

	// backoff=10ms, exponential, max_backoff=25ms
	// 第 1 次退避=10ms；第 2 次=20ms；第 3 次=40ms → 封到 25ms
	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "net", Retry: &workflow.RetryPolicy{
				Max: 4, Backoff: 10 * time.Millisecond,
				MaxBackoff: 25 * time.Millisecond, Exponential: true,
			}},
		},
	}

	var backoffs []time.Duration
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		OnStep: func(ev workflow.StepEvent) {
			if ev.Phase == workflow.PhaseRetry {
				backoffs = append(backoffs, ev.NextBackoff)
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond}
	if len(backoffs) != len(want) {
		t.Fatalf("backoffs=%v, want %v", backoffs, want)
	}
	for i, d := range backoffs {
		if d != want[i] {
			t.Errorf("backoff[%d]=%v, want %v", i, d, want[i])
		}
	}
}

// -----------------------------------------------------------------------------
// 测试：ctx 取消提前退出 backoff
// -----------------------------------------------------------------------------

func TestRunner_Retry_ContextCancelDuringBackoff(t *testing.T) {
	s := newScripted()
	// 都失败：模拟持续错误
	s.responds["net"] = []error{errors.New("e1"), errors.New("e2"), errors.New("e3")}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "net", Retry: &workflow.RetryPolicy{
				Max: 5, Backoff: 500 * time.Millisecond,
			}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	// 30ms 后取消：应该发生在第一次 backoff 期间
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res, err := r.Run(ctx, w, workflow.RunOptions{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error")
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("elapsed=%v, cancel should stop backoff early", elapsed)
	}
	// 应仅执行一次
	if res.Steps[0].Attempts != 1 {
		t.Fatalf("Attempts=%d, want 1", res.Steps[0].Attempts)
	}
}

// -----------------------------------------------------------------------------
// 测试：WHEN_EVAL 错误不重试
// -----------------------------------------------------------------------------

func TestRunner_Retry_WhenEvalNotRetried(t *testing.T) {
	s := newScripted()
	r := workflow.NewRunner(workflow.RunnerOptions{Command: s})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "x", When: "no_such_var == 1",
				Retry: &workflow.RetryPolicy{Max: 5, Backoff: 10 * time.Millisecond}},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	if res.Steps[0].ErrorCode != "WHEN_EVAL" {
		t.Fatalf("ErrorCode = %q", res.Steps[0].ErrorCode)
	}
	if res.Steps[0].Attempts != 0 {
		t.Errorf("Attempts=%d, want 0 (when eval never triggered exec)", res.Steps[0].Attempts)
	}
}

// -----------------------------------------------------------------------------
// 测试：NO_EXECUTOR 不重试
// -----------------------------------------------------------------------------

func TestRunner_Retry_NoExecutorNotRetried(t *testing.T) {
	r := workflow.NewRunner(workflow.RunnerOptions{}) // 都没配
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "x",
				Retry: &workflow.RetryPolicy{Max: 5, Backoff: 10 * time.Millisecond}},
		},
	}
	start := time.Now()
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected err")
	}
	if res.Steps[0].ErrorCode != "NO_EXECUTOR" {
		t.Errorf("ErrorCode = %q", res.Steps[0].ErrorCode)
	}
	// 没有 backoff，应该立刻返回
	if elapsed > 50*time.Millisecond {
		t.Errorf("elapsed=%v, should not backoff on NO_EXECUTOR", elapsed)
	}
	// Attempts 记 1（第一次尝试即遇到声明性错误）
	if res.Steps[0].Attempts != 1 {
		t.Errorf("Attempts=%d, want 1", res.Steps[0].Attempts)
	}
}

// -----------------------------------------------------------------------------
// Loader：retry 字段完整解析
// -----------------------------------------------------------------------------

func TestLoader_Retry(t *testing.T) {
	src := `
workflow:
  id: wf.retry
  steps:
    - id: s1
      command: a.b
      retry:
        max: 4
        backoff: 500ms
        max_backoff: 5s
        exponential: true
`
	w, err := workflow.LoadBytes([]byte(src))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	rp := w.Steps[0].Retry
	if rp == nil {
		t.Fatal("Retry not parsed")
	}
	if rp.Max != 4 || rp.Backoff != 500*time.Millisecond ||
		rp.MaxBackoff != 5*time.Second || !rp.Exponential {
		t.Fatalf("retry parsed = %+v", rp)
	}
}

func TestLoader_Retry_InvalidRejected(t *testing.T) {
	src := `
workflow:
  id: wf.bad
  steps:
    - id: s1
      command: a.b
      retry:
        max: 3
        exponential: true
`
	// exponential=true 但 backoff=0，Validate 应拒
	_, err := workflow.LoadBytes([]byte(src))
	if err == nil {
		t.Fatal("expected validate error")
	}
	if !strings.Contains(err.Error(), "backoff") {
		t.Fatalf("err should mention backoff, got %v", err)
	}
}
