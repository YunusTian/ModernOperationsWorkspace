package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// Validate：ID 引用与 compensate 互斥
// -----------------------------------------------------------------------------

func TestValidate_OnFailure_UnknownIDRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a"},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"no_such"}},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown step id") {
		t.Fatalf("want unknown-id error, got %v", err)
	}
}

func TestValidate_OnFailure_DuplicateRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a"},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"s1", "s1"}},
	}
	if err := w.Validate(); err == nil {
		t.Fatal("want duplicate error")
	}
}

func TestValidate_Compensate_BothCommandAndRecipe(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a",
				Compensate: &workflow.CompensateAction{Command: "x", Recipe: "y"}},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutually-exclusive, got %v", err)
	}
}

func TestValidate_Compensate_EmptyRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a", Compensate: &workflow.CompensateAction{}},
		},
	}
	if err := w.Validate(); err == nil {
		t.Fatal("want empty-compensate error")
	}
}

// -----------------------------------------------------------------------------
// 主流程成功 → 不触发 rollback
// -----------------------------------------------------------------------------

func TestRunner_Rollback_SuccessNoRollback(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a"] = map[string]any{}
	cmd.out["comp"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a",
				Compensate: &workflow.CompensateAction{Command: "comp"}},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"s1"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
	if len(res.Rollback) != 0 {
		t.Fatalf("rollback should not run on success, got %+v", res.Rollback)
	}
	// comp 不应被调用
	for _, c := range cmd.calls {
		if c.ID == "comp" {
			t.Fatalf("comp should not be invoked: %+v", cmd.calls)
		}
	}
}

// -----------------------------------------------------------------------------
// 主流程失败 → 逆序回滚 "已成功" 的 step
// -----------------------------------------------------------------------------

func TestRunner_Rollback_ReverseOrderOnlySuccessSteps(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["upload"] = map[string]any{}
	cmd.out["deploy"] = map[string]any{}
	cmd.fail["healthcheck"] = errors.New("500")
	cmd.out["rm_upload"] = map[string]any{}
	cmd.out["rollback_deploy"] = map[string]any{}
	cmd.out["skip_never_hit"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "upload", Command: "upload",
				Compensate: &workflow.CompensateAction{Command: "rm_upload"}},
			{ID: "deploy", Command: "deploy",
				Compensate: &workflow.CompensateAction{Command: "rollback_deploy"}},
			{ID: "healthcheck", Command: "healthcheck",
				// 这一步失败，其 compensate 不应被触发（未成功过）
				Compensate: &workflow.CompensateAction{Command: "skip_never_hit"}},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"upload", "deploy", "healthcheck"}},
	}

	var rollbackEvs []workflow.StepEvent
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		OnStep: func(ev workflow.StepEvent) {
			if ev.Phase == workflow.PhaseRollback {
				rollbackEvs = append(rollbackEvs, ev)
			}
		},
	})
	if err == nil {
		t.Fatal("expected err")
	}
	if res.OK {
		t.Fatal("res.OK want false")
	}
	if len(res.Rollback) != 2 {
		t.Fatalf("expected 2 rollback entries (deploy, upload), got %+v", res.Rollback)
	}
	// 逆序：先 deploy，再 upload
	if res.Rollback[0].StepID != "deploy" || !res.Rollback[0].OK {
		t.Fatalf("rollback[0]=%+v, want deploy ok", res.Rollback[0])
	}
	if res.Rollback[1].StepID != "upload" || !res.Rollback[1].OK {
		t.Fatalf("rollback[1]=%+v, want upload ok", res.Rollback[1])
	}
	// Command 调用顺序：upload, deploy, healthcheck, rollback_deploy, rm_upload
	wantOrder := []string{"upload", "deploy", "healthcheck", "rollback_deploy", "rm_upload"}
	if len(cmd.calls) != len(wantOrder) {
		t.Fatalf("call count=%d, want %d (%v)", len(cmd.calls), len(wantOrder), cmdIDs(cmd))
	}
	for i, name := range wantOrder {
		if cmd.calls[i].ID != name {
			t.Errorf("call[%d]=%q, want %q", i, cmd.calls[i].ID, name)
		}
	}
	// PhaseRollback 事件顺序与 res.Rollback 对齐
	if len(rollbackEvs) != 2 || rollbackEvs[0].Step.ID != "deploy" || rollbackEvs[1].Step.ID != "upload" {
		t.Errorf("events: %+v", rollbackEvs)
	}
}

func cmdIDs(c *fakeCommandExecutor) []string {
	out := make([]string, 0, len(c.calls))
	for _, x := range c.calls {
		out = append(out, x.ID)
	}
	return out
}

// -----------------------------------------------------------------------------
// 没写 compensate 的 step 出现在 rollback 列表 → 静默跳过（Skipped 行）
// -----------------------------------------------------------------------------

func TestRunner_Rollback_MissingCompensateSkipped(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a"] = map[string]any{}
	cmd.fail["b"] = errors.New("boom")

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a"}, // 没写 compensate
			{ID: "b", Command: "b"},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"a"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	if len(res.Rollback) != 1 {
		t.Fatalf("rollback len=%d, want 1", len(res.Rollback))
	}
	if !res.Rollback[0].Skipped || !res.Rollback[0].OK {
		t.Fatalf("expected skipped rollback entry, got %+v", res.Rollback[0])
	}
}

