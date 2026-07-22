// v060_p0_test.go 覆盖 v0.6.0 P0 新增的 Loader / Validate 路径。
//
// 涵盖：
//   - parallel_limit：正/负/超限/无 parallel 组合
//   - parallel_group + branches：唯一 name / 空 branches / 空 steps / 嵌套禁止
//   - step.workflow：子调用主路径 / 循环 / 深度 / 绝对路径 / ref://
//   - inputs.schema：编译 OK / 与 type 冲突 / 无效 pattern
//   - workflow.idempotency_key 与 manifest_version 字段透传
//
// 目的：不触及 Runner，纯粹校验静态解析层；确保 v0.3 旧测试全部继续 PASS
// 是通过复用 workflow_test / loader_test 等已有断言实现的。

package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// parallel_limit
// -----------------------------------------------------------------------------

func TestV060_ParallelLimit_OKOnParallelStep(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a", Parallel: true, ParallelLimit: 4},
			{ID: "s2", Command: "b", Parallel: true},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestV060_ParallelLimit_ZeroEqualsUnlimited(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a", Parallel: true, ParallelLimit: 0},
			{ID: "s2", Command: "b", Parallel: true},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestV060_ParallelLimit_RejectsWithoutParallel(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a", ParallelLimit: 4},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "PARALLEL_LIMIT_WITHOUT_PARALLEL") {
		t.Fatalf("expected PARALLEL_LIMIT_WITHOUT_PARALLEL, got %v", err)
	}
}

func TestV060_ParallelLimit_RejectsNegative(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a", Parallel: true, ParallelLimit: -1},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "PARALLEL_LIMIT_INVALID") {
		t.Fatalf("expected PARALLEL_LIMIT_INVALID, got %v", err)
	}
}

func TestV060_ParallelLimit_RejectsAboveCap(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "s1", Command: "a", Parallel: true, ParallelLimit: 513},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "PARALLEL_LIMIT_EXCEEDS_CAP") {
		t.Fatalf("expected PARALLEL_LIMIT_EXCEEDS_CAP, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// parallel_group
// -----------------------------------------------------------------------------

func newBranchStep(id string, cmd string) workflow.Step {
	return workflow.Step{ID: id, Command: cmd}
}

func TestV060_ParallelGroup_HappyPath(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "rolling",
				ParallelGroup: &workflow.ParallelGroup{
					ParallelLimit: 3,
					Branches: []workflow.Branch{
						{Name: "a", Steps: []workflow.Step{newBranchStep("a1", "ssh.exec")}},
						{Name: "b", Steps: []workflow.Step{newBranchStep("b1", "ssh.exec")}},
					},
				},
			},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestV060_ParallelGroup_EmptyBranchesRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{ID: "rolling", ParallelGroup: &workflow.ParallelGroup{Branches: nil}},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "branches is empty") {
		t.Fatalf("expected empty-branches error, got %v", err)
	}
}

func TestV060_ParallelGroup_DuplicateBranchNameRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "rolling",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{
						{Name: "a", Steps: []workflow.Step{newBranchStep("a1", "x")}},
						{Name: "a", Steps: []workflow.Step{newBranchStep("a2", "y")}},
					},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "BRANCH_NAME_DUPLICATE") {
		t.Fatalf("expected BRANCH_NAME_DUPLICATE, got %v", err)
	}
}

func TestV060_ParallelGroup_EmptyBranchNameRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "rolling",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{
						{Name: "", Steps: []workflow.Step{newBranchStep("a1", "x")}},
					},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "branches[0].name is empty") {
		t.Fatalf("expected empty branch name error, got %v", err)
	}
}

func TestV060_ParallelGroup_EmptyBranchStepsRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "rolling",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{{Name: "a"}},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "has no steps") {
		t.Fatalf("expected empty steps error, got %v", err)
	}
}

