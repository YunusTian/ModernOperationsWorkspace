package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// 内存 fake：CommandExecutor / RecipeExecutor
// -----------------------------------------------------------------------------

type fakeCall struct {
	Kind   string // "command" | "recipe"
	ID     string
	Params map[string]any
	Opts   workflow.CommandRunOptions
}

// fakeCommandExecutor：按 cmdID 查表决定返回值或错误。
type fakeCommandExecutor struct {
	mu    sync.Mutex
	calls []fakeCall

	// out: cmdID -> map[string]any（会被序列化成 JSON）
	out map[string]map[string]any
	// fail: cmdID -> 要返回的错误
	fail map[string]error
}

func newFakeCmd() *fakeCommandExecutor {
	return &fakeCommandExecutor{
		out:  make(map[string]map[string]any),
		fail: make(map[string]error),
	}
}

func (f *fakeCommandExecutor) RunCommand(_ context.Context, cmdID string, params map[string]any, opts workflow.CommandRunOptions) (*workflow.StepOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Kind: "command", ID: cmdID, Params: params, Opts: opts})
	f.mu.Unlock()

	if err, ok := f.fail[cmdID]; ok {
		return nil, err
	}
	data, _ := json.Marshal(f.out[cmdID])
	return &workflow.StepOutput{AuditID: "audit-" + cmdID, Data: data}, nil
}

type fakeRecipeExecutor struct {
	mu    sync.Mutex
	calls []fakeCall
	out   map[string]map[string]any
	fail  map[string]error
}

func newFakeRecipe() *fakeRecipeExecutor {
	return &fakeRecipeExecutor{
		out:  make(map[string]map[string]any),
		fail: make(map[string]error),
	}
}

func (f *fakeRecipeExecutor) RunRecipe(_ context.Context, id string, params map[string]any, opts workflow.CommandRunOptions) (*workflow.StepOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Kind: "recipe", ID: id, Params: params, Opts: opts})
	f.mu.Unlock()

	if err, ok := f.fail[id]; ok {
		return nil, err
	}
	data, _ := json.Marshal(f.out[id])
	return &workflow.StepOutput{AuditID: "audit-recipe-" + id, Data: data}, nil
}

// codedErr 用于测试错误码透传。
type codedErr struct {
	code string
	msg  string
}

func (e *codedErr) Error() string     { return e.msg }
func (e *codedErr) ErrorCode() string { return e.code }

// -----------------------------------------------------------------------------
// 顺序执行 / 变量传递
// -----------------------------------------------------------------------------

func TestRunner_SequentialAndVarPassing(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["ssh.upload"] = map[string]any{"bytes_sent": float64(2048), "path": "/tmp/a.tar"}
	cmd.out["ssh.exec"] = map[string]any{"exit_code": float64(0)}

	rcp := newFakeRecipe()
	rcp.out["file.backup"] = map[string]any{"snapshot": "snap-01"}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd, Recipe: rcp})

	w := &workflow.Workflow{
		ID: "deploy",
		Inputs: []workflow.Input{
			{Name: "service"},
			{Name: "package"},
		},
		Steps: []workflow.Step{
			{
				ID: "upload", Command: "ssh.upload",
				Params: map[string]any{"file": "${inputs.package}"},
			},
			{
				ID: "stop", Command: "ssh.exec",
				// 引用上一步 out 与 inputs
				Params: map[string]any{
					"cmd":     "systemctl stop ${inputs.service}",
					"context": "sent=${steps.upload.out.bytes_sent}",
				},
			},
			{
				ID: "backup", Recipe: "file.backup",
				Params: map[string]any{"src": "${steps.upload.out.path}"},
			},
		},
	}

	var events []workflow.StepEvent
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		Inputs: map[string]any{
			"service": "myapp",
			"package": "app.tar",
		},
		TargetID: "host-01",
		OnStep:   func(ev workflow.StepEvent) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatalf("res.OK = false")
	}
	if len(res.Steps) != 3 {
		t.Fatalf("len(Steps) = %d", len(res.Steps))
	}

	// 顺序断言：Command 调用顺序 upload -> stop；Recipe 一次 file.backup
	if len(cmd.calls) != 2 {
		t.Fatalf("cmd calls = %d", len(cmd.calls))
	}
	if cmd.calls[0].ID != "ssh.upload" || cmd.calls[1].ID != "ssh.exec" {
		t.Errorf("cmd order: %+v", cmd.calls)
	}
	if len(rcp.calls) != 1 || rcp.calls[0].ID != "file.backup" {
		t.Fatalf("recipe calls: %+v", rcp.calls)
	}

	// 变量传递：${inputs.service} → myapp、${steps.upload.out.bytes_sent} → 2048、${steps.upload.out.path} → /tmp/a.tar
	stopParams := cmd.calls[1].Params
	if stopParams["cmd"] != "systemctl stop myapp" {
		t.Errorf("stop.cmd = %v", stopParams["cmd"])
	}
	if stopParams["context"] != "sent=2048" {
		t.Errorf("stop.context = %v", stopParams["context"])
	}
	if rcp.calls[0].Params["src"] != "/tmp/a.tar" {
		t.Errorf("backup.src = %v", rcp.calls[0].Params["src"])
	}

	// TargetID 透传
	if cmd.calls[0].Opts.TargetID != "host-01" {
		t.Errorf("TargetID = %q", cmd.calls[0].Opts.TargetID)
	}

	// AuditID 挂载
	if res.Steps[0].AuditID != "audit-ssh.upload" {
		t.Errorf("audit = %q", res.Steps[0].AuditID)
	}

	// OnStep 事件：3 步 × (start + finish) = 6 个
	if len(events) != 6 {
		t.Fatalf("events = %d", len(events))
	}
	wantPhases := []workflow.StepPhase{
		workflow.PhaseStart, workflow.PhaseFinish,
		workflow.PhaseStart, workflow.PhaseFinish,
		workflow.PhaseStart, workflow.PhaseFinish,
	}
	for i, ev := range events {
		if ev.Phase != wantPhases[i] {
			t.Errorf("event[%d].Phase = %s want %s", i, ev.Phase, wantPhases[i])
		}
	}
}

