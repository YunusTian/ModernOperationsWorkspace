package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLifecycleInstallEnableDisableAndDoctor(t *testing.T) {
	source := testPluginPackage(t, "demo", "0.5.1")
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	lifecycle, err := NewLifecycle(pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.now = func() time.Time { return time.Unix(123, 0) }

	installed, err := lifecycle.Install(source)
	if err != nil {
		t.Fatalf("Install() error: %v", err)
	}
	if installed.ID != "demo" || installed.Version != "0.5.1" || installed.Enabled {
		t.Fatalf("unexpected installation: %+v", installed)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "demo", "plugin.json")); err != nil {
		t.Fatalf("installed package missing: %v", err)
	}

	item, err := lifecycle.SetEnabled("demo", true)
	if err != nil || !item.Enabled {
		t.Fatalf("SetEnabled(true) = %+v, %v", item, err)
	}
	enabled, managed, err := lifecycle.IsEnabled("demo")
	if err != nil || !managed || !enabled {
		t.Fatalf("IsEnabled() = %v, %v, %v", enabled, managed, err)
	}

	items, err := lifecycle.List()
	if err != nil || len(items) != 1 || !items[0].Enabled {
		t.Fatalf("List() = %+v, %v", items, err)
	}
	diagnostics, err := lifecycle.Doctor()
	if err != nil || len(diagnostics) != 1 || !diagnostics[0].OK {
		t.Fatalf("Doctor() = %+v, %v", diagnostics, err)
	}

	item, err = lifecycle.SetEnabled("demo", false)
	if err != nil || item.Enabled {
		t.Fatalf("SetEnabled(false) = %+v, %v", item, err)
	}
}

func TestLifecycleInstallRejectsExistingDestination(t *testing.T) {
	source := testPluginPackage(t, "demo", "0.5.1")
	lifecycle, err := NewLifecycle(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Install(source); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Install(source); err == nil {
		t.Fatal("expected duplicate install to fail")
	}
}

func TestLifecycleRejectsInvalidID(t *testing.T) {
	lifecycle, err := NewLifecycle(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.SetEnabled("../escape", true); err == nil {
		t.Fatal("expected invalid id to fail")
	}
	if _, _, err := lifecycle.IsEnabled("../escape"); err == nil {
		t.Fatal("expected invalid id lookup to fail")
	}
}

func TestLifecycleDoctorReportsTamperedPackage(t *testing.T) {
	source := testPluginPackage(t, "demo", "0.5.1")
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	lifecycle, err := NewLifecycle(pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Install(source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "demo", "bin", "plugin"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := lifecycle.Doctor()
	if err != nil || len(diagnostics) != 1 || diagnostics[0].OK || diagnostics[0].Error == "" {
		t.Fatalf("Doctor() = %+v, %v", diagnostics, err)
	}
}

func testPluginPackage(t *testing.T, id, version string) string {
	t.Helper()
	dir := t.TempDir()
	content := []byte("plugin-binary")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifestJSON := fmt.Sprintf(`{
  "manifestVersion": 1,
  "id": %q,
  "name": "Demo",
  "version": %q,
  "compatibility": {"core": ">=0.5.0,<0.6.0"},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/plugin", "checksum": "sha256:%s"}]
}`, id, version, runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
