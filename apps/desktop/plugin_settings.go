// plugin_settings.go —— 桌面插件配置 Wails 绑定（v0.5.2 P1）。
//
// 提供三个 Wails 方法：
//   - GetPluginSchema(id)              → 返回 compiled schema 字段列表 + 当前脱敏值
//   - GetPluginSettings(id)            → 返回当前脱敏 settings（自动应用 defaults）
//   - SetPluginSettings(id, patch)     → 校验并落盘；patch 是完整的 settings JSON
//
// 与 CLI 共享 core/plugin/settings，语义完全一致。
package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/mow/mow/core/config"
	coreplugin "github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/plugin/settings"
	"github.com/mow/mow/sdk/manifest"
)

// PluginSettingsField 是暴露给前端的单个字段视图。
type PluginSettingsField struct {
	Path        string `json:"path"`
	Type        string `json:"type,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Default     any    `json:"default,omitempty"`
	Enum        []any  `json:"enum,omitempty"`
	Format      string `json:"format,omitempty"`
	Depth       int    `json:"depth"`
	// Number/String 约束（若声明）
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	MinLength *int     `json:"min_length,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
}

// PluginSettingsVM 是 GetPluginSchema / GetPluginSettings 的返回。
type PluginSettingsVM struct {
	ID       string                 `json:"id"`
	HasSchema bool                  `json:"has_schema"`
	Enabled  bool                   `json:"enabled"`
	Fields   []PluginSettingsField  `json:"fields,omitempty"`
	Settings json.RawMessage        `json:"settings"`
	// SecretPaths：全部 secret 字段路径（前端可用于渲染 password 输入 & 提示）。
	SecretPaths []string `json:"secret_paths,omitempty"`
}

// GetPluginSettings 返回脱敏后的当前 settings（自动 apply defaults）。
func (a *App) GetPluginSettings(id string) (PluginSettingsVM, error) {
	return a.buildPluginSettingsVM(id, true)
}

// GetPluginSchema 返回 compiled schema 字段列表 + 当前脱敏值，供 UI 一次性渲染。
func (a *App) GetPluginSchema(id string) (PluginSettingsVM, error) {
	return a.buildPluginSettingsVM(id, true)
}

// SetPluginSettings 校验并写回完整的 settings JSON。
// patch 必须是 object；secret 字段值为 "***" 时会保留原值（避免 UI 把脱敏值写回）。
func (a *App) SetPluginSettings(id string, patch json.RawMessage) (PluginSettingsVM, error) {
	if id == "" {
		return PluginSettingsVM{}, fmt.Errorf("id is required")
	}
	schema, err := loadDesktopSchema(a.cfg, id)
	if err != nil {
		return PluginSettingsVM{}, err
	}
	pc := a.cfg.Plugins[id]
	// 把上一版的 secret 值合并回来：patch 里遇到 "***" 视为"保持不变"。
	merged := patch
	if schema != nil {
		merged, err = mergeSecrets(schema, pc.Settings, patch)
		if err != nil {
			return PluginSettingsVM{}, err
		}
	}
	if schema != nil {
		withDefaults, err := schema.ApplyDefaults(merged)
		if err != nil {
			return PluginSettingsVM{}, err
		}
		if errs := schema.Validate(withDefaults); len(errs) > 0 {
			return PluginSettingsVM{}, fmt.Errorf("validation failed: %s", errs[0].Error())
		}
	}
	pc.Settings = merged
	if a.cfg.Plugins == nil {
		a.cfg.Plugins = map[string]config.PluginConfig{}
	}
	a.cfg.Plugins[id] = pc
	if err := saveDesktopConfig(a.cfg); err != nil {
		return PluginSettingsVM{}, fmt.Errorf("save config: %w", err)
	}
	return a.buildPluginSettingsVM(id, true)
}

