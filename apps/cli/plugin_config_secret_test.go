package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/plugin/settings"
	"github.com/mow/mow/sdk"
)

// TestPluginConfigSetSecretIsolation：CLI 侧 set 一个 secret 字段后，
// config.json 里不留明文；sidecar 文件持有明文；plugin.get 能读到脱敏值。
func TestPluginConfigSetSecretIsolation(t *testing.T) {
	schema := `{
	  "type":"object",
	  "properties":{
	    "port":{"type":"integer"},
	    "api_key":{"type":"string","secret":true}
	  }
	}`
	cfgPath := installFakePluginConfig(t, "demo", schema)
	// set secret
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "set", "demo", "api_key", "sk-cli"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// set non-secret
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "set", "demo", "port", "2222"); err != nil {
		t.Fatalf("set port: %v", err)
	}
	// config.json 不应包含 sk-cli；应包含 port
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-cli") {
		t.Fatalf("secret leaked into config.json: %s", raw)
	}
	if !strings.Contains(string(raw), `"port": 2222`) {
		t.Fatalf("port change not persisted: %s", raw)
	}
	// sidecar 应存在明文
	cfg := loadCfgFromFile(t, cfgPath)
	sidecar := filepath.Join(cfg.App.DataDir, "plugin-secrets", "demo.json")
	sidecarRaw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
	if !strings.Contains(string(sidecarRaw), "sk-cli") {
		t.Fatalf("sidecar should hold secret plainly: %s", sidecarRaw)
	}
	// get 应输出脱敏值
	out, _, err := runCLI(t, cfgPath, "plugin", "config", "get", "demo", "api_key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.TrimSpace(out) != `"***"` {
		t.Fatalf("get should return redacted: %q", out)
	}
	// unset secret → sidecar 移除
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "unset", "demo", "api_key"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar should be removed after last secret unset: %v", err)
	}
}

// TestPluginConfigListDoesNotLeakSecrets：list / list-json 输出中不应出现原始 secret。
func TestPluginConfigListDoesNotLeakSecrets(t *testing.T) {
	schema := `{"type":"object","properties":{"api_key":{"type":"string","secret":true}}}`
	cfgPath := installFakePluginConfig(t, "demo", schema)
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "set", "demo", "api_key", "supersecret"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, cfgPath, "plugin", "config", "demo")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if strings.Contains(out, "supersecret") {
		t.Fatalf("secret leaked in list: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("expected redaction marker: %s", out)
	}
	out, _, err = runCLI(t, cfgPath, "plugin", "config", "demo", "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}
	if strings.Contains(out, "supersecret") {
		t.Fatalf("secret leaked in json: %s", out)
	}
}

// TestPluginInitRequestMergesSidecar：verify pluginInitRequest 会把 sidecar
// 合并回 sdk.InitRequest.Settings，插件端收到完整数据。
func TestPluginInitRequestMergesSidecar(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.App.DataDir = filepath.Join(root, "data")
	cfg.App.PluginsDir = filepath.Join(root, "plugins")
	if err := os.MkdirAll(cfg.App.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// sidecar 写一份 secret
	store := settings.NewStoreFromDataDir(cfg.App.DataDir)
	if err := store.Save("demo", json.RawMessage(`{"api_key":"sk-merged"}`)); err != nil {
		t.Fatal(err)
	}
	pc := config.PluginConfig{Enabled: true, Settings: json.RawMessage(`{"port":2222}`)}
	req := pluginInitRequest("demo", pc, cfg)
	if !strings.Contains(string(req.Settings), `sk-merged`) {
		t.Fatalf("merged settings should include sidecar secret: %s", req.Settings)
	}
	if !strings.Contains(string(req.Settings), `"port":2222`) {
		t.Fatalf("merged settings should include base data: %s", req.Settings)
	}
	// DataDir 应指向 plugin-data 子目录
	if !strings.HasSuffix(req.DataDir, "plugin-data") {
		t.Fatalf("unexpected data dir: %s", req.DataDir)
	}
	_ = sdk.InitRequest{} // keep import
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func loadCfgFromFile(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
