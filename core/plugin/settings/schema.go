// Package settings 实现 Manifest.settingsSchema 驱动的插件配置。
//
// 设计要点（v0.5.2 P1）：
//   - 解析 JSON Schema 子集（type / properties / required / enum / default /
//     description / minimum / maximum / minLength / maxLength / pattern /
//     items / additionalProperties / secret / format / title）
//   - 语义化 "secret" 关键字：标记敏感字段，UI 脱敏输入 + 展示，
//     日志/审计 redact；真正的 keyring 存储可在后续批次落地
//   - 提供 Compile / Validate / ApplyDefaults / Redact 四个入口
//   - 不引入 github.com/santhosh-tekuri/jsonschema/v5：那是重量级 draft
//     完整实现，Manifest.settingsSchema 只需要子集，且 secret 是自定义关键字
//
// 该包被 CLI (`mow plugin config`) 与 Desktop (Wails 绑定) 共用。
package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Schema 描述一个 settingsSchema 顶层对象。仅支持 object 顶层
// （约束与 Manifest schema 的 v0.5.0 声明一致：`settingsSchema.type == "object"`）。
type Schema struct {
	// Root 是顶层 object node。
	Root *Node
	// Order 记录顶层字段的字典序集合，便于 UI 稳定渲染。
	Order []string
}

// Node 是 Schema 内部节点，覆盖 JSON Schema 关键字子集。
type Node struct {
	// Type 允许的取值："object" / "array" / "string" / "integer" / "number" /
	// "boolean"；空字符串表示不校验类型（等价于任意）。
	Type string
	// Title 与 Description 用于 UI 渲染。
	Title       string
	Description string
	// Properties（type=object 有效）：字段 → 子节点；Required 是必填字段集合。
	Properties map[string]*Node
	Required   map[string]struct{}
	// Order 记录 properties 字典序（保证 UI 稳定）。
	Order []string
	// AllowAdditional：默认 true；additionalProperties=false 时置为 false。
	AllowAdditional bool
	// Items（type=array 有效）：数组元素 schema；nil 表示不校验。
	Items *Node
	// Enum：任意 JSON 值列表；非空时校验值必须命中。
	Enum []any
	// Default：默认值原始 JSON；ApplyDefaults 会在 property 缺失时注入。
	Default    json.RawMessage
	hasDefault bool
	// 数字约束
	Minimum *float64
	Maximum *float64
	// 字符串约束
	MinLength *int
	MaxLength *int
	Pattern   *regexp.Regexp
	// Secret：true → 敏感字段，UI 脱敏、日志 redact。
	Secret bool
	// Format 只作 UI 提示（如 "url" / "email"），不参与业务校验。
	Format string
	// Raw 保留原始 schema JSON，供 UI / 未来扩展消费。
	Raw json.RawMessage
}

// Compile 把 Manifest.SettingsSchema 的 raw JSON 编译成 Schema。
// raw 为空 / null → 返回 nil schema（视为"无约束"）。
func Compile(raw json.RawMessage) (*Schema, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("settings: parse schema: %w", err)
	}
	n, err := compileNode(root, "")
	if err != nil {
		return nil, err
	}
	if n.Type != "" && n.Type != "object" {
		return nil, fmt.Errorf("settings: top-level schema type must be object, got %q", n.Type)
	}
	return &Schema{Root: n, Order: append([]string(nil), n.Order...)}, nil
}

