package command

import (
	"encoding/json"
)

// -----------------------------------------------------------------------------
// 参数脱敏（对齐 CommandSpec.InputSchema）
// -----------------------------------------------------------------------------
//
// 约定：JSON Schema 中的字段级扩展属性 "x-mow-sensitive": true 表示该字段
// 在审计 / 日志中必须脱敏。
//
// 示例 schema：
//   {
//     "type": "object",
//     "properties": {
//       "cmd":      {"type": "string"},
//       "password": {"type": "string", "x-mow-sensitive": true}
//     }
//   }
//
// 脱敏动作：
//   - 命中字段：替换为 "***" 字符串，保留 key 结构，方便审计里看到字段名
//   - 非对象参数（如数组或原语）：原样返回
//   - schema 解析失败：兜底原样返回，不能因为脱敏问题挡住审计
//
// v0.1 只处理顶层字段；嵌套对象里的敏感字段待 v0.2 引入递归。

// sensitiveMask 是替换值，导出便于测试断言。
const sensitiveMask = "***"

// RedactParams 使用 schema 描述的 sensitive 字段清单，返回一份脱敏后的 params JSON。
// schema、params 任一为空时按"无敏感字段"处理。
func RedactParams(schema, params json.RawMessage) json.RawMessage {
	if len(params) == 0 {
		return params
	}
	names := sensitiveFields(schema)
	if len(names) == 0 {
		return params
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(params, &m); err != nil {
		// 非对象或非法 JSON：不脱敏，交由上层自处。
		return params
	}
	changed := false
	for _, n := range names {
		if _, ok := m[n]; ok {
			maskBytes, _ := json.Marshal(sensitiveMask)
			m[n] = maskBytes
			changed = true
		}
	}
	if !changed {
		return params
	}
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return out
}

// sensitiveFields 解析 JSON Schema 的 properties，返回带 "x-mow-sensitive": true 的字段名列表。
// schema 解析失败时返回 nil。
func sensitiveFields(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var doc struct {
		Properties map[string]struct {
			Sensitive bool `json:"x-mow-sensitive,omitempty"`
			// 兼容 "sensitive": true 的旧写法（若有）
			SensitiveAlt bool `json:"sensitive,omitempty"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil
	}
	var out []string
	for name, p := range doc.Properties {
		if p.Sensitive || p.SensitiveAlt {
			out = append(out, name)
		}
	}
	return out
}
