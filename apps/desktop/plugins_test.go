package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/logger"
)

// newAppForPluginTest 构造一个只装配 cfg + logger 的最小 App，方便 plugins.go 中
// 走 Lifecycle 的 Wails 方法测试。
func newAppForPluginTest(t *testing.T, pluginsDir string) *App {
	t.Helper()
	return &App{
		log:     logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON}),
		cfg:     config.Config{App: config.AppConfig{PluginsDir: pluginsDir}},
		enabled: map[string]bool{},
	}
}

func makeTestPluginPackage(t *testing.T, dir, id, version, coreConstraint string) string {
	t.Helper()
	pkg := filepath.Join(dir, id)
	if err := os.MkdirAll(filepath.Join(pkg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("plugin-binary-" + id + "-" + version)
	if err := os.WriteFile(filepath.Join(pkg, "bin", "plugin"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifest := fmt.Sprintf(`{
  "manifestVersion": 1,
  "id": %q,
  "name": "Demo Plugin",
  "version": %q,
  "author": "acme",
  "description": "A demo plugin used for tests.",
  "compatibility": {"core": %q},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/plugin", "checksum": "sha256:%s"}]
}`, id, version, coreConstraint, runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(pkg, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestListPluginsReportsHealthyEntry(t *testing.T) {
	pluginsDir := t.TempDir()
	makeTestPluginPackage(t, pluginsDir, "demo", "0.5.1", ">=0.5.0,<0.6.0")
	a := newAppForPluginTest(t, pluginsDir)

	items, err := a.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	got := items[0]
	if got.ID != "demo" || got.Version != "0.5.1" || got.Name != "Demo Plugin" {
		t.Fatalf("unexpected fields: %+v", got)
	}
	if got.Health != "ok" || got.HealthError != "" {
		t.Fatalf("expected healthy, got %+v", got)
	}
	if got.Enabled {
		t.Fatal("newly installed plugin should default to disabled")
	}
	wantPlatform := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	if got.Platform != wantPlatform {
		t.Fatalf("platform = %q, want %q", got.Platform, wantPlatform)
	}
	if got.CompatibilityCore != ">=0.5.0,<0.6.0" {
		t.Fatalf("compat = %q", got.CompatibilityCore)
	}
}

func TestListPluginsFlagsIncompatible(t *testing.T) {
	pluginsDir := t.TempDir()
	// 声明一个高得离谱的 core 约束，让当前 sdk/version.Version 不满足。
	makeTestPluginPackage(t, pluginsDir, "demo", "0.5.1", ">=99.0.0")
	a := newAppForPluginTest(t, pluginsDir)

	items, err := a.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	got := items[0]
	if got.Health != "incompatible" {
		t.Fatalf("expected incompatible, got: %+v", got)
	}
	if got.HealthCode == "" {
		t.Fatalf("expected stable health_code, got: %+v", got)
	}
}

func TestListPluginsFlagsBrokenPackage(t *testing.T) {
	pluginsDir := t.TempDir()
	pkg := makeTestPluginPackage(t, pluginsDir, "demo", "0.5.1", ">=0.5.0,<0.6.0")
	// 篡改 entrypoint，让 checksum 不匹配 → Doctor 会报 broken。
	if err := os.WriteFile(filepath.Join(pkg, "bin", "plugin"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := newAppForPluginTest(t, pluginsDir)

	items, err := a.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Health != "broken" || items[0].HealthError == "" {
		t.Fatalf("expected broken, got: %+v", items[0])
	}
}

func TestSetPluginEnabledAndUninstall(t *testing.T) {
	pluginsDir := t.TempDir()
	makeTestPluginPackage(t, pluginsDir, "demo", "0.5.1", ">=0.5.0,<0.6.0")
	a := newAppForPluginTest(t, pluginsDir)

	if _, err := a.SetPluginEnabled("demo", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	items, err := a.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	if !items[0].Enabled {
		t.Fatal("expected enabled=true after SetPluginEnabled")
	}

	// Uninstall without purge → 保留 state
	if err := a.UninstallPlugin("demo", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("package dir should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, ".state", "demo.json")); err != nil {
		t.Fatalf("state should be preserved: %v", err)
	}
}