func compileNode(m map[string]any, path string) (*Node, error) {
	n := &Node{AllowAdditional: true}
	if raw, err := json.Marshal(m); err == nil {
		n.Raw = raw
	}
	if v, ok := m["type"].(string); ok {
		switch v {
		case "object", "array", "string", "integer", "number", "boolean":
			n.Type = v
		default:
			return nil, fmt.Errorf("settings: unsupported type %q at %s", v, pathOr(path, "<root>"))
		}
	}
	if s, ok := m["title"].(string); ok {
		n.Title = s
	}
	if s, ok := m["description"].(string); ok {
		n.Description = s
	}
	if s, ok := m["format"].(string); ok {
		n.Format = s
	}
	if b, ok := m["secret"].(bool); ok {
		n.Secret = b
	}
	if def, ok := m["default"]; ok {
		raw, err := json.Marshal(def)
		if err != nil {
			return nil, fmt.Errorf("settings: default at %s: %w", pathOr(path, "<root>"), err)
		}
		n.Default = raw
		n.hasDefault = true
	}
	if enum, ok := m["enum"].([]any); ok {
		n.Enum = append([]any(nil), enum...)
	}
	if v, ok := numberFrom(m["minimum"]); ok {
		n.Minimum = &v
	}
	if v, ok := numberFrom(m["maximum"]); ok {
		n.Maximum = &v
	}
	if v, ok := intFrom(m["minLength"]); ok {
		n.MinLength = &v
	}
	if v, ok := intFrom(m["maxLength"]); ok {
		n.MaxLength = &v
	}
	if s, ok := m["pattern"].(string); ok {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("settings: bad pattern at %s: %w", pathOr(path, "<root>"), err)
		}
		n.Pattern = re
	}
	if props, ok := m["properties"].(map[string]any); ok {
		n.Properties = make(map[string]*Node, len(props))
		names := make([]string, 0, len(props))
		for name, sub := range props {
			subMap, ok := sub.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("settings: property %s at %s must be an object", name, pathOr(path, "<root>"))
			}
			child, err := compileNode(subMap, pathOr(path, "<root>")+"."+name)
			if err != nil {
				return nil, err
			}
			n.Properties[name] = child
			names = append(names, name)
		}
		sort.Strings(names)
		n.Order = names
	}
	if req, ok := m["required"].([]any); ok {
		n.Required = map[string]struct{}{}
		for _, r := range req {
			if s, ok := r.(string); ok {
				n.Required[s] = struct{}{}
			}
		}
	}
	if ap, ok := m["additionalProperties"]; ok {
		if b, isBool := ap.(bool); isBool {
			n.AllowAdditional = b
		}
	}
	if items, ok := m["items"].(map[string]any); ok {
		child, err := compileNode(items, pathOr(path, "<root>")+"[]")
		if err != nil {
			return nil, err
		}
		n.Items = child
	}
	return n, nil
}

// Field 是 UI 侧展示单元；由 Schema.Fields() 返回。
type Field struct {
	// Path 是点号路径，例："providers"、"providers[].options"。
	Path string
	// Node 引用到 compiled schema 节点（Type / Enum / 默认 / 校验等元信息）。
	Node *Node
	// Depth 便于 UI 缩进（0 = 顶层字段）。
	Depth int
}

// Fields 返回稳定顺序的字段清单，深度优先遍历 object 子树。
// 数组 / 标量作为叶子；不会递归进 array items（那由 UI 决定如何渲染）。
func (s *Schema) Fields() []Field {
	if s == nil || s.Root == nil {
		return nil
	}
	var out []Field
	var walk func(prefix string, n *Node, depth int)
	walk = func(prefix string, n *Node, depth int) {
		if n == nil || n.Type != "object" || len(n.Properties) == 0 {
			return
		}
		for _, name := range n.Order {
			child := n.Properties[name]
			full := name
			if prefix != "" {
				full = prefix + "." + name
			}
			out = append(out, Field{Path: full, Node: child, Depth: depth})
			if child.Type == "object" && len(child.Properties) > 0 {
				walk(full, child, depth+1)
			}
		}
	}
	walk("", s.Root, 0)
	return out
}

// Error 描述一次校验失败。
type Error struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e Error) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// Validate 对 raw settings JSON 做类型 / required / enum / 数字/字符串约束校验。
// 未匹配 schema 但 additionalProperties=false 的字段会被拒绝。
// 返回值按路径字典序稳定排序。
func (s *Schema) Validate(raw json.RawMessage) []Error {
	if s == nil || s.Root == nil {
		return nil
	}
	var val any
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		val = map[string]any{}
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&val); err != nil {
			return []Error{{Path: "", Message: "invalid JSON: " + err.Error()}}
		}
	}
	var errs []Error
	validateNode("", s.Root, val, &errs)
	sort.Slice(errs, func(i, j int) bool { return errs[i].Path < errs[j].Path })
	return errs
}

