package workflow_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow"
)

// -----------------------------------------------------------------------------
// LoadFile：完整文件解析（正例）
// -----------------------------------------------------------------------------

func TestLoadFile_Simple(t *testing.T) {
	w, err := workflow.LoadFile(filepath.Join("testdata", "simple.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if w.ID != "deploy.dotnet" {
		t.Errorf("ID = %q, want deploy.dotnet", w.ID)
	}
	if len(w.Inputs) != 2 {
		t.Fatalf("Inputs len = %d, want 2", len(w.Inputs))
	}
	if w.Inputs[0].Name != "package" || w.Inputs[0].Type != workflow.InputTypeFile || !w.Inputs[0].Required {
		t.Errorf("Inputs[0] = %+v", w.Inputs[0])
	}
	if w.Inputs[1].Default != "myapp" {
		t.Errorf("Inputs[1].Default = %v, want myapp", w.Inputs[1].Default)
	}
	if len(w.Steps) != 5 {
		t.Fatalf("Steps len = %d, want 5", len(w.Steps))
	}
	if w.Steps[0].Command != "ssh.upload" || w.Steps[0].Recipe != "" {
		t.Errorf("Steps[0] = %+v", w.Steps[0])
	}
	if w.Steps[1].Timeout != 5*time.Second {
		t.Errorf("Steps[1].Timeout = %v, want 5s", w.Steps[1].Timeout)
	}
	if w.Steps[2].Recipe != "file.backup" || w.Steps[2].Command != "" {
		t.Errorf("Steps[2] = %+v", w.Steps[2])
	}
	if got := w.Steps[0].Params["dest"]; got != "/opt/app/" {
		t.Errorf("Steps[0].Params[dest] = %v", got)
	}
}

// -----------------------------------------------------------------------------
// LoadBytes / LoadReader：接口一致性
// -----------------------------------------------------------------------------

const minimalYAML = `
workflow:
  id: minimal
  steps:
    - id: s1
      command: ssh.exec
`

func TestLoadBytes_Minimal(t *testing.T) {
	w, err := workflow.LoadBytes([]byte(minimalYAML))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if w.ID != "minimal" || len(w.Steps) != 1 {
		t.Fatalf("unexpected: %+v", w)
	}
}

func TestLoadReader_Minimal(t *testing.T) {
	w, err := workflow.LoadReader(bytes.NewBufferString(minimalYAML))
	if err != nil {
		t.Fatalf("LoadReader: %v", err)
	}
	if w.ID != "minimal" {
		t.Errorf("ID = %q", w.ID)
	}
}

func TestLoadReader_NilReader(t *testing.T) {
	if _, err := workflow.LoadReader(nil); err == nil {
		t.Fatal("expected error for nil reader")
	}
}

// -----------------------------------------------------------------------------
// 严格模式：未知字段必须报错
// -----------------------------------------------------------------------------

func TestLoadBytes_StrictUnknownFieldTopLevel(t *testing.T) {
	data := `
workflow:
  id: x
  steps:
    - id: s1
      command: ssh.exec
  onFailure: []   # PR2 尚未支持，严格模式应报错
`
	_, err := workflow.LoadBytes([]byte(data))
	if err == nil {
		t.Fatal("expected strict error for unknown field 'onFailure'")
	}
	if !strings.Contains(err.Error(), "onFailure") {
		t.Errorf("error should mention onFailure, got: %v", err)
	}
}

func TestLoadBytes_StrictUnknownFieldStep(t *testing.T) {
	data := `
workflow:
  id: x
  steps:
    - id: s1
      command: ssh.exec
      retry: { max: 3 }   # 未支持
`
	_, err := workflow.LoadBytes([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("expected strict error for retry, got: %v", err)
	}
}

func TestLoadBytes_StrictUnknownFieldInput(t *testing.T) {
	data := `
workflow:
  id: x
  inputs:
    - name: pkg
      kind: file   # 应该是 type
  steps:
    - id: s1
      command: ssh.exec
`
	_, err := workflow.LoadBytes([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected strict error for kind, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 解析错误：非 YAML / 空 / 缺 workflow key / 多文档
// -----------------------------------------------------------------------------

func TestLoadBytes_Empty(t *testing.T) {
	_, err := workflow.LoadBytes([]byte(""))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-document error, got: %v", err)
	}
}

func TestLoadBytes_MissingWorkflowKey(t *testing.T) {
	data := `
id: x
steps: []
`
	_, err := workflow.LoadBytes([]byte(data))
	// 严格模式下 id/steps 属于未知顶层字段，会先报未知字段错。
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadBytes_MultiDocument(t *testing.T) {
	data := `
workflow:
  id: a
  steps:
    - id: s1
      command: ssh.exec
---
workflow:
  id: b
  steps:
    - id: s1
      command: ssh.exec
`
	_, err := workflow.LoadBytes([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "multi-document") {
		t.Fatalf("expected multi-document error, got: %v", err)
	}
}

func TestLoadBytes_InvalidYAML(t *testing.T) {
	_, err := workflow.LoadBytes([]byte("workflow: [oops"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// -----------------------------------------------------------------------------
// Timeout / Validate 联动
// -----------------------------------------------------------------------------

func TestLoadBytes_InvalidTimeout(t *testing.T) {
	data := `
workflow:
  id: x
  steps:
    - id: s1
      command: ssh.exec
      timeout: forever
`
	_, err := workflow.LoadBytes([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout parse error, got: %v", err)
	}
}

func TestLoadBytes_ValidateFailurePropagates(t *testing.T) {
	// command 与 recipe 同时给：Validate 应触发。
	data := `
workflow:
  id: x
  steps:
    - id: s1
      command: ssh.exec
      recipe: system.cpu
`
	_, err := workflow.LoadBytes([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected validate error to propagate, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// LoadFile：找不到文件
// -----------------------------------------------------------------------------

func TestLoadFile_NotFound(t *testing.T) {
	_, err := workflow.LoadFile(filepath.Join("testdata", "no-such-file.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
