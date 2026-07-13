// Package settings 补充：secret 字段的隔离存储。
//
// 语义（v0.5.2 P1 第二批）：
//   - config.json 里 plugins.<id>.settings 只存"非 secret 字段"
//   - secret 字段单独落到 <DataDir>/plugin-secrets/<id>.json（dir 0o700 / file 0o600）
//   - Init 前用 Merge 把两份合并成完整 settings 交给插件
//   - 用户改 secret 时用 Split 把它剥回 sidecar
//
// 该文件只与 core/plugin/settings 的 Schema 打交道，避免耦合 core/config。
package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// SecretStore 描述 secret sidecar 的落盘位置与安全约束。
// 每个 plugin 一个 <BaseDir>/<id>.json；BaseDir 建议为 `<DataDir>/plugin-secrets`。
type SecretStore struct {
	BaseDir string
}

// NewStoreFromDataDir 返回一个标准布局的 SecretStore：`<DataDir>/plugin-secrets`。
// CLI 与 Desktop 都通过它拿到相同的 sidecar 目录。
func NewStoreFromDataDir(dataDir string) SecretStore {
	return SecretStore{BaseDir: filepath.Join(dataDir, "plugin-secrets")}
}

// idPattern 与 Lifecycle 保持一致，防止路径穿越。
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)

func (s SecretStore) pathFor(id string) (string, error) {
	if s.BaseDir == "" {
		return "", fmt.Errorf("secret store: BaseDir is empty")
	}
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("secret store: invalid plugin id %q", id)
	}
	return filepath.Join(s.BaseDir, id+".json"), nil
}

// Load 读取指定插件的 secret sidecar；不存在返回 (nil, false, nil)。
func (s SecretStore) Load(id string) (json.RawMessage, bool, error) {
	path, err := s.pathFor(id)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("secret store: read %s: %w", path, err)
	}
	return json.RawMessage(data), true, nil
}

// Save 写入 secret sidecar；raw 为空 / null / "{}" 时**改写为删除**，保持磁盘干净。
// 目录不存在会以 0o700 创建；文件权限固定为 0o600。
func (s SecretStore) Save(id string, raw json.RawMessage) error {
	path, err := s.pathFor(id)
	if err != nil {
		return err
	}
	if isEmptyObject(raw) {
		return s.Delete(id)
	}
	if err := os.MkdirAll(s.BaseDir, 0o700); err != nil {
		return fmt.Errorf("secret store: mkdir: %w", err)
	}
	// 通过临时文件 + rename 原子落盘；避免半写。
	tmp, err := os.CreateTemp(s.BaseDir, "."+id+".*.tmp")
	if err != nil {
		return fmt.Errorf("secret store: mktemp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("secret store: write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secret store: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("secret store: rename: %w", err)
	}
	cleanup = false
	return nil
}

// Delete 删除 sidecar 文件；不存在视为成功。
func (s SecretStore) Delete(id string) error {
	path, err := s.pathFor(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secret store: remove: %w", err)
	}
	return nil
}

// isEmptyObject 判定 raw 表示 null / 空 / 空 object。
func isEmptyObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	if v == nil {
		return true
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Split / Merge
// -----------------------------------------------------------------------------

// Split 从完整 settings 中剥离 schema 中标为 secret 的字段：
//   - clean：完整结构（object shape 保留），但所有 secret 叶子已被移除
//   - secrets：仅包含 secret 字段的稀疏 object，形状与 settings 一致
//   - 如果 schema=nil 或没有 secret 字段，clean=settings 原值 / secrets=空
//
// 数组元素按下标位置递归处理（sidecar 里也是按下标存），保证 Merge 时能一一对上。
func Split(schema *Schema, raw json.RawMessage) (clean, secrets json.RawMessage, err error) {
	if schema == nil || schema.Root == nil || len(raw) == 0 {
		return raw, nil, nil
	}
	var val any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&val); err != nil {
		return nil, nil, fmt.Errorf("secret split: %w", err)
	}
	secretTree := splitNode(schema.Root, val)
	cleanBytes, err := json.Marshal(val)
	if err != nil {
		return nil, nil, err
	}
	if secretTree == nil || isEmptyTree(secretTree) {
		return cleanBytes, nil, nil
	}
	secretBytes, err := json.Marshal(secretTree)
	if err != nil {
		return nil, nil, err
	}
	return cleanBytes, secretBytes, nil
}

// splitNode 就地把 val 中命中 secret 的叶子移除，并返回被抽取出来的 secret 树。
// 只在 object / array 结构上递归；标量若为 secret 由父级 object 处理。
func splitNode(n *Node, val any) any {
	if n == nil || val == nil {
		return nil
	}
	switch n.Type {
	case "object":
		m, ok := val.(map[string]any)
		if !ok {
			return nil
		}
		var out map[string]any
		for name, child := range n.Properties {
			cur, present := m[name]
			if !present {
				continue
			}
			if child.Secret {
				if out == nil {
					out = map[string]any{}
				}
				out[name] = cur
				delete(m, name)
				continue
			}
			sub := splitNode(child, cur)
			if sub != nil {
				if out == nil {
					out = map[string]any{}
				}
				out[name] = sub
			}
		}
		if out == nil {
			return nil
		}
		return out
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return nil
		}
		outArr := make([]any, len(arr))
		hasAny := false
		for i, item := range arr {
			sub := splitNode(n.Items, item)
			if sub != nil {
				outArr[i] = sub
				hasAny = true
			}
		}
		if !hasAny {
			return nil
		}
		return outArr
	}
	return nil
}

