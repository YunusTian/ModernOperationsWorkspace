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

// -----------------------------------------------------------------------------
// 递归脱敏（v0.2）
// -----------------------------------------------------------------------------

func TestRedactParams_NestedObject(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"auth":{
				"type":"object",
				"properties":{
					"password":{"type":"string","x-mow-sensitive":true},
					"user":{"type":"string"}
				}
			}
		}
	}`)
	params := json.RawMessage(`{"auth":{"user":"alice","password":"hunter2"}}`)
	out := RedactParams(schema, params)
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("nested secret leaked: %s", out)
	}
	if !strings.Contains(string(out), `"alice"`) {
		t.Fatalf("non-sensitive nested field lost: %s", out)
	}
	if !strings.Contains(string(out), `"password":"***"`) {
		t.Fatalf("nested mask missing: %s", out)
	}
}

func TestRedactParams_SensitiveObjectWholeTree(t *testing.T) {
	// 整个 object 被标 sensitive → 顶层直接掩码，子字段不再递归。
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"credentials":{"type":"object","x-mow-sensitive":true,"properties":{"token":{"type":"string"}}}
		}
	}`)
	params := json.RawMessage(`{"credentials":{"token":"abc","refresh":"def"}}`)
	out := RedactParams(schema, params)
	if strings.Contains(string(out), "abc") || strings.Contains(string(out), "def") {
		t.Fatalf("sensitive object leaked: %s", out)
	}
	if !strings.Contains(string(out), `"credentials":"***"`) {
		t.Fatalf("object mask missing: %s", out)
	}
}

func TestRedactParams_ArrayItems(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"tokens":{
				"type":"array",
				"items":{
					"type":"object",
					"properties":{
						"value":{"type":"string","x-mow-sensitive":true},
						"name":{"type":"string"}
					}
				}
			}
		}
	}`)
	params := json.RawMessage(`{"tokens":[{"name":"prod","value":"s1"},{"name":"dev","value":"s2"}]}`)
	out := RedactParams(schema, params)
	if strings.Contains(string(out), `"s1"`) || strings.Contains(string(out), `"s2"`) {
		t.Fatalf("array items leaked: %s", out)
	}
	// 验证结构与非敏感字段保留
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	arr, _ := got["tokens"].([]any)
	if len(arr) != 2 {
		t.Fatalf("array length: %d", len(arr))
	}
	first := arr[0].(map[string]any)
	if first["name"] != "prod" || first["value"] != sensitiveMask {
		t.Fatalf("first item bad: %+v", first)
	}
}

func TestRedactParams_DeepNested(t *testing.T) {
	// 三层嵌套：确保递归不会中途丢字段
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"a":{"type":"object","properties":{
				"b":{"type":"object","properties":{
					"secret":{"type":"string","x-mow-sensitive":true},
					"leaf":{"type":"string"}
				}}
			}}
		}
	}`)
	params := json.RawMessage(`{"a":{"b":{"secret":"XXX","leaf":"keep"}}}`)
	out := RedactParams(schema, params)
	if strings.Contains(string(out), "XXX") {
		t.Fatalf("deep secret leaked: %s", out)
	}
	if !strings.Contains(string(out), `"leaf":"keep"`) {
		t.Fatalf("deep leaf lost: %s", out)
	}
}