// saveDesktopConfig 把当前 cfg 写回默认 config 路径 `<DataDir>/config.json`。
// 桌面客户端目前不通过 --config 参数指定路径，因此固定使用默认位置；
// 用户可在配置目录自行编辑同一份文件。
func saveDesktopConfig(cfg config.Config) error {
	if cfg.App.DataDir == "" {
		return fmt.Errorf("app.data_dir is empty")
	}
	path := filepath.Join(cfg.App.DataDir, "config.json")
	return config.Save(path, cfg)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func (a *App) buildPluginSettingsVM(id string, redact bool) (PluginSettingsVM, error) {
	pc := a.cfg.Plugins[id]
	schema, err := loadDesktopSchema(a.cfg, id)
	if err != nil {
		return PluginSettingsVM{}, err
	}
	raw := pc.Settings
	if schema != nil {
		merged, err := schema.ApplyDefaults(raw)
		if err != nil {
			return PluginSettingsVM{}, err
		}
		raw = merged
	}
	if redact && schema != nil {
		red, err := schema.Redact(raw)
		if err != nil {
			return PluginSettingsVM{}, err
		}
		raw = red
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	vm := PluginSettingsVM{
		ID:        id,
		HasSchema: schema != nil,
		Enabled:   pc.Enabled,
		Settings:  raw,
	}
	if schema != nil {
		vm.SecretPaths = schema.SecretPaths()
		for _, f := range schema.Fields() {
			field := PluginSettingsField{
				Path: f.Path, Type: f.Node.Type, Title: f.Node.Title,
				Description: f.Node.Description, Secret: f.Node.Secret,
				Enum: f.Node.Enum, Format: f.Node.Format, Depth: f.Depth,
				Minimum: f.Node.Minimum, Maximum: f.Node.Maximum,
				MinLength: f.Node.MinLength, MaxLength: f.Node.MaxLength,
			}
			if f.Node.Pattern != nil {
				field.Pattern = f.Node.Pattern.String()
			}
			if _, req := isRequired(schema, f.Path); req {
				field.Required = true
			}
			if f.Node.Default != nil {
				var d any
				_ = json.Unmarshal(f.Node.Default, &d)
				field.Default = d
			}
			vm.Fields = append(vm.Fields, field)
		}
	}
	return vm, nil
}

// loadDesktopSchema 与 CLI 侧的 loadSchema 语义一致；单独放在此包避免跨 main 引用。
func loadDesktopSchema(cfg config.Config, id string) (*settings.Schema, error) {
	lifecycle, err := coreplugin.NewLifecycle(cfg.App.PluginsDir)
	if err != nil {
		return nil, err
	}
	items, err := lifecycle.List()
	if err != nil {
		return nil, err
	}
	found := false
	for _, it := range items {
		if it.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, nil
	}
	pkgDir := filepath.Join(cfg.App.PluginsDir, id)
	mf, err := manifest.Load(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("read plugin.json: %w", err)
	}
	return settings.Compile(mf.SettingsSchema)
}

func isRequired(s *settings.Schema, path string) (*settings.Node, bool) {
	if s == nil || s.Root == nil {
		return nil, false
	}
	cur := s.Root
	parts := splitPath(path)
	for i, p := range parts {
		if cur == nil || cur.Type != "object" {
			return nil, false
		}
		if i == len(parts)-1 {
			_, req := cur.Required[p]
			return cur.Properties[p], req
		}
		cur = cur.Properties[p]
	}
	return nil, false
}

// splitPath 用点号切分；后续如需支持数组下标可以扩展这里。
func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	// 简易 split；避免引入 strings 依赖噪音
	out := []string{}
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			out = append(out, path[start:i])
			start = i + 1
		}
	}
	out = append(out, path[start:])
	return out
}

// mergeSecrets 把 patch 中被脱敏（值为 "***"）的 secret 字段还原为 previous 对应值，
// 避免 UI 编辑非 secret 字段后把 "***" 存回磁盘。
func mergeSecrets(s *settings.Schema, previous, patch json.RawMessage) (json.RawMessage, error) {
	if s == nil || s.Root == nil {
		return patch, nil
	}
	var prev any
	if len(previous) > 0 {
		if err := json.Unmarshal(previous, &prev); err != nil {
			return nil, err
		}
	}
	var next any
	if err := json.Unmarshal(patch, &next); err != nil {
		return nil, err
	}
	mergeSecretsNode(s.Root, prev, next)
	return json.Marshal(next)
}

func mergeSecretsNode(n *settings.Node, prev, next any) {
	if n == nil || next == nil {
		return
	}
	switch n.Type {
	case "object":
		prevMap, _ := prev.(map[string]any)
		nextMap, ok := next.(map[string]any)
		if !ok {
			return
		}
		for name, child := range n.Properties {
			if child.Secret {
				if v, ok := nextMap[name]; ok {
					if s, isStr := v.(string); isStr && s == "***" {
						if prevMap != nil {
							if pv, ok := prevMap[name]; ok {
								nextMap[name] = pv
							} else {
								delete(nextMap, name)
							}
						} else {
							delete(nextMap, name)
						}
					}
				}
			} else {
				var pv any
				if prevMap != nil {
					pv = prevMap[name]
				}
				if nv, ok := nextMap[name]; ok {
					mergeSecretsNode(child, pv, nv)
				}
			}
		}
	case "array":
		prevArr, _ := prev.([]any)
		nextArr, ok := next.([]any)
		if !ok {
			return
		}
		for i, item := range nextArr {
			var pv any
			if i < len(prevArr) {
				pv = prevArr[i]
			}
			mergeSecretsNode(n.Items, pv, item)
		}
	}
}