func validateNode(path string, n *Node, val any, errs *[]Error) {
	if n == nil {
		return
	}
	if val == nil {
		return
	}
	switch n.Type {
	case "object":
		m, ok := val.(map[string]any)
		if !ok {
			*errs = append(*errs, Error{Path: path, Message: "expected object"})
			return
		}
		for req := range n.Required {
			if _, ok := m[req]; !ok {
				*errs = append(*errs, Error{Path: joinPath(path, req), Message: "required"})
			}
		}
		for name, child := range n.Properties {
			if v, ok := m[name]; ok {
				validateNode(joinPath(path, name), child, v, errs)
			}
		}
		if !n.AllowAdditional {
			for k := range m {
				if _, known := n.Properties[k]; !known {
					*errs = append(*errs, Error{Path: joinPath(path, k), Message: "unknown field (additionalProperties=false)"})
				}
			}
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			*errs = append(*errs, Error{Path: path, Message: "expected array"})
			return
		}
		for i, item := range arr {
			validateNode(fmt.Sprintf("%s[%d]", path, i), n.Items, item, errs)
		}
	case "string":
		s, ok := val.(string)
		if !ok {
			*errs = append(*errs, Error{Path: path, Message: "expected string"})
			return
		}
		if n.MinLength != nil && len(s) < *n.MinLength {
			*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("length %d < minLength %d", len(s), *n.MinLength)})
		}
		if n.MaxLength != nil && len(s) > *n.MaxLength {
			*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("length %d > maxLength %d", len(s), *n.MaxLength)})
		}
		if n.Pattern != nil && !n.Pattern.MatchString(s) {
			*errs = append(*errs, Error{Path: path, Message: "does not match pattern " + n.Pattern.String()})
		}
	case "integer", "number":
		f, ok := floatFrom(val)
		if !ok {
			*errs = append(*errs, Error{Path: path, Message: "expected " + n.Type})
			return
		}
		if n.Type == "integer" && f != float64(int64(f)) {
			*errs = append(*errs, Error{Path: path, Message: "expected integer, got fractional"})
		}
		if n.Minimum != nil && f < *n.Minimum {
			*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("value %v < minimum %v", f, *n.Minimum)})
		}
		if n.Maximum != nil && f > *n.Maximum {
			*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("value %v > maximum %v", f, *n.Maximum)})
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			*errs = append(*errs, Error{Path: path, Message: "expected boolean"})
		}
	}
	if len(n.Enum) > 0 {
		if !enumContains(n.Enum, val) {
			*errs = append(*errs, Error{Path: path, Message: enumMessage(n.Enum)})
		}
	}
}

// ApplyDefaults 递归地把 schema 中声明的 default 注入 raw 里缺失的字段，
// 返回新的 JSON 字节；不改动 raw。
func (s *Schema) ApplyDefaults(raw json.RawMessage) (json.RawMessage, error) {
	if s == nil || s.Root == nil {
		return raw, nil
	}
	var val any
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		val = map[string]any{}
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("settings: parse raw: %w", err)
		}
	}
	next, err := applyDefaultsNode(s.Root, val)
	if err != nil {
		return nil, err
	}
	return json.Marshal(next)
}

func applyDefaultsNode(n *Node, val any) (any, error) {
	if n == nil {
		return val, nil
	}
	switch n.Type {
	case "object":
		m, ok := val.(map[string]any)
		if !ok {
			if val != nil {
				return val, nil
			}
			m = map[string]any{}
		}
		for name, child := range n.Properties {
			cur, present := m[name]
			if !present && child.hasDefault {
				var d any
				dec := json.NewDecoder(bytes.NewReader(child.Default))
				dec.UseNumber()
				if err := dec.Decode(&d); err != nil {
					return nil, err
				}
				cur = d
				present = true
			}
			if present {
				next, err := applyDefaultsNode(child, cur)
				if err != nil {
					return nil, err
				}
				m[name] = next
			}
		}
		return m, nil
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return val, nil
		}
		for i, item := range arr {
			next, err := applyDefaultsNode(n.Items, item)
			if err != nil {
				return nil, err
			}
			arr[i] = next
		}
		return arr, nil
	}
	return val, nil
}

// Redact 返回一份 raw 的拷贝，把所有 Secret=true 字段替换成 "***"。
// 用于 CLI list / desktop 展示 / 审计日志。raw 为空 → 返回原样。
func (s *Schema) Redact(raw json.RawMessage) (json.RawMessage, error) {
	if s == nil || s.Root == nil || len(bytes.TrimSpace(raw)) == 0 {
		return raw, nil
	}
	var val any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&val); err != nil {
		return nil, fmt.Errorf("settings: parse raw: %w", err)
	}
	redactNode(s.Root, val)
	return json.Marshal(val)
}

