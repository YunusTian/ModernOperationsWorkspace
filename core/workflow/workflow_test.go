package workflow_test

import (
	"strings"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// 正例
// -----------------------------------------------------------------------------

func TestValidate_OK_CommandStep(t *testing.T) {
	w := &workflow.Workflow{
		ID: "deploy.dotnet",
		Inputs: []workflow.Input{
			{Name: "package", Type: workflow.InputTypeFile, Required: true},
			{Name: "service", Type: workflow.InputTypeString, Required: true},
		},
		Steps: []workflow.Step{
			{ID: "upload", Command: "ssh.upload", Params: map[string]any{"file": "${package}"}},
			{ID: "stop", Command: "ssh.exec", Timeout: 5 * time.Second},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_OK_RecipeStep(t *testing.T) {
	w := &workflow.Workflow{
		ID: "diag.system",
		Steps: []workflow.Step{
			{ID: "cpu", Recipe: "system.cpu"},
			{ID: "disk", Recipe: "system.disk"},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_OK_MixedStepsAndEmptyInputType(t *testing.T) {
	// Input.Type 留空应被视为 string，属于合法。
	w := &workflow.Workflow{
		ID: "mixed",
		Inputs: []workflow.Input{
			{Name: "host"},
		},
		Steps: []workflow.Step{
			{ID: "s1", Command: "ssh.exec"},
			{ID: "s2", Recipe: "system.cpu"},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 负例
// -----------------------------------------------------------------------------

func TestValidate_NilWorkflow(t *testing.T) {
	var w *workflow.Workflow
	if err := w.Validate(); err == nil {
		t.Fatal("expected error for nil workflow")
	}
}

func TestValidate_EmptyID(t *testing.T) {
	w := &workflow.Workflow{
		Steps: []workflow.Step{{ID: "s", Command: "ssh.exec"}},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "workflow id") {
		t.Fatalf("expected empty-id error, got %v", err)
	}
}

func TestValidate_NoSteps(t *testing.T) {
	w := &workflow.Workflow{ID: "x"}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "no steps") {
		t.Fatalf("expected no-steps error, got %v", err)
	}
}

func TestValidate_StepEmptyID(t *testing.T) {
	w := &workflow.Workflow{
		ID: "x",
		Steps: []workflow.Step{
			{Command: "ssh.exec"},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "id is empty") {
		t.Fatalf("expected empty step-id error, got %v", err)
	}
}

func TestValidate_StepDuplicateID(t *testing.T) {
	w := &workflow.Workflow{
		ID: "x",
		Steps: []workflow.Step{
			{ID: "dup", Command: "ssh.exec"},
			{ID: "dup", Command: "ssh.exec"},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("expected duplicate step-id error, got %v", err)
	}
}

func TestValidate_StepMissingCommandAndRecipe(t *testing.T) {
	w := &workflow.Workflow{
		ID: "x",
		Steps: []workflow.Step{
			{ID: "s1"},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "command or recipe required") {
		t.Fatalf("expected command-or-recipe-required error, got %v", err)
	}
}

func TestValidate_StepCommandAndRecipeConflict(t *testing.T) {
	w := &workflow.Workflow{
		ID: "x",
		Steps: []workflow.Step{
			{ID: "s1", Command: "ssh.exec", Recipe: "system.cpu"},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestValidate_StepNegativeTimeout(t *testing.T) {
	w := &workflow.Workflow{
		ID: "x",
		Steps: []workflow.Step{
			{ID: "s1", Command: "ssh.exec", Timeout: -1},
		},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestValidate_InputEmptyName(t *testing.T) {
	w := &workflow.Workflow{
		ID:     "x",
		Inputs: []workflow.Input{{}},
		Steps:  []workflow.Step{{ID: "s", Command: "ssh.exec"}},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "name is empty") {
		t.Fatalf("expected empty input-name error, got %v", err)
	}
}

func TestValidate_InputDuplicateName(t *testing.T) {
	w := &workflow.Workflow{
		ID: "x",
		Inputs: []workflow.Input{
			{Name: "svc"},
			{Name: "svc"},
		},
		Steps: []workflow.Step{{ID: "s", Command: "ssh.exec"}},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Fatalf("expected duplicate input-name error, got %v", err)
	}
}

func TestValidate_InputUnsupportedType(t *testing.T) {
	w := &workflow.Workflow{
		ID: "x",
		Inputs: []workflow.Input{
			{Name: "svc", Type: workflow.InputType("json")},
		},
		Steps: []workflow.Step{{ID: "s", Command: "ssh.exec"}},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("expected unsupported-type error, got %v", err)
	}
}
