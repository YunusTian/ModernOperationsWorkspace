// plugin_config.go —— `mow plugin config <id>` 家族命令（v0.5.2 P1）。
//
// 语义：
//   - `mow plugin config <id>`                    → 列出 schema 字段（默认值 + 脱敏）
//   - `mow plugin config <id> get <path>`         → 输出某字段的当前值（脱敏）
//   - `mow plugin config <id> set <path> <value>` → 校验后写入
//   - `mow plugin config <id> unset <path>`       → 删除某字段
//   - `mow plugin config <id> schema [--json]`    → 展示 schema 元数据
//
// 落盘策略：`--config` 指定时写回同路径；未指定时写到 `~/.mow/config.json`。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mow/mow/core/config"
	coreplugin "github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/plugin/settings"
	"github.com/mow/mow/sdk/manifest"
)

func newPluginConfigCmd(holder *appHolder) *cobra.Command {
	var listJSON bool
	cmd := &cobra.Command{
		Use:   "config <id> [get|set|unset|schema] ...",
		Short: "Inspect or modify plugin settings (schema-driven)",
		Long: `Manage a plugin's user-facing settings, driven by its Manifest.settingsSchema.

Examples:
  mow plugin config ai                       # list all fields (secrets redacted)
  mow plugin config ai get providers         # print current value of a field
  mow plugin config ai set providers.0.kind mock
  mow plugin config ai unset providers.0.options.api_key
  mow plugin config ai schema --json         # emit compiled schema summary`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigList(cmd, holder, args[0], listJSON)
		},
	}
	cmd.Flags().BoolVar(&listJSON, "json", false, "emit machine-readable JSON")
	cmd.AddCommand(
		newPluginConfigGetCmd(holder),
		newPluginConfigSetCmd(holder),
		newPluginConfigUnsetCmd(holder),
		newPluginConfigSchemaCmd(holder),
	)
	return cmd
}

func newPluginConfigGetCmd(holder *appHolder) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id> <path>",
		Short: "Print current value of a settings field (secrets redacted)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigGet(cmd, holder, args[0], args[1])
		},
	}
}

func newPluginConfigSetCmd(holder *appHolder) *cobra.Command {
	return &cobra.Command{
		Use:   "set <id> <path> <value>",
		Short: "Set a settings field (value is coerced to the schema type)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSet(cmd, holder, args[0], args[1], args[2])
		},
	}
}

func newPluginConfigUnsetCmd(holder *appHolder) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <id> <path>",
		Short: "Remove a settings field",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSet(cmd, holder, args[0], args[1], "")
		},
	}
}

func newPluginConfigSchemaCmd(holder *appHolder) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "schema <id>",
		Short: "Show the compiled schema (fields, types, defaults, required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSchema(cmd, holder, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

// -----------------------------------------------------------------------------
// implementations
// -----------------------------------------------------------------------------

func runConfigList(cmd *cobra.Command, holder *appHolder, id string, jsonOut bool) error {
	pc, schema, err := loadPluginConfig(holder, id)
	if err != nil {
		return err
	}
	// 合并 sidecar 后再展示（脱敏），确保 UI 看到"完整 settings"。
	settingsRaw, err := mergeSidecar(holder, id, pc.Settings)
	if err != nil {
		return err
	}
	if schema != nil {
		merged, err := schema.ApplyDefaults(settingsRaw)
		if err != nil {
			return fmt.Errorf("apply defaults: %w", err)
		}
		settingsRaw = merged
	}
	redacted, err := redactSettings(schema, settingsRaw)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"id":       id,
			"enabled":  pc.Enabled,
			"settings": json.RawMessage(redacted),
		})
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Plugin:  %s\n", id)
	fmt.Fprintf(out, "Enabled: %v\n", pc.Enabled)
	if schema == nil {
		fmt.Fprintln(out, "\nNo settingsSchema declared. Current raw settings:")
		fmt.Fprintln(out, string(prettify(redacted)))
		return nil
	}
	fmt.Fprintln(out, "\nSchema fields:")
	for _, f := range schema.Fields() {
		fmt.Fprintf(out, "  %s%s", strings.Repeat("  ", f.Depth), f.Path)
		if f.Node.Type != "" {
			fmt.Fprintf(out, " (%s)", f.Node.Type)
		}
		if f.Node.Secret {
			fmt.Fprintf(out, " [secret]")
		}
		if _, req := findRequired(schema, f.Path); req {
			fmt.Fprintf(out, " [required]")
		}
		fmt.Fprintln(out)
		if f.Node.Description != "" {
			fmt.Fprintf(out, "      %s\n", f.Node.Description)
		}
	}
	fmt.Fprintln(out, "\nCurrent settings (secrets redacted):")
	fmt.Fprintln(out, string(prettify(redacted)))
	return nil
}

