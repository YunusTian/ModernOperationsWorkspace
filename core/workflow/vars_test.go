package workflow_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mow/mow/core/workflow"
)

func newScope() workflow.Scope {
	return workflow.Scope{
		Inputs: map[string]any{
			"service": "myapp",
			"port":    8080,
			"debug":   true,
			"tags":    []any{"prod", "linux"},
		},
		Steps: map[string]workflow.StepScope{
			"upload": {Out: map[string]any{
				"bytes_sent": 12345,
				"file":       "app.tar.gz",
			}},
			"health": {Out: map[string]any{
				"status": "ok",
			}},
		},
	}
}

// -----------------------------------------------------------------------------
// 1. 无 ${} 原样返回（含非 string 原始类型）
// -----------------------------------------------------------------------------

func TestInterpolate_NoPlaceholderReturnsAsIs(t *testing.T) {
	got, err := workflow.Interpolate("plain text", newScope())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "plain text" {
		t.Errorf("got %v, want plain text", got)
	}
	// 非 string 原始类型
	got2, err := workflow.Interpolate(42, newScope())
	if err != nil || got2 != 42 {
		t.Errorf("int passthrough: got=%v err=%v", got2, err)
	}
	got3, err := workflow.Interpolate(nil, newScope())
	if err != nil || got3 != nil {
		t.Errorf("nil passthrough: got=%v err=%v", got3, err)
	}
}

// -----------------------------------------------------------------------------
// 2. 整串一个 ${expr} → 保留原始类型
// -----------------------------------------------------------------------------

