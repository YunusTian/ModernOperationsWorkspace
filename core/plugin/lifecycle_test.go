package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mow/mow/sdk/manifest"
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
	content := []byte("plugin-binary " + version)
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

func TestLifecycleUninstallPreservesState(t *testing.T) {
	source := testPluginPackage(t, "demo", "0.5.1")
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	lifecycle, err := NewLifecycle(pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Install(source); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.SetEnabled("demo", true); err != nil {
		t.Fatal(err)
	}

	if err := lifecycle.Uninstall("demo", false); err != nil {
		t.Fatalf("Uninstall() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("plugin directory still exists: %v", err)
	}
	if _, err := os.Stat(lifecycle.statePath("demo")); err != nil {
		t.Fatalf("state file should be preserved, got: %v", err)
	}
	// state 文件仍然存在，可再次安装（Install 会覆写 enabled=false，这里只
	// 断言 state 数据未在 uninstall 阶段被删除）。
	if _, err := lifecycle.Install(source); err != nil {
		t.Fatalf("reinstall error: %v", err)
	}
	if _, managed, err := lifecycle.IsEnabled("demo"); err != nil || !managed {
		t.Fatalf("state should still be managed after reinstall: managed=%v err=%v", managed, err)
	}
}

func TestLifecycleUninstallPurgeRemovesState(t *testing.T) {
	source := testPluginPackage(t, "demo", "0.5.1")
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	lifecycle, err := NewLifecycle(pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Install(source); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.SetEnabled("demo", true); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Uninstall("demo", true); err != nil {
		t.Fatalf("Uninstall(purge=true) error: %v", err)
	}
	if _, err := os.Stat(lifecycle.statePath("demo")); !os.IsNotExist(err) {
		t.Fatalf("state file should be purged, got: %v", err)
	}
}

func TestLifecycleUninstallRejectsMissing(t *testing.T) {
	lifecycle, err := NewLifecycle(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Uninstall("missing", false); err == nil {
		t.Fatal("expected error when uninstalling absent plugin")
	}
	if err := lifecycle.Uninstall("../escape", false); err == nil {
		t.Fatal("expected invalid id to fail")
	}
}

func TestLifecycleUpdateReplacesPackagePreservingState(t *testing.T) {
	sourceV1 := testPluginPackage(t, "demo", "0.5.1")
	sourceV2 := testPluginPackage(t, "demo", "0.5.2")
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	lifecycle, err := NewLifecycle(pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Install(sourceV1); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.SetEnabled("demo", true); err != nil {
		t.Fatal(err)
	}

	updated, err := lifecycle.Update(sourceV2)
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if updated.Version != "0.5.2" || !updated.Enabled {
		t.Fatalf("unexpected update result: %+v", updated)
	}
	mf, err := manifestLoadForTest(filepath.Join(pluginsDir, "demo"))
	if err != nil {
		t.Fatalf("re-load manifest: %v", err)
	}
	if mf != "0.5.2" {
		t.Fatalf("installed manifest version = %q, want 0.5.2", mf)
	}
	// 没有 backup 遗留。
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && (startsWith(e.Name(), ".update-") || startsWith(e.Name(), ".update-backup-")) {
			t.Fatalf("stale staging directory remains: %s", e.Name())
		}
	}
}

func TestLifecycleUpdateRejectsMissingInstallation(t *testing.T) {
	source := testPluginPackage(t, "demo", "0.5.2")
	lifecycle, err := NewLifecycle(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Update(source); err == nil {
		t.Fatal("expected update to fail when plugin is not installed")
	}
}

func TestLifecycleUpdateRejectsInvalidPackage(t *testing.T) {
	sourceV1 := testPluginPackage(t, "demo", "0.5.1")
	sourceV2 := testPluginPackage(t, "demo", "0.5.2")
	// 篡改 v2 内容让 checksum 失配。
	if err := os.WriteFile(filepath.Join(sourceV2, "bin", "plugin"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}

	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	lifecycle, err := NewLifecycle(pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Install(sourceV1); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Update(sourceV2); err == nil {
		t.Fatal("expected update to fail on invalid package")
	}
	// 现有安装应保持完好。
	if _, err := os.Stat(filepath.Join(pluginsDir, "demo", "plugin.json")); err != nil {
		t.Fatalf("original install missing after failed update: %v", err)
	}
	mf, err := manifestLoadForTest(filepath.Join(pluginsDir, "demo"))
	if err != nil || mf != "0.5.1" {
		t.Fatalf("expected original v0.5.1 to remain, got %q err=%v", mf, err)
	}
}

// manifestLoadForTest 读取安装目录下的 plugin.json 并返回 version 字段。
func manifestLoadForTest(dir string) (string, error) {
	mf, err := manifest.Load(dir)
	if err != nil {
		return "", err
	}
	return mf.Version, nil
}

func startsWith(s, prefix string) bool {
	return strings.HasPrefix(s, prefix)
}