func runConfigGet(cmd *cobra.Command, holder *appHolder, id, path string) error {
	pc, schema, err := loadPluginConfig(holder, id)
	if err != nil {
		return err
	}
	src, err := mergeSidecar(holder, id, pc.Settings)
	if err != nil {
		return err
	}
	if schema != nil {
		src, _ = schema.ApplyDefaults(src)
	}
	redacted, err := redactSettings(schema, src)
	if err != nil {
		return err
	}
	val, ok, err := settings.GetPath(redacted, path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("path %q not found", path)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(val))
	return nil
}

func runConfigSet(cmd *cobra.Command, holder *appHolder, id, path, rawValue string) error {
	app, err := holder.Load()
	if err != nil {
		return err
	}
	schema, err := loadSchema(app.Cfg, id)
	if err != nil {
		return err
	}
	pc := app.Cfg.Plugins[id]
	// 合并 sidecar → 得到当前完整 settings；再对 path 做增删。
	full, err := mergeSidecar(holder, id, pc.Settings)
	if err != nil {
		return err
	}
	var next json.RawMessage
	if rawValue == "" {
		next, err = settings.SetPath(full, path, nil)
	} else {
		val := parseCLIValue(rawValue, schema, path)
		next, err = settings.SetPath(full, path, val)
	}
	if err != nil {
		return err
	}
	if schema != nil {
		merged, err := schema.ApplyDefaults(next)
		if err != nil {
			return err
		}
		if errs := schema.Validate(merged); len(errs) > 0 {
			return fmt.Errorf("validation failed:\n%s", formatErrors(errs))
		}
	}
	// 拆分 secret / 非 secret：secret → sidecar，非 secret → config.json。
	clean, secretPart, err := settings.Split(schema, next)
	if err != nil {
		return err
	}
	pc.Settings = clean
	if app.Cfg.Plugins == nil {
		app.Cfg.Plugins = map[string]config.PluginConfig{}
	}
	app.Cfg.Plugins[id] = pc
	if err := saveConfigFile(holder, app.Cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	store := settings.NewStoreFromDataDir(app.Cfg.App.DataDir)
	if err := store.Save(id, secretPart); err != nil {
		return fmt.Errorf("save secrets: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Updated %s.%s\n", id, path)
	return nil
}

func runConfigSchema(cmd *cobra.Command, holder *appHolder, id string, jsonOut bool) error {
	_, schema, err := loadPluginConfig(holder, id)
	if err != nil {
		return err
	}
	if schema == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "no settingsSchema declared")
		return nil
	}
	if jsonOut {
		type fieldOut struct {
			Path        string `json:"path"`
			Type        string `json:"type,omitempty"`
			Title       string `json:"title,omitempty"`
			Description string `json:"description,omitempty"`
			Secret      bool   `json:"secret,omitempty"`
			Required    bool   `json:"required,omitempty"`
			Default     any    `json:"default,omitempty"`
			Enum        []any  `json:"enum,omitempty"`
			Depth       int    `json:"depth"`
		}
		fields := schema.Fields()
		out := make([]fieldOut, 0, len(fields))
		for _, f := range fields {
			row := fieldOut{
				Path: f.Path, Type: f.Node.Type, Title: f.Node.Title, Description: f.Node.Description,
				Secret: f.Node.Secret, Enum: f.Node.Enum, Depth: f.Depth,
			}
			if _, req := findRequired(schema, f.Path); req {
				row.Required = true
			}
			if f.Node.Default != nil {
				var d any
				_ = json.Unmarshal(f.Node.Default, &d)
				row.Default = d
			}
			out = append(out, row)
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Schema for %s\n", id)
	for _, f := range schema.Fields() {
		fmt.Fprintf(w, "  %s%s", strings.Repeat("  ", f.Depth), f.Path)
		if f.Node.Type != "" {
			fmt.Fprintf(w, " (%s)", f.Node.Type)
		}
		if f.Node.Secret {
			fmt.Fprint(w, " [secret]")
		}
		if _, req := findRequired(schema, f.Path); req {
			fmt.Fprint(w, " [required]")
		}
		if f.Node.Default != nil {
			fmt.Fprintf(w, " default=%s", strings.TrimSpace(string(f.Node.Default)))
		}
		fmt.Fprintln(w)
		if f.Node.Description != "" {
			fmt.Fprintf(w, "      %s\n", f.Node.Description)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func loadPluginConfig(holder *appHolder, id string) (config.PluginConfig, *settings.Schema, error) {
	app, err := holder.Load()
	if err != nil {
		return config.PluginConfig{}, nil, err
	}
	pc := app.Cfg.Plugins[id]
	schema, err := loadSchema(app.Cfg, id)
	return pc, schema, err
}

// loadSchema 从已安装插件的 plugin.json 里读 settingsSchema 并编译。
// 插件未安装 → 返回 nil schema、nil error（允许在插件未安装时也能编辑 raw settings）。
func loadSchema(cfg config.Config, id string) (*settings.Schema, error) {
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

func redactSettings(schema *settings.Schema, raw json.RawMessage) (json.RawMessage, error) {
	if schema == nil {
		return raw, nil
	}
	return schema.Redact(raw)
}

// mergeSidecar 读取 <DataDir>/plugin-secrets/<id>.json 并合并到 base。
// 无 sidecar 时直接返回 base；错误直接返回，避免静默丢失 secret。
func mergeSidecar(holder *appHolder, id string, base json.RawMessage) (json.RawMessage, error) {
	app, err := holder.Load()
	if err != nil {
		return nil, err
	}
	if app.Cfg.App.DataDir == "" {
		return base, nil
	}
	store := settings.NewStoreFromDataDir(app.Cfg.App.DataDir)
	sec, ok, err := store.Load(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return base, nil
	}
	return settings.Merge(base, sec)
}

func prettify(raw json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return out
}

func findRequired(s *settings.Schema, path string) (*settings.Node, bool) {
	if s == nil || s.Root == nil {
		return nil, false
	}
	parts := strings.Split(path, ".")
	cur := s.Root
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

func parseCLIValue(raw string, s *settings.Schema, path string) any {
	if s != nil {
		if node := nodeAt(s, path); node != nil {
			switch node.Type {
			case "integer":
				if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
					return n
				}
			case "number":
				if f, err := strconv.ParseFloat(raw, 64); err == nil {
					return f
				}
			case "boolean":
				if b, err := strconv.ParseBool(raw); err == nil {
					return b
				}
			case "object", "array":
				return json.RawMessage(raw)
			}
			if node.Type == "string" {
				return raw
			}
		}
	}
	trim := strings.TrimSpace(raw)
	if trim == "true" || trim == "false" || trim == "null" ||
		(len(trim) > 0 && (trim[0] == '{' || trim[0] == '[' || trim[0] == '"')) {
		return json.RawMessage(raw)
	}
	if _, err := strconv.ParseFloat(trim, 64); err == nil {
		return json.RawMessage(raw)
	}
	return raw
}

func nodeAt(s *settings.Schema, path string) *settings.Node {
	if s == nil {
		return nil
	}
	parts := strings.Split(path, ".")
	cur := s.Root
	for _, p := range parts {
		if cur == nil || cur.Type != "object" {
			return nil
		}
		cur = cur.Properties[p]
	}
	return cur
}

func formatErrors(errs []settings.Error) string {
	lines := make([]string, len(errs))
	for i, e := range errs {
		lines[i] = "  - " + e.Error()
	}
	return strings.Join(lines, "\n")
}

func saveConfigFile(holder *appHolder, cfg config.Config) error {
	path := ""
	if holder.configPath != nil {
		path = *holder.configPath
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".mow", "config.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return config.Save(path, cfg)
}
