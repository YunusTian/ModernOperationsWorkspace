package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/logger"
)

// installSchemaPlugin 在 <PluginsDir>/<id>/ 下写入 plugin.json + bin，schema 由参数注入。
// 返回 App（含 cfg），可直接调用 Wails 方法。
func installSchemaPlugin(t *testing.T, id, schemaJSON string) *App {
	t.Helper()
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(pluginsDir, id, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("bin-" + id)
	binName := "plugin"
	if runtime.GOOS == "windows" {
		binName = "plugin.exe"
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, id, "bin", binName), content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	mfJSON := fmt.Sprintf(`{
  "manifestVersion": 1,
  "id": %q,
  "name": "Demo",
  "version": "0.5.2",
  "compatibility": {"core": ">=0.5.0,<99.0.0"},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/%s", "checksum": "sha256:%s"}],
  "settingsSchema": %s
}`, id, runtime.GOOS, runtime.GOARCH, binName, hex.EncodeToString(sum[:]), schemaJSON)
	if err := os.WriteFile(filepath.Join(pluginsDir, id, "plugin.json"), []byte(mfJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(pluginsDir, ".state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, id+".json"),
		[]byte(fmt.Sprintf(`{"id":%q,"enabled":true,"version":"0.5.2","installed":"2026-07-13T00:00:00Z"}`, id)),
		0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.App.DataDir = dataDir
	cfg.App.PluginsDir = pluginsDir
	cfg.Plugins = map[string]config.PluginConfig{
		id: {Enabled: true, Settings: json.RawMessage(`{}`)},
	}
	return &App{
		log:     logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON}),
		cfg:     cfg,
		enabled: map[string]bool{},
	}
}

func TestGetPluginSchemaHappyPath(t *testing.T) {
	schema := `{
	  "type":"object",
	  "properties":{
	    "port":{"type":"integer","default":22,"description":"port"},
	    "api_key":{"type":"string","secret":true}
	  }
	}`
	a := installSchemaPlugin(t, "demo", schema)
	vm, err := a.GetPluginSchema("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !vm.HasSchema {
		t.Fatalf("has_schema should be true")
	}
	// 字段列表按字典序：api_key, port
	if len(vm.Fields) != 2 || vm.Fields[0].Path != "api_key" || vm.Fields[1].Path != "port" {
		t.Fatalf("unexpected fields: %+v", vm.Fields)
	}
	if !vm.Fields[0].Secret {
		t.Fatal("api_key should be marked secret")
	}
	if v, ok := vm.Fields[1].Default.(float64); !ok || v != 22 {
		t.Fatalf("port default: %+v", vm.Fields[1].Default)
	}
	// settings 应已注入 default port=22
	var s map[string]any
	if err := json.Unmarshal(vm.Settings, &s); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%v", s["port"]) != "22" {
		t.Fatalf("default not applied: %+v", s)
	}
}

func TestSetPluginSettingsValidatesAndRedacts(t *testing.T) {
	schema := `{
	  "type":"object",
	  "additionalProperties":false,
	  "properties":{
	    "port":{"type":"integer","minimum":1,"maximum":65535},
	    "api_key":{"type":"string","secret":true}
	  }
	}`
	a := installSchemaPlugin(t, "demo", schema)

	// 越限拒绝
	if _, err := a.SetPluginSettings("demo", json.RawMessage(`{"port":99999}`)); err == nil {
		t.Fatal("expected validation error for port")
	}

	// 正常写入：secret 落 sidecar，config.json 只留非 secret
	vm, err := a.SetPluginSettings("demo", json.RawMessage(`{"port":2222,"api_key":"sk-live"}`))
	if err != nil {
		t.Fatal(err)
	}
	// VM 返回的应是脱敏后的 view
	if !strings.Contains(string(vm.Settings), `"api_key":"***"`) {
		t.Fatalf("api_key should be redacted in VM: %s", vm.Settings)
	}
	// config.json 不应包含 secret 明文
	raw, err := os.ReadFile(filepath.Join(a.cfg.App.DataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `sk-live`) {
		t.Fatalf("secret leaked into config.json: %s", string(raw))
	}
	if strings.Contains(string(raw), `api_key`) {
		t.Fatalf("secret key name should not appear in config.json: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"port": 2222`) {
		t.Fatalf("non-secret should be persisted: %s", string(raw))
	}
	// sidecar 应存明文
	sidecar := filepath.Join(a.cfg.App.DataDir, "plugin-secrets", "demo.json")
	sidecarRaw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	if !strings.Contains(string(sidecarRaw), `sk-live`) {
		t.Fatalf("sidecar should hold secret plainly: %s", sidecarRaw)
	}
}

func TestSetPluginSettingsPreservesSecretsWhenPatchIsRedacted(t *testing.T) {
	schema := `{
	  "type":"object",
	  "properties":{
	    "port":{"type":"integer"},
	    "api_key":{"type":"string","secret":true}
	  }
	}`
	a := installSchemaPlugin(t, "demo", schema)
	// 先写一遍 secret
	if _, err := a.SetPluginSettings("demo", json.RawMessage(`{"api_key":"sk-original","port":1}`)); err != nil {
		t.Fatal(err)
	}
	// 用户只改 port，patch 中 api_key 是 "***"（相当于 UI 把脱敏值写回）
	vm, err := a.SetPluginSettings("demo", json.RawMessage(`{"api_key":"***","port":9}`))
	if err != nil {
		t.Fatal(err)
	}
	// config.json 只保留 port
	raw, _ := os.ReadFile(filepath.Join(a.cfg.App.DataDir, "config.json"))
	if strings.Contains(string(raw), `sk-original`) {
		t.Fatalf("secret leaked into config.json: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"port": 9`) {
		t.Fatalf("port change should be persisted: %s", string(raw))
	}
	// sidecar 里 secret 应保留原值
	sidecar := filepath.Join(a.cfg.App.DataDir, "plugin-secrets", "demo.json")
	sidecarRaw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
	if !strings.Contains(string(sidecarRaw), `sk-original`) {
		t.Fatalf("sidecar should retain original secret: %s", sidecarRaw)
	}
	// VM 仍脱敏
	if !strings.Contains(string(vm.Settings), `"api_key":"***"`) {
		t.Fatalf("api_key should remain redacted in VM: %s", vm.Settings)
	}
}

func TestGetPluginSchemaMissingPluginReturnsBlankVM(t *testing.T) {
	a := installSchemaPlugin(t, "demo", `{"type":"object"}`)
	vm, err := a.GetPluginSchema("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if vm.HasSchema {
		t.Fatalf("should not have schema for missing plugin")
	}
}