func TestV060_ParallelGroup_ForbidsInnerParallelGroup(t *testing.T) {
	// branch 内的 step 不允许再套一个 parallel_group（v0.6.0 单层限制）
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "outer",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{
						{
							Name: "a",
							Steps: []workflow.Step{
								{
									ID: "inner",
									ParallelGroup: &workflow.ParallelGroup{
										Branches: []workflow.Branch{
											{Name: "x", Steps: []workflow.Step{newBranchStep("x1", "c")}},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "NESTED_PARALLEL_GROUP_FORBIDDEN") {
		t.Fatalf("expected NESTED_PARALLEL_GROUP_FORBIDDEN, got %v", err)
	}
}

func TestV060_ParallelGroup_ForbidsInnerParallelBool(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "outer",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{
						{
							Name: "a",
							Steps: []workflow.Step{
								{ID: "a1", Command: "c", Parallel: true},
							},
						},
					},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "NESTED_PARALLEL_FORBIDDEN") {
		t.Fatalf("expected NESTED_PARALLEL_FORBIDDEN, got %v", err)
	}
}

func TestV060_ParallelGroup_BranchOutRefRejected(t *testing.T) {
	// branch a 的 step 引用 branch b 的 step.out → BRANCH_OUT_REF
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "rolling",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{
						{
							Name: "a",
							Steps: []workflow.Step{
								{ID: "a1", Command: "ssh.exec", Params: map[string]any{
									"cmd": "echo ${steps.b1.out.data}", // 引用了 branch b 的 b1
								}},
							},
						},
						{
							Name: "b",
							Steps: []workflow.Step{
								{ID: "b1", Command: "ssh.exec"},
							},
						},
					},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "BRANCH_OUT_REF") {
		t.Fatalf("expected BRANCH_OUT_REF, got %v", err)
	}
}

func TestV060_ParallelGroup_SameBranchOutRefOK(t *testing.T) {
	// 同 branch 内的 step 引用兄弟 out 完全合法（branch 内串行）
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "rolling",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{
						{
							Name: "a",
							Steps: []workflow.Step{
								{ID: "a1", Command: "ssh.exec"},
								{ID: "a2", Command: "ssh.exec", Params: map[string]any{
									"cmd": "echo ${steps.a1.out.data}", // 同 branch
								}},
							},
						},
					},
				},
			},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestV060_ParallelGroup_ExclusivityWithCommand(t *testing.T) {
	// 同一 step 同时声明 Command 和 ParallelGroup → 互斥
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID:      "s1",
				Command: "ssh.exec",
				ParallelGroup: &workflow.ParallelGroup{
					Branches: []workflow.Branch{{Name: "a", Steps: []workflow.Step{newBranchStep("a1", "x")}}},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestV060_ParallelGroup_ParallelLimitAboveCapRejected(t *testing.T) {
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID: "rolling",
				ParallelGroup: &workflow.ParallelGroup{
					ParallelLimit: 1024,
					Branches:      []workflow.Branch{{Name: "a", Steps: []workflow.Step{newBranchStep("a1", "x")}}},
				},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "PARALLEL_LIMIT_EXCEEDS_CAP") {
		t.Fatalf("expected PARALLEL_LIMIT_EXCEEDS_CAP, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// step.workflow 子调用
// -----------------------------------------------------------------------------

func TestV060_SubWorkflow_LoaderHappyPath(t *testing.T) {
	tmp := t.TempDir()
	child := filepath.Join(tmp, "child.yaml")
	os.WriteFile(child, []byte(`
workflow:
  id: child
  steps:
    - id: c1
      command: ssh.exec
`), 0o644)
	parent := filepath.Join(tmp, "parent.yaml")
	os.WriteFile(parent, []byte(`
workflow:
  id: parent
  steps:
    - id: call_child
      workflow: ./child.yaml
      inputs:
        a: 1
`), 0o644)
	w, err := workflow.LoadFile(parent)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(w.Steps) != 1 || w.Steps[0].Workflow == nil {
		t.Fatalf("expected 1 step with sub-workflow, got %+v", w.Steps)
	}
	if w.Steps[0].Workflow.Loaded == nil {
		t.Fatal("expected sub-workflow to be pre-loaded")
	}
	if w.Steps[0].Workflow.Loaded.ID != "child" {
		t.Errorf("child.ID = %q, want child", w.Steps[0].Workflow.Loaded.ID)
	}
	if w.Steps[0].SubWorkflowInputs["a"] != 1 {
		t.Errorf("expected inputs.a=1, got %v", w.Steps[0].SubWorkflowInputs["a"])
	}
}

func TestV060_SubWorkflow_CycleRejected(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.yaml")
	b := filepath.Join(tmp, "b.yaml")
	os.WriteFile(a, []byte(`
workflow:
  id: a
  steps:
    - id: to_b
      workflow: ./b.yaml
`), 0o644)
	os.WriteFile(b, []byte(`
workflow:
  id: b
  steps:
    - id: to_a
      workflow: ./a.yaml
`), 0o644)
	_, err := workflow.LoadFile(a)
	if err == nil || !strings.Contains(err.Error(), "SUBWORKFLOW_CYCLE") {
		t.Fatalf("expected SUBWORKFLOW_CYCLE, got %v", err)
	}
}

func TestV060_SubWorkflow_DepthExceeded(t *testing.T) {
	tmp := t.TempDir()
	// 生成 6 层链：0 → 1 → ... → 5，超过 MaxSubWorkflowDepth=5
	for i := 0; i <= 6; i++ {
		var body string
		if i < 6 {
			body = "workflow:\n  id: w" + itoa(i) + "\n  steps:\n    - id: next\n      workflow: ./w" + itoa(i+1) + ".yaml\n"
		} else {
			body = "workflow:\n  id: w" + itoa(i) + "\n  steps:\n    - id: leaf\n      command: ssh.exec\n"
		}
		os.WriteFile(filepath.Join(tmp, "w"+itoa(i)+".yaml"), []byte(body), 0o644)
	}
	_, err := workflow.LoadFile(filepath.Join(tmp, "w0.yaml"))
	if err == nil || !strings.Contains(err.Error(), "SUBWORKFLOW_DEPTH_EXCEEDED") {
		t.Fatalf("expected SUBWORKFLOW_DEPTH_EXCEEDED, got %v", err)
	}
}

func TestV060_SubWorkflow_MaxDepthAtBoundaryOK(t *testing.T) {
	tmp := t.TempDir()
	// 生成 5 层链（0→1→2→3→4→5，5 是叶子），深度 5 == MaxSubWorkflowDepth
	for i := 0; i <= 5; i++ {
		var body string
		if i < 5 {
			body = "workflow:\n  id: w" + itoa(i) + "\n  steps:\n    - id: next\n      workflow: ./w" + itoa(i+1) + ".yaml\n"
		} else {
			body = "workflow:\n  id: w" + itoa(i) + "\n  steps:\n    - id: leaf\n      command: ssh.exec\n"
		}
		os.WriteFile(filepath.Join(tmp, "w"+itoa(i)+".yaml"), []byte(body), 0o644)
	}
	if _, err := workflow.LoadFile(filepath.Join(tmp, "w0.yaml")); err != nil {
		t.Fatalf("expected boundary depth OK, got %v", err)
	}
}

func TestV060_SubWorkflow_AbsPathForbidden(t *testing.T) {
	tmp := t.TempDir()
	child := filepath.Join(tmp, "child.yaml")
	os.WriteFile(child, []byte(`
workflow:
  id: child
  steps:
    - id: c1
      command: ssh.exec
`), 0o644)
	parent := filepath.Join(tmp, "parent.yaml")
	// 用绝对路径引用 child
	yamlContent := "workflow:\n  id: parent\n  steps:\n    - id: call\n      workflow: " + child + "\n"
	os.WriteFile(parent, []byte(yamlContent), 0o644)
	_, err := workflow.LoadFile(parent)
	if err == nil || !strings.Contains(err.Error(), "WORKFLOW_ABS_PATH_FORBIDDEN") {
		t.Fatalf("expected WORKFLOW_ABS_PATH_FORBIDDEN, got %v", err)
	}
}

func TestV060_SubWorkflow_RefReserved(t *testing.T) {
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "parent.yaml")
	os.WriteFile(parent, []byte(`
workflow:
  id: parent
  steps:
    - id: call
      workflow: ref://some.published.id
`), 0o644)
	_, err := workflow.LoadFile(parent)
	if err == nil || !strings.Contains(err.Error(), "WORKFLOW_REF_UNSUPPORTED") {
		t.Fatalf("expected WORKFLOW_REF_UNSUPPORTED, got %v", err)
	}
}

func TestV060_SubWorkflow_ExclusivityWithCommand(t *testing.T) {
	// step 同时声明 Command 与 Workflow → 互斥
	w := &workflow.Workflow{
		ID: "wf",
		Steps: []workflow.Step{
			{
				ID:       "s1",
				Command:  "ssh.exec",
				Workflow: &workflow.SubWorkflow{Path: "./x.yaml", Loaded: &workflow.Workflow{ID: "x", Steps: []workflow.Step{{ID: "leaf", Command: "c"}}}},
			},
		},
	}
	if err := w.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// inputs.schema
// -----------------------------------------------------------------------------

func TestV060_InputsSchema_LoaderCompilesOK(t *testing.T) {
	yamlBody := `
workflow:
  id: wf
  inputs:
    - name: port
      required: true
      schema:
        type: integer
        minimum: 1
        maximum: 65535
        default: 8080
    - name: env
      schema: { type: string, enum: [dev, prod] }
  steps:
    - id: s1
      command: ssh.exec
`
	w, err := workflow.LoadBytes([]byte(yamlBody))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if len(w.Inputs) != 2 || w.Inputs[0].Schema == nil || w.Inputs[1].Schema == nil {
		t.Fatalf("expected 2 inputs with compiled schema, got %+v", w.Inputs)
	}
}

func TestV060_InputsSchema_ConflictsWithType(t *testing.T) {
	yamlBody := `
workflow:
  id: wf
  inputs:
    - name: port
      type: int
      schema:
        type: integer
  steps:
    - id: s1
      command: ssh.exec
`
	_, err := workflow.LoadBytes([]byte(yamlBody))
	if err == nil || !strings.Contains(err.Error(), "INPUT_TYPE_SCHEMA_CONFLICT") {
		t.Fatalf("expected INPUT_TYPE_SCHEMA_CONFLICT, got %v", err)
	}
}

func TestV060_InputsSchema_InvalidPatternRejectedAtCompile(t *testing.T) {
	yamlBody := `
workflow:
  id: wf
  inputs:
    - name: version
      schema:
        type: string
        pattern: "["   # 语法错误
  steps:
    - id: s1
      command: ssh.exec
`
	_, err := workflow.LoadBytes([]byte(yamlBody))
	if err == nil || !strings.Contains(err.Error(), "bad pattern") {
		t.Fatalf("expected bad-pattern error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// workflow.idempotency_key & manifest_version
// -----------------------------------------------------------------------------

func TestV060_IdempotencyKey_LoaderPassesThrough(t *testing.T) {
	yamlBody := `
workflow:
  id: wf
  manifest_version: 2
  idempotency_key: "${inputs.host}-nightly"
  steps:
    - id: s1
      command: ssh.exec
`
	w, err := workflow.LoadBytes([]byte(yamlBody))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if w.ManifestVersion != 2 {
		t.Errorf("ManifestVersion = %d, want 2", w.ManifestVersion)
	}
	if w.IdempotencyKey != "${inputs.host}-nightly" {
		t.Errorf("IdempotencyKey = %q, want ${inputs.host}-nightly", w.IdempotencyKey)
	}
}

// -----------------------------------------------------------------------------
// step.target
// -----------------------------------------------------------------------------

func TestV060_StepTarget_LoaderPassesThrough(t *testing.T) {
	yamlBody := `
workflow:
  id: wf
  steps:
    - id: dump
      command: ssh.exec
    - id: scp
      command: ssh.exec
      target: db1
`
	w, err := workflow.LoadBytes([]byte(yamlBody))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if w.Steps[0].Target != "" || w.Steps[1].Target != "db1" {
		t.Fatalf("target mismatch: %+v", []string{w.Steps[0].Target, w.Steps[1].Target})
	}
}

// -----------------------------------------------------------------------------
// v0.3 兼容性回归：旧字段不受影响
// -----------------------------------------------------------------------------

func TestV060_V03Compat_MinimalStillWorks(t *testing.T) {
	yamlBody := `
workflow:
  id: legacy
  inputs:
    - name: host
      type: string
      required: true
  steps:
    - id: s1
      command: ssh.exec
      when: 'inputs.host != ""'
      retry: { max: 3, backoff: 500ms }
      compensate:
        command: ssh.exec
        params: { cmd: "cleanup" }
    - id: s2
      recipe: system.cpu
      parallel: true
    - id: s3
      recipe: system.disk
      parallel: true
  on_failure:
    rollback: [s1]
`
	w, err := workflow.LoadBytes([]byte(yamlBody))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if w.ID != "legacy" || len(w.Steps) != 3 {
		t.Fatalf("unexpected: %+v", w)
	}
	if w.Inputs[0].Type != workflow.InputTypeString {
		t.Errorf("Type = %q", w.Inputs[0].Type)
	}
	if w.Inputs[0].Schema != nil {
		t.Errorf("Schema should be nil for v0.3 shorthand")
	}
	if w.Steps[0].Retry == nil || w.Steps[0].Compensate == nil {
		t.Fatal("retry/compensate should be preserved")
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// itoa 是 strconv.Itoa 的极简包装，仅为避免额外 import。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
