package command

import (
	"encoding/json"
)

// -----------------------------------------------------------------------------
// 参数脱敏（对齐 CommandSpec.InputSchema，递归实现）
// -----------------------------------------------------------------------------
//
// 约定：JSON Schema 中的字段级扩展属性 "x-mow-sensitive": true 表示该字段
// 在审计 / 日志 / AI 回写消息中必须脱敏。为保持向后兼容，字段级
// "sensitive": true 也识别。
//
// 递归规则（v0.2）：
//   - schema.type = "object" → 遍历 properties；命中 sensitive 的字段整体替换
//     为 "***"；子字段仍是 object / array 时继续下潜
//   - schema.type = "array"  → 对 items schema 应用到每个元素
//   - 其它类型             → 原值保留（叶子节点仅依赖父级 sensitive 标记）
//   - schema 与 params 结构不匹配时静默跳过对应分支，永不改写非目标位置
//   - 任一路径解析失败：整份 params 原样返回，避免脱敏问题挡住审计
//
// 顶层 params 可能是 object / array 之外的原语（例如插件把整个 params 声明为
// string）；此种情况保持原逻辑：非对象参数原样返回。

// sensitiveMask 是替换值，导出便于测试断言。
const sensitiveMask = "***"

// RedactParams 使用 schema 描述的 sensitive 字段清单，返回一份脱敏后的 params JSON。
// schema、params 任一为空时按"无敏感字段"处理。
func RedactParams(schema, params json.RawMessage) json.RawMessage {
	if len(params) == 0 || len(schema) == 0 {
		return params
	}
	// 解析 schema 与 params 为可编辑的 any 树。
	var schemaNode any
	if err := json.Unmarshal(schema, &schemaNode); err != nil {
		return params
	}
	var paramsNode any
	if err := json.Unmarshal(params, &paramsNode); err != nil {
		return params
	}
	redacted, changed := redactValue(schemaNode, paramsNode)
	if !changed {
		return params
	}
	out, err := json.Marshal(redacted)
	if err != nil {
		return params
	}
	return out
}

// redactValue 递归遍历 (schema, value)，返回脱敏后的 value 以及是否发生改动。
// schema 或 value 类型不匹配时原样返回。
func redactValue(schema, value any) (any, bool) {
	sm, ok := schema.(map[string]any)
	if !ok {
		return value, false
	}
	switch v := value.(type) {
	case map[string]any:
		return redactObject(sm, v)
	case []any:
		return redactArray(sm, v)
	default:
		return value, false
	}
}

// redactObject 处理 object 分支：遍历 properties，命中 sensitive → 掩码；
// 否则若子字段仍是 object / array 则递归。
func redactObject(schema map[string]any, obj map[string]any) (map[string]any, bool) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return obj, false
	}
	changed := false
	for name, rawSub := range props {
		subSchema, ok := rawSub.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := obj[name]; !exists {
			continue
		}
		if isSensitive(subSchema) {
			obj[name] = sensitiveMask
			changed = true
			continue
		}
		newV, sub := redactValue(subSchema, obj[name])
		if sub {
			obj[name] = newV
			changed = true
		}
	}
	return obj, changed
}

// redactArray 处理 array 分支：把 items schema 应用到每个元素。
func redactArray(schema map[string]any, arr []any) ([]any, bool) {
	itemsRaw, ok := schema["items"].(map[string]any)
	if !ok {
		return arr, false
	}
	changed := false
	for i, el := range arr {
		newV, sub := redactValue(itemsRaw, el)
		if sub {
			arr[i] = newV
			changed = true
		}
	}
	return arr, changed
}

// isSensitive 判断当前 schema 节点是否被显式标注为敏感。
// 优先识别 "x-mow-sensitive"；兼容 "sensitive"。
func isSensitive(s map[string]any) bool {
	if v, ok := s["x-mow-sensitive"].(bool); ok && v {
		return true
	}
	if v, ok := s["sensitive"].(bool); ok && v {
		return true
	}
	return false
}
