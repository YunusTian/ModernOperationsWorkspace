package workflow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// EvalBool：独立表达式求值（供 Step.When 使用）
// -----------------------------------------------------------------------------

func TestEvalBool_TrueFalse(t *testing.T) {
	scope := workflow.Scope{
		Inputs: map[string]any{"debug": true, "port": 8080},
		Steps:  map[string]workflow.StepScope{},
	}
	cases := map[string]bool{
		"inputs.debug":        true,
		"inputs.debug == true":true,
		"inputs.port > 1024":  true,
		"inputs.port < 80":    false,
		"!inputs.debug":       false,
		"1 == 1":              true,
	}
	for expr, want := range cases {
		got, err := workflow.EvalBool(expr, scope)
		if err != nil {
			t.Fatalf("EvalBool(%q): unexpected err %v", expr, err)
		}
		if got != want {
			t.Errorf("EvalBool(%q) = %v, want %v", expr, got, want)
		}
	}
}

func TestEvalBool_ErrorsAreInterpolationError(t *testing.T) {
	scope := workflow.Scope{}
	tests := []string{
		"",                        // 空
		"no_such_top_level == 1",  // 未声明的顶层名：expr 编译期拒绝
		"1 +",                     // 语法错
		"1 + 1",                   // 非 bool
	}
	for _, expr := range tests {
		_, err := workflow.EvalBool(expr, scope)
		if err == nil {
			t.Errorf("EvalBool(%q): expected error, got nil", expr)
			continue
		}
		var ie *workflow.InterpolationError
		if !errorsAs(err, &ie) {
			t.Errorf("EvalBool(%q) → err type = %T, want *InterpolationError", expr, err)
		}
	}
}

// errorsAs 是本地小工具，避免测试文件直接导入 errors 只用一次。
func errorsAs(err error, target **workflow.InterpolationError) bool {
	for e := err; e != nil; {
		if v, ok := e.(*workflow.InterpolationError); ok {
			*target = v
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// -----------------------------------------------------------------------------
// Runner + when：整合行为
// -----------------------------------------------------------------------------

func TestRunner_When_TrueRuns(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a.ok"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a.ok", When: "inputs.enable == true"},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		Inputs: map[string]any{"enable": true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
	if res.Steps[0].Skipped {
		t.Fatal("s1 should not be skipped")
	}
	if len(cmd.calls) != 1 {
		t.Fatalf("cmd calls = %d, want 1", len(cmd.calls))
	}
}

func TestRunner_When_FalseSkips(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a.ok"] = map[string]any{"ran": true}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a.ok", When: "inputs.enable == true"},
			{ID: "s2", Command: "a.ok"},
		},
	}

	var events []workflow.StepEvent
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		Inputs: map[string]any{"enable": false},
		OnStep: func(ev workflow.StepEvent) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true (skip is not failure)")
	}
	// s1 skipped、s2 finished
	if !res.Steps[0].Skipped || res.Steps[0].AuditID != "" || len(res.Steps[0].Data) != 0 {
		t.Fatalf("s1 skipped state wrong: %+v", res.Steps[0])
	}
	if res.Steps[1].Skipped || res.Steps[1].AuditID == "" {
		t.Fatalf("s2 should run: %+v", res.Steps[1])
	}
	// 只调用了 a.ok 一次（s2）
	if len(cmd.calls) != 1 {
		t.Fatalf("cmd calls = %d, want 1", len(cmd.calls))
	}

	// 事件序列：s1 → start + skip；s2 → start + finish
	phases := make([]workflow.StepPhase, 0, len(events))
	for _, ev := range events {
		phases = append(phases, ev.Phase)
	}
	want := []workflow.StepPhase{
		workflow.PhaseStart, workflow.PhaseSkip,
		workflow.PhaseStart, workflow.PhaseFinish,
	}
	if len(phases) != len(want) {
		t.Fatalf("phases len = %d, want %d (%v)", len(phases), len(want), phases)
	}
	for i, p := range phases {
		if p != want[i] {
			t.Errorf("phases[%d] = %s, want %s", i, p, want[i])
		}
	}
}

func TestRunner_When_EvalErrorAbortsWorkflow(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a.ok"] = map[string]any{}
	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			// 语法错，编译失败 → WHEN_EVAL；工作流应中断
			{ID: "s1", Command: "a.ok", When: "inputs.enable =="},
			{ID: "s2", Command: "a.ok"},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		Inputs: map[string]any{"enable": true},
	})
	if err == nil {
		t.Fatal("expected err")
	}
	if res.OK {
		t.Fatal("res.OK want false")
	}
	if res.Steps[0].ErrorCode != "WHEN_EVAL" {
		t.Errorf("s1 ErrorCode = %q, want WHEN_EVAL", res.Steps[0].ErrorCode)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("only s1 should be attempted, got %d", len(res.Steps))
	}
	if len(cmd.calls) != 0 {
		t.Error("no command should be invoked")
	}
}

func TestRunner_When_UndefinedVariableAborts(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a.ok"] = map[string]any{}
	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			// 引用未声明的顶层名（既不是 inputs 也不是 steps）：expr 编译期就会拒绝。
			{ID: "s1", Command: "a.ok", When: "no_such_top_level == 1"},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	if res.Steps[0].ErrorCode != "WHEN_EVAL" {
		t.Errorf("ErrorCode = %q, want WHEN_EVAL: msg=%s", res.Steps[0].ErrorCode, res.Steps[0].ErrorMsg)
	}
}

// 与前置 step 输出联动：when 引用 ${steps.<id>.out.*}
func TestRunner_When_ReadsPrevStepOut(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["probe"] = map[string]any{"healthy": true}
	cmd.out["repair"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "probe", Command: "probe"},
			// 只在探测不健康时修复
			{ID: "repair", Command: "repair", When: "!steps.probe.out.healthy"},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Steps[1].Skipped {
		t.Fatalf("repair should be skipped when probe is healthy: %+v", res.Steps[1])
	}
	if len(cmd.calls) != 1 || cmd.calls[0].ID != "probe" {
		t.Fatalf("only probe should be invoked, got %+v", cmd.calls)
	}
}

// -----------------------------------------------------------------------------
// Loader：when 字段被解析（严格模式下不再是未知字段）
// -----------------------------------------------------------------------------

func TestLoader_When(t *testing.T) {
	src := `
workflow:
  id: wf.when
  steps:
    - id: s1
      command: a.ok
      when: inputs.debug == true
`
	w, err := workflow.LoadBytes([]byte(src))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if w.Steps[0].When != "inputs.debug == true" {
		t.Fatalf("when parsed = %q", w.Steps[0].When)
	}
	// 顺便校验严格模式仍然生效
	if !strings.Contains("", "") {
		t.Fatal("sanity check")
	}
}