func redactNode(n *Node, val any) {
	if n == nil || val == nil {
		return
	}
	switch n.Type {
	case "object":
		m, ok := val.(map[string]any)
		if !ok {
			return
		}
		for name, child := range n.Properties {
			cur, present := m[name]
			if !present {
				continue
			}
			if child.Secret && cur != nil {
				m[name] = "***"
				continue
			}
			redactNode(child, cur)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return
		}
		for _, item := range arr {
			redactNode(n.Items, item)
		}
	}
}

// SecretPaths 返回全部 secret 字段的路径（object.property 形式；数组用 `[]`）。
func (s *Schema) SecretPaths() []string {
	if s == nil || s.Root == nil {
		return nil
	}
	var out []string
	var walk func(prefix string, n *Node)
	walk = func(prefix string, n *Node) {
		if n == nil {
			return
		}
		if n.Secret && prefix != "" {
			out = append(out, prefix)
			return
		}
		switch n.Type {
		case "object":
			for _, name := range n.Order {
				sub := name
				if prefix != "" {
					sub = prefix + "." + name
				}
				walk(sub, n.Properties[name])
			}
		case "array":
			walk(prefix+"[]", n.Items)
		}
	}
	walk("", s.Root)
	sort.Strings(out)
	return out
}

// SetPath 在 raw settings JSON 中设置指定路径的值，返回新的 JSON。
// 路径语法支持 `a.b.c`；数组下标暂不支持（v0.5.2 P1 覆盖 CLI/Desktop
// 主要用例：scalar 字段 & object 嵌套；providers[] 由 UI 特殊化处理）。
// value 可以是 string / number / bool / null / json.RawMessage / map / slice。
func SetPath(raw json.RawMessage, path string, value any) (json.RawMessage, error) {
	if path == "" {
		return nil, fmt.Errorf("settings: empty path")
	}
	var val any
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		val = map[string]any{}
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("settings: parse raw: %w", err)
		}
	}
	m, ok := val.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("settings: top-level must be object")
	}
	parts := strings.Split(path, ".")
	cur := m
	for i, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			// 允许自动创建中间层，与 CLI/UI 交互习惯匹配。
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
		_ = i
	}
	// 最后一层：如果 value 是 json.RawMessage，尝试 decode 成 any；否则直接放。
	last := parts[len(parts)-1]
	switch v := value.(type) {
	case json.RawMessage:
		var out any
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			return nil, fmt.Errorf("settings: decode value at %s: %w", path, err)
		}
		cur[last] = out
	case nil:
		delete(cur, last)
	default:
		cur[last] = v
	}
	return json.Marshal(m)
}

// GetPath 读取 raw 中指定路径的值；不存在时返回 (nil, false, nil)。
// 与 SetPath 对偶，仅支持 `.` 分隔的 object 路径。
func GetPath(raw json.RawMessage, path string) (json.RawMessage, bool, error) {
	if path == "" {
		return raw, true, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	var val any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&val); err != nil {
		return nil, false, err
	}
	parts := strings.Split(path, ".")
	for _, p := range parts {
		m, ok := val.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		next, ok := m[p]
		if !ok {
			return nil, false, nil
		}
		val = next
	}
	out, err := json.Marshal(val)
	return out, true, err
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func joinPath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}

func pathOr(path, fallback string) string {
	if path == "" {
		return fallback
	}
	return path
}

func numberFrom(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return t, true
	case int:
		return float64(t), true
	}
	return 0, false
}

func intFrom(v any) (int, bool) {
	switch t := v.(type) {
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case float64:
		return int(t), true
	case int:
		return t, true
	}
	return 0, false
}

func floatFrom(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return t, true
	case int:
		return float64(t), true
	}
	return 0, false
}

func enumContains(list []any, v any) bool {
	target, err := canonical(v)
	if err != nil {
		return false
	}
	for _, e := range list {
		if got, err := canonical(e); err == nil && got == target {
			return true
		}
	}
	return false
}

func canonical(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func enumMessage(list []any) string {
	parts := make([]string, 0, len(list))
	for _, v := range list {
		b, _ := json.Marshal(v)
		parts = append(parts, string(b))
	}
	sort.Strings(parts)
	return "must be one of " + strings.Join(parts, ", ")
}
