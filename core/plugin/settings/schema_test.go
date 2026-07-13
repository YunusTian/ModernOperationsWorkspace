package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompileEmpty(t *testing.T) {
	s, err := Compile(nil)
	if err != nil || s != nil {
		t.Fatalf("expected nil schema for empty raw; got %v %v", s, err)
	}
	s, err = Compile(json.RawMessage(`null`))
	if err != nil || s != nil {
		t.Fatalf("expected nil for null; got %v %v", s, err)
	}
}

func TestCompileTopLevelMustBeObject(t *testing.T) {
	_, err := Compile(json.RawMessage(`{"type":"string"}`))
	if err == nil {
		t.Fatal("expected error for non-object top-level")
	}
}

func TestValidateBasicShapes(t *testing.T) {
	s, err := Compile(json.RawMessage(`{
	  "type": "object",
	  "additionalProperties": false,
	  "required": ["name"],
	  "properties": {
	    "name":    {"type": "string", "minLength": 1, "maxLength": 32, "pattern": "^[a-z]+$"},
	    "port":    {"type": "integer", "minimum": 1, "maximum": 65535, "default": 22},
	    "level":   {"type": "string", "enum": ["debug","info","warn","error"]},
	    "verbose": {"type": "boolean"},
	    "options": {"type": "object", "additionalProperties": true, "properties": {"x": {"type": "number"}}}
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	// happy path
	errs := s.Validate(json.RawMessage(`{"name":"ssh","port":22,"level":"info","verbose":true,"options":{"x":1.5}}`))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	// missing required + additional field + bad enum + bad pattern
	errs = s.Validate(json.RawMessage(`{"level":"trace","weird":true,"name":"BadName"}`))
	msgs := errorMessages(errs)
	wantContains(t, msgs, "level: must be one of")
	wantContains(t, msgs, "name: does not match pattern")
	wantContains(t, msgs, "weird: unknown field")
	// missing required checked separately
	errs = s.Validate(json.RawMessage(`{}`))
	if len(errs) == 0 || errs[0].Path != "name" {
		t.Fatalf("expected required 'name' error; got %+v", errs)
	}
}

func TestValidateInteger(t *testing.T) {
	s, _ := Compile(json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","minimum":0}}}`))
	errs := s.Validate(json.RawMessage(`{"n":1.5}`))
	if len(errs) == 0 {
		t.Fatal("expected fractional to fail")
	}
	if errs[0].Message != "expected integer, got fractional" && errs[0].Path != "n" {
		t.Fatalf("unexpected: %+v", errs)
	}
}

func TestApplyDefaultsSkipsPresent(t *testing.T) {
	s, _ := Compile(json.RawMessage(`{
	  "type":"object",
	  "properties":{
	    "port":{"type":"integer","default":22},
	    "name":{"type":"string","default":"anon"},
	    "opts":{"type":"object","properties":{"retry":{"type":"integer","default":3}}}
	  }
	}`))
	// 空 → 全部默认
	got, err := s.ApplyDefaults(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(got), `"port":22`, `"name":"anon"`) {
		t.Fatalf("defaults not applied: %s", got)
	}
	// 显式提供的值应保留
	got, err = s.ApplyDefaults(json.RawMessage(`{"port":2222,"opts":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(got), `"port":2222`, `"name":"anon"`, `"retry":3`) {
		t.Fatalf("bad defaults merge: %s", got)
	}
}

func TestRedactSecretFields(t *testing.T) {
	s, _ := Compile(json.RawMessage(`{
	  "type":"object",
	  "properties":{
	    "api_key":{"type":"string","secret":true},
	    "providers":{"type":"array","items":{"type":"object","properties":{"token":{"type":"string","secret":true},"name":{"type":"string"}}}}
	  }
	}`))
	got, err := s.Redact(json.RawMessage(`{"api_key":"sk-live-xxxxx","providers":[{"name":"openai","token":"tk-yyyyy"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `"api_key":"***"`
	if !strings.Contains(string(got), want) {
		t.Fatalf("expected redaction: %s", got)
	}
	if !strings.Contains(string(got), `"token":"***"`) {
		t.Fatalf("expected nested redaction: %s", got)
	}
	// Non-secret name should remain
	if !strings.Contains(string(got), `"name":"openai"`) {
		t.Fatalf("non-secret should remain: %s", got)
	}
}

func TestSecretPaths(t *testing.T) {
	s, _ := Compile(json.RawMessage(`{
	  "type":"object",
	  "properties":{
	    "api_key":{"type":"string","secret":true},
	    "providers":{"type":"array","items":{"type":"object","properties":{"token":{"type":"string","secret":true}}}}
	  }
	}`))
	got := s.SecretPaths()
	if len(got) != 2 || got[0] != "api_key" || got[1] != "providers[].token" {
		t.Fatalf("unexpected secret paths: %v", got)
	}
}

func TestFieldsOrderAndDepth(t *testing.T) {
	s, _ := Compile(json.RawMessage(`{
	  "type":"object",
	  "properties":{
	    "b":{"type":"string"},
	    "a":{"type":"object","properties":{"z":{"type":"string"},"y":{"type":"integer"}}}
	  }
	}`))
	fields := s.Fields()
	// 顺序：a, a.y, a.z, b
	if len(fields) != 4 {
		t.Fatalf("got %d fields", len(fields))
	}
	want := []string{"a", "a.y", "a.z", "b"}
	for i, f := range fields {
		if f.Path != want[i] {
			t.Fatalf("field %d = %q, want %q", i, f.Path, want[i])
		}
	}
	if fields[0].Depth != 0 || fields[1].Depth != 1 {
		t.Fatalf("bad depths: %+v", fields)
	}
}

func TestSetAndGetPath(t *testing.T) {
	raw := json.RawMessage(`{"a":{"b":1}}`)
	// set 已有字段
	next, err := SetPath(raw, "a.b", 42)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, _ := GetPath(next, "a.b")
	if !ok || strings.TrimSpace(string(got)) != "42" {
		t.Fatalf("set failed: %s", got)
	}
	// 自动创建中间层
	next, err = SetPath(next, "x.y.z", "hi")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, _ = GetPath(next, "x.y.z")
	if !ok || strings.TrimSpace(string(got)) != `"hi"` {
		t.Fatalf("nested set failed: %s", got)
	}
	// 删除
	next, err = SetPath(next, "a.b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := GetPath(next, "a.b"); ok {
		t.Fatal("expected a.b to be removed")
	}
	// GetPath 不存在
	if _, ok, _ := GetPath(next, "no.such"); ok {
		t.Fatal("expected missing path")
	}
}

func TestValidateEnumMixedTypes(t *testing.T) {
	s, _ := Compile(json.RawMessage(`{"type":"object","properties":{"v":{"enum":[1,"two",true]}}}`))
	if errs := s.Validate(json.RawMessage(`{"v":1}`)); len(errs) != 0 {
		t.Fatalf("expected 1 to pass: %+v", errs)
	}
	if errs := s.Validate(json.RawMessage(`{"v":"two"}`)); len(errs) != 0 {
		t.Fatalf("expected \"two\" to pass: %+v", errs)
	}
	if errs := s.Validate(json.RawMessage(`{"v":"three"}`)); len(errs) == 0 {
		t.Fatalf("expected reject \"three\"")
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func errorMessages(errs []Error) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}

func wantContains(t *testing.T, haystack []string, needle string) {
	t.Helper()
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return
		}
	}
	t.Fatalf("expected %q in %v", needle, haystack)
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}