// -----------------------------------------------------------------------------
// rollback 内部失败：不嵌套 rollback、不 retry；后续 compensate 仍执行
// -----------------------------------------------------------------------------

func TestRunner_Rollback_InternalFailureDoesNotAbort(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a"] = map[string]any{}
	cmd.out["b"] = map[string]any{}
	cmd.fail["c"] = errors.New("boom")
	// 补偿：compA 会成功，compB 会失败
	cmd.out["compA"] = map[string]any{}
	cmd.fail["compB"] = errors.New("comp fail")

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a",
				Compensate: &workflow.CompensateAction{Command: "compA"}},
			{ID: "b", Command: "b",
				Compensate: &workflow.CompensateAction{Command: "compB"}},
			{ID: "c", Command: "c"},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"a", "b"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	if len(res.Rollback) != 2 {
		t.Fatalf("rollback len=%d", len(res.Rollback))
	}
	// 逆序：先 b (fail)、再 a (ok)
	if res.Rollback[0].StepID != "b" || res.Rollback[0].OK {
		t.Fatalf("rollback[0]=%+v, want b failed", res.Rollback[0])
	}
	if res.Rollback[1].StepID != "a" || !res.Rollback[1].OK {
		t.Fatalf("rollback[1]=%+v, want a ok", res.Rollback[1])
	}
}

// -----------------------------------------------------------------------------
// Skipped step 不回滚
// -----------------------------------------------------------------------------

func TestRunner_Rollback_SkippedStepNotRolledBack(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["a"] = map[string]any{}
	cmd.fail["c"] = errors.New("boom")
	cmd.out["comp_b"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "a", Command: "a"},
			{ID: "b", Command: "never", When: "false",
				Compensate: &workflow.CompensateAction{Command: "comp_b"}},
			{ID: "c", Command: "c"},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"a", "b"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	// b 被 skip → 不回滚；a 无 compensate → skipped 行；总长度 1
	if len(res.Rollback) != 1 {
		t.Fatalf("rollback len=%d (%+v)", len(res.Rollback), res.Rollback)
	}
	if res.Rollback[0].StepID != "a" || !res.Rollback[0].Skipped {
		t.Fatalf("unexpected: %+v", res.Rollback[0])
	}
	// comp_b 必须没被调用
	for _, c := range cmd.calls {
		if c.ID == "comp_b" {
			t.Fatalf("comp_b should not run: %+v", cmd.calls)
		}
	}
}

// -----------------------------------------------------------------------------
// compensate 参数插值 & scope 中可读 steps.<id>.out.*
// -----------------------------------------------------------------------------

func TestRunner_Rollback_CompensateInterpolatesScope(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["upload"] = map[string]any{"path": "/tmp/x"}
	cmd.fail["deploy"] = errors.New("boom")
	cmd.out["rm"] = map[string]any{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd})
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "upload", Command: "upload",
				Compensate: &workflow.CompensateAction{
					Command: "rm",
					Params:  map[string]any{"file": "${steps.upload.out.path}"},
				}},
			{ID: "deploy", Command: "deploy"},
		},
		OnFailure: &workflow.FailurePolicy{Rollback: []string{"upload"}},
	}
	_, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	// 查找 rm 的调用
	var rmCall *fakeCall
	for i := range cmd.calls {
		if cmd.calls[i].ID == "rm" {
			rmCall = &cmd.calls[i]
			break
		}
	}
	if rmCall == nil {
		t.Fatal("rm not invoked")
	}
	if rmCall.Params["file"] != "/tmp/x" {
		t.Fatalf("interpolation failed: %v", rmCall.Params)
	}
}

// -----------------------------------------------------------------------------
// Loader：yaml on_failure + compensate 完整往返
// -----------------------------------------------------------------------------

func TestLoader_OnFailureRollback(t *testing.T) {
	src := `
workflow:
  id: wf.deploy
  steps:
    - id: upload
      command: ssh.upload
      compensate:
        command: ssh.exec
        params: { cmd: "rm -f /tmp/pkg" }
        timeout: 5s
    - id: deploy
      command: ssh.exec
  on_failure:
    rollback: [upload]
`
	w, err := workflow.LoadBytes([]byte(src))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if w.OnFailure == nil || len(w.OnFailure.Rollback) != 1 || w.OnFailure.Rollback[0] != "upload" {
		t.Fatalf("on_failure not parsed: %+v", w.OnFailure)
	}
	comp := w.Steps[0].Compensate
	if comp == nil || comp.Command != "ssh.exec" || comp.Timeout == 0 {
		t.Fatalf("compensate not parsed: %+v", comp)
	}
}

func TestLoader_OnFailureRollback_UnknownIDRejected(t *testing.T) {
	src := `
workflow:
  id: wf
  steps:
    - id: s1
      command: a
  on_failure:
    rollback: [nope]
`
	_, err := workflow.LoadBytes([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "unknown step id") {
		t.Fatalf("want unknown-id error, got %v", err)
	}
}