// -----------------------------------------------------------------------------
// 失败中止 + Error 阶段回调
// -----------------------------------------------------------------------------

func TestRunner_StopsOnFailure(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a.ok"] = map[string]any{}
	cmd.fail["b.bad"] = &codedErr{code: "BOOM", msg: "something exploded"}
	cmd.out["c.never"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a.ok"},
			{ID: "s2", Command: "b.bad"},
			{ID: "s3", Command: "c.never"},
		},
	}

	var errEvents []workflow.StepEvent
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		OnStep: func(ev workflow.StepEvent) {
			if ev.Phase == workflow.PhaseError {
				errEvents = append(errEvents, ev)
			}
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if res.OK {
		t.Error("res.OK want false")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("should stop after s2, got %d steps", len(res.Steps))
	}
	if res.Steps[1].ErrorCode != "BOOM" {
		t.Errorf("ErrorCode = %q, want BOOM", res.Steps[1].ErrorCode)
	}
	if len(cmd.calls) != 2 {
		t.Errorf("cmd calls should be 2, got %d", len(cmd.calls))
	}
	if len(errEvents) != 1 || errEvents[0].Step.ID != "s2" {
		t.Errorf("error events: %+v", errEvents)
	}
	if errEvents[0].Err == nil {
		t.Error("error event should carry Err")
	}
}

// -----------------------------------------------------------------------------
// 无 ErrorCode 的普通 error → STEP_FAILED
// -----------------------------------------------------------------------------

func TestRunner_PlainError(t *testing.T) {
	cmd := newFakeCmd()
	cmd.fail["x"] = errors.New("plain failure")

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID:    "wf",
		Steps: []workflow.Step{{ID: "s1", Command: "x"}},
	}
	res, _ := r.Run(context.Background(), w, workflow.RunOptions{})
	if res.Steps[0].ErrorCode != "STEP_FAILED" {
		t.Errorf("ErrorCode = %q", res.Steps[0].ErrorCode)
	}
}

// -----------------------------------------------------------------------------
// 插值失败在 Runner 层报告为 INTERPOLATE
// -----------------------------------------------------------------------------

func TestRunner_InterpolationError(t *testing.T) {
	cmd := newFakeCmd()
	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "x", Params: map[string]any{"cmd": "${inputs.no_such}"}},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Steps[0].ErrorCode != "INTERPOLATE" {
		t.Errorf("ErrorCode = %q", res.Steps[0].ErrorCode)
	}
	if len(cmd.calls) != 0 {
		t.Error("command must not be invoked when interpolation fails")
	}
}

// -----------------------------------------------------------------------------
// 缺少 Executor
// -----------------------------------------------------------------------------

func TestRunner_MissingExecutor(t *testing.T) {
	r := workflow.NewRunner(workflow.RunnerOptions{}) // 都不传
	w := &workflow.Workflow{
		ID:    "wf",
		Steps: []workflow.Step{{ID: "s1", Command: "x"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil || res.Steps[0].ErrorCode != "NO_EXECUTOR" {
		t.Fatalf("want NO_EXECUTOR, got %+v err=%v", res.Steps[0], err)
	}
}

// -----------------------------------------------------------------------------
// Validate 前置：不合法的 workflow 直接失败
// -----------------------------------------------------------------------------

func TestRunner_ValidateBlocks(t *testing.T) {
	r := workflow.NewRunner(workflow.RunnerOptions{Command: newFakeCmd()})
	_, err := r.Run(context.Background(), &workflow.Workflow{ID: ""}, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected validate error")
	}
}

// -----------------------------------------------------------------------------
// nil Params 也能正常执行（插值路径应容错）
// -----------------------------------------------------------------------------

func TestRunner_NilParams(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["x"] = map[string]any{"ok": true}
	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID:    "wf",
		Steps: []workflow.Step{{ID: "s1", Command: "x"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
	if !reflect.DeepEqual(cmd.calls[0].Params, map[string]any{}) {
		t.Errorf("params want empty map, got %v", cmd.calls[0].Params)
	}
}

// -----------------------------------------------------------------------------
// Timeout 透传
// -----------------------------------------------------------------------------

func TestRunner_TimeoutPropagates(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["x"] = map[string]any{}
	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "x", Timeout: 3 * time.Second},
		},
	}
	if _, err := r.Run(context.Background(), w, workflow.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if cmd.calls[0].Opts.Timeout != 3*time.Second {
		t.Errorf("Timeout = %v", cmd.calls[0].Opts.Timeout)
	}
}
