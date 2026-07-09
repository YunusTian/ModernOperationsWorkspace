package command

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactParams_TopLevelSensitive(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"cmd":      {"type":"string"},
			"password": {"type":"string","x-mow-sensitive":true}
		}
	}`)
	params := json.RawMessage(`{"cmd":"whoami","password":"hunter2"}`)

	out := RedactParams(schema, params)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["cmd"] != "whoami" {
		t.Errorf("cmd should stay: %v", m["cmd"])
	}
	if m["password"] != sensitiveMask {
		t.Errorf("password should be masked: %v", m["password"])
	}
	if strings.Contains(string(out), "hunter2") {
		t.Errorf("secret leaked: %s", out)
	}
}

func TestRedactParams_LegacySensitiveAlias(t *testing.T) {
	// 兼容 "sensitive": true 的旧写法
	schema := json.RawMessage(`{"properties":{"token":{"sensitive":true}}}`)
	params := json.RawMessage(`{"token":"abc"}`)
	out := RedactParams(schema, params)
	if strings.Contains(string(out), "abc") {
		t.Errorf("legacy alias not masked: %s", out)
	}
}

func TestRedactParams_NoSchema(t *testing.T) {
	params := json.RawMessage(`{"password":"hunter2"}`)
	out := RedactParams(nil, params)
	if string(out) != string(params) {
		t.Errorf("without schema, params should pass through")
	}
}

func TestRedactParams_SchemaWithoutSensitive(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"cmd":{"type":"string"}}}`)
	params := json.RawMessage(`{"cmd":"ls"}`)
	out := RedactParams(schema, params)
	if string(out) != string(params) {
		t.Errorf("no sensitive fields → identity, got %s", out)
	}
}

func TestRedactParams_MissingField(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"password":{"x-mow-sensitive":true}}}`)
	params := json.RawMessage(`{"cmd":"ls"}`)
	out := RedactParams(schema, params)
	// 未出现的敏感字段：无需替换，原样返回
	if string(out) != string(params) {
		t.Errorf("params should stay identical when sensitive field absent")
	}
}

func TestRedactParams_InvalidJSON(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"pwd":{"x-mow-sensitive":true}}}`)
	// 非对象也不应 panic
	out := RedactParams(schema, json.RawMessage(`"just a string"`))
	if string(out) != `"just a string"` {
		t.Errorf("non-object params should pass through: %s", out)
	}
}
