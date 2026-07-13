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
)

// installFakePlugin 在 <PluginsDir>/<id>/ 下写入 plugin.json + bin，用于 config 测试。
// settingsSchema 由参数注入；返回 CLI --config path。
func installFakePluginConfig(t *testing.T, id, settingsSchema string) string {
	t.Helper()
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsDir, id, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("fake-" + id)
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
  "name": "Fake",
  "version": "0.5.2",
  "compatibility": {"core": ">=0.5.0,<99.0.0"},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/%s", "checksum": "sha256:%s"}],
  "settingsSchema": %s
}`, id, runtime.GOOS, runtime.GOARCH, binName, hex.EncodeToString(sum[:]), settingsSchema)
	if err := os.WriteFile(filepath.Join(pluginsDir, id, "plugin.json"), []byte(mfJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// state：使其被 Lifecycle.List 认出
	stateDir := filepath.Join(pluginsDir, ".state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, id+".json"),
		[]byte(fmt.Sprintf(`{"id":%q,"enabled":true,"version":"0.5.2","installed":"2026-07-13T00:00:00Z"}`, id)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.App.DataDir = filepath.Join(root, "data")
	cfg.App.PluginsDir = pluginsDir
	cfg.Plugins = map[string]config.PluginConfig{
		id: {Enabled: true, Settings: json.RawMessage(`{}`)},
	}
	cfgPath := filepath.Join(root, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestPluginConfigList(t *testing.T) {
	schema := `{
	  "type":"object",
	  "additionalProperties": false,
	  "properties": {
	    "port": {"type":"integer","default":22,"description":"listen port"},
	    "api_key": {"type":"string","secret":true,"description":"api key"}
	  }
	}`
	cfgPath := installFakePluginConfig(t, "fake", schema)
	// 先写入 api_key
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "set", "fake", "api_key", "sk-xxx"); err != nil {
		t.Fatalf("set api_key: %v", err)
	}
	// list：port 应默认到 22；api_key 应脱敏为 ***
	out, _, err := runCLI(t, cfgPath, "plugin", "config", "fake")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "port (integer)") || !strings.Contains(out, "api_key (string) [secret]") {
		t.Fatalf("schema hint missing: %s", out)
	}
	if strings.Contains(out, "sk-xxx") {
		t.Fatalf("secret leaked in list output: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("expected redaction marker: %s", out)
	}
	if !strings.Contains(out, "\"port\": 22") {
		t.Fatalf("default not applied: %s", out)
	}
}

func TestPluginConfigSetAndGet(t *testing.T) {
	schema := `{
	  "type":"object",
	  "additionalProperties": false,
	  "properties": {
	    "port": {"type":"integer","minimum":1,"maximum":65535}
	  }
	}`
	cfgPath := installFakePluginConfig(t, "fake", schema)
	// happy set
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "set", "fake", "port", "2222"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// get 输出应是 2222
	out, _, err := runCLI(t, cfgPath, "plugin", "config", "get", "fake", "port")
	if err != nil || strings.TrimSpace(out) != "2222" {
		t.Fatalf("get: err=%v out=%q", err, out)
	}
	// 超限应报错，磁盘配置不变
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "set", "fake", "port", "99999"); err == nil {
		t.Fatalf("expected validation to reject 99999")
	}
	// unset
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "unset", "fake", "port"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if _, _, err := runCLI(t, cfgPath, "plugin", "config", "get", "fake", "port"); err == nil {
		t.Fatalf("expected get to fail after unset")
	}
}

func TestPluginConfigSchemaJSON(t *testing.T) {
	schema := `{
	  "type":"object",
	  "required":["kind"],
	  "properties":{
	    "kind":{"type":"string","enum":["a","b"]},
	    "opts":{"type":"object","properties":{"token":{"type":"string","secret":true}}}
	  }
	}`
	cfgPath := installFakePluginConfig(t, "fake", schema)
	out, _, err := runCLI(t, cfgPath, "plugin", "config", "schema", "fake", "--json")
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(rows) < 3 {
		t.Fatalf("expected >=3 fields, got %d: %+v", len(rows), rows)
	}
	// kind 必填
	var kindRow map[string]any
	for _, r := range rows {
		if r["path"] == "kind" {
			kindRow = r
			break
		}
	}
	if kindRow == nil || kindRow["required"] != true {
		t.Fatalf("kind should be required: %+v", kindRow)
	}
	// opts.token 应标记 secret
	var tok map[string]any
	for _, r := range rows {
		if r["path"] == "opts.token" {
			tok = r
			break
		}
	}
	if tok == nil || tok["secret"] != true {
		t.Fatalf("opts.token should be secret: %+v", tok)
	}
}