func TestInterpolate_SinglePlaceholderPreservesType(t *testing.T) {
	sc := newScope()
	got, err := workflow.Interpolate("${inputs.port}", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 8080 {
		t.Errorf("want int 8080, got %T %v", got, got)
	}
	got, err = workflow.Interpolate("${inputs.debug}", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != true {
		t.Errorf("want bool true, got %T %v", got, got)
	}
}

// -----------------------------------------------------------------------------
// 3. 混合形态 → 字符串拼接
// -----------------------------------------------------------------------------

func TestInterpolate_MixedStringInterpolation(t *testing.T) {
	sc := newScope()
	got, err := workflow.Interpolate("systemctl stop ${inputs.service}", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "systemctl stop myapp" {
		t.Errorf("got %v", got)
	}
	// 多个占位符
	got, err = workflow.Interpolate("service=${inputs.service}, port=${inputs.port}", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "service=myapp, port=8080" {
		t.Errorf("got %v", got)
	}
}

// -----------------------------------------------------------------------------
// 4. steps.<id>.out.<field>
// -----------------------------------------------------------------------------

func TestInterpolate_StepsOut(t *testing.T) {
	sc := newScope()
	got, err := workflow.Interpolate("${steps.upload.out.bytes_sent}", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 12345 {
		t.Errorf("want 12345, got %T %v", got, got)
	}
	got, err = workflow.Interpolate("uploaded ${steps.upload.out.file} (${steps.upload.out.bytes_sent} bytes)", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "uploaded app.tar.gz (12345 bytes)" {
		t.Errorf("got %v", got)
	}
}

// -----------------------------------------------------------------------------
// 5. Map 递归
// -----------------------------------------------------------------------------

func TestInterpolate_MapRecursive(t *testing.T) {
	sc := newScope()
	in := map[string]any{
		"cmd":     "systemctl restart ${inputs.service}",
		"port":    "${inputs.port}",
		"literal": 100,
	}
	got, err := workflow.Interpolate(in, sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := got.(map[string]any)
	if m["cmd"] != "systemctl restart myapp" {
		t.Errorf("cmd: %v", m["cmd"])
	}
	if m["port"] != 8080 {
		t.Errorf("port: %T %v", m["port"], m["port"])
	}
	if m["literal"] != 100 {
		t.Errorf("literal: %v", m["literal"])
	}
	// 原 map 不应被修改
	if in["cmd"] != "systemctl restart ${inputs.service}" {
		t.Errorf("input map mutated: %v", in["cmd"])
	}
}

// -----------------------------------------------------------------------------
// 6. Slice 递归
// -----------------------------------------------------------------------------

func TestInterpolate_SliceRecursive(t *testing.T) {
	sc := newScope()
	in := []any{"${inputs.service}", "static", "${inputs.port}"}
	got, err := workflow.Interpolate(in, sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []any{"myapp", "static", 8080}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// -----------------------------------------------------------------------------
// 7. 深度嵌套 map+slice
// -----------------------------------------------------------------------------

func TestInterpolate_DeepNested(t *testing.T) {
	sc := newScope()
	in := map[string]any{
		"env": map[string]any{
			"SERVICE": "${inputs.service}",
			"TAGS":    []any{"${inputs.tags[0]}", "${inputs.tags[1]}"},
		},
		"cmds": []any{
			map[string]any{"exec": "start ${inputs.service}"},
		},
	}
	got, err := workflow.Interpolate(in, sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := got.(map[string]any)
	env := m["env"].(map[string]any)
	if env["SERVICE"] != "myapp" {
		t.Errorf("SERVICE=%v", env["SERVICE"])
	}
	tags := env["TAGS"].([]any)
	if tags[0] != "prod" || tags[1] != "linux" {
		t.Errorf("TAGS=%v", tags)
	}
	cmds := m["cmds"].([]any)
	inner := cmds[0].(map[string]any)
	if inner["exec"] != "start myapp" {
		t.Errorf("cmds[0].exec=%v", inner["exec"])
	}
}

// -----------------------------------------------------------------------------
// 8. 未知变量 → error 带偏移量
// -----------------------------------------------------------------------------

func TestInterpolate_UnknownVariable(t *testing.T) {
	sc := newScope()
	_, err := workflow.Interpolate("prefix ${inputs.missing}", sc)
	if err == nil {
		t.Fatal("expected error for unknown variable")
	}
	var ie *workflow.InterpolationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InterpolationError, got %T: %v", err, err)
	}
	if ie.Offset != 7 { // "prefix " 长度 = 7
		t.Errorf("offset = %d, want 7", ie.Offset)
	}
	if !strings.Contains(ie.Expr, "inputs.missing") {
		t.Errorf("expr = %q", ie.Expr)
	}
}

// -----------------------------------------------------------------------------
// 9. 未知 step out 字段
// -----------------------------------------------------------------------------

func TestInterpolate_UnknownStep(t *testing.T) {
	sc := newScope()
	_, err := workflow.Interpolate("${steps.no_such.out.x}", sc)
	if err == nil {
		t.Fatal("expected error for unknown step")
	}
	var ie *workflow.InterpolationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InterpolationError, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// 10. 语法错误 / 未闭合 ${
// -----------------------------------------------------------------------------

func TestInterpolate_SyntaxError(t *testing.T) {
	_, err := workflow.Interpolate("${inputs.}", newScope())
	if err == nil {
		t.Fatal("expected syntax error")
	}
	var ie *workflow.InterpolationError
	if !errors.As(err, &ie) {
		t.Fatalf("want InterpolationError, got %T", err)
	}
}

func TestInterpolate_UnterminatedPlaceholder(t *testing.T) {
	_, err := workflow.Interpolate("bad ${inputs.service", newScope())
	if err == nil {
		t.Fatal("expected unterminated error")
	}
	var ie *workflow.InterpolationError
	if !errors.As(err, &ie) {
		t.Fatalf("want InterpolationError, got %T", err)
	}
	if !strings.Contains(ie.Cause.Error(), "unterminated") {
		t.Errorf("cause = %v", ie.Cause)
	}
}

// -----------------------------------------------------------------------------
// 11. 表达式支持：条件 / 运算
// -----------------------------------------------------------------------------

func TestInterpolate_ExprConditional(t *testing.T) {
	sc := newScope()
	got, err := workflow.Interpolate("${inputs.port + 1}", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 8081 {
		t.Errorf("got %v", got)
	}
	got, err = workflow.Interpolate("${inputs.debug ? \"on\" : \"off\"}", sc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "on" {
		t.Errorf("got %v", got)
	}
}