// Merge 把 secrets 深合并回 base，返回完整 settings；不修改入参。
// 未在 schema 中标记 secret 的路径也会照样合入（宽松策略，避免 sidecar 与 schema 漂移时静默丢数据）。
func Merge(base, secrets json.RawMessage) (json.RawMessage, error) {
	if len(secrets) == 0 || string(secrets) == "null" {
		return base, nil
	}
	var b, s any
	if len(base) > 0 {
		dec := json.NewDecoder(bytes.NewReader(base))
		dec.UseNumber()
		if err := dec.Decode(&b); err != nil {
			return nil, fmt.Errorf("secret merge: base: %w", err)
		}
	} else {
		b = map[string]any{}
	}
	dec := json.NewDecoder(bytes.NewReader(secrets))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("secret merge: secrets: %w", err)
	}
	merged := deepMerge(b, s)
	return json.Marshal(merged)
}

// deepMerge：secrets 优先级高于 base，object/array 递归；数组按下标覆盖，
// 遇到 nil 元素时保留 base 元素。
func deepMerge(base, over any) any {
	if over == nil {
		return base
	}
	bm, bIsMap := base.(map[string]any)
	om, oIsMap := over.(map[string]any)
	if bIsMap && oIsMap {
		out := make(map[string]any, len(bm))
		for k, v := range bm {
			out[k] = v
		}
		for k, v := range om {
			if existing, ok := out[k]; ok {
				out[k] = deepMerge(existing, v)
			} else {
				out[k] = v
			}
		}
		return out
	}
	ba, bIsArr := base.([]any)
	oa, oIsArr := over.([]any)
	if bIsArr && oIsArr {
		length := len(ba)
		if len(oa) > length {
			length = len(oa)
		}
		out := make([]any, length)
		for i := 0; i < length; i++ {
			switch {
			case i < len(ba) && i < len(oa):
				if oa[i] == nil {
					out[i] = ba[i]
				} else {
					out[i] = deepMerge(ba[i], oa[i])
				}
			case i < len(ba):
				out[i] = ba[i]
			default:
				out[i] = oa[i]
			}
		}
		return out
	}
	return over
}

// isEmptyTree：递归判断 map/array 是否只剩下空节点，用于决定是否落 sidecar。
func isEmptyTree(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]any:
		for _, sub := range t {
			if !isEmptyTree(sub) {
				return false
			}
		}
		return true
	case []any:
		for _, sub := range t {
			if !isEmptyTree(sub) {
				return false
			}
		}
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// SecretLeafPaths 遍历 schema，返回所有 secret 叶子的原始点号路径（数组用 `[]`）。
// 已在 Schema.SecretPaths 里实现同名逻辑；这里额外提供一个排序稳定的字符串切片
// 便于调用方（如 audit redactor）快速判断"某个 key 是否是 secret"。
func SecretLeafPaths(s *Schema) []string {
	if s == nil {
		return nil
	}
	out := s.SecretPaths()
	sort.Strings(out)
	return out
}
