package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	hclog "github.com/hashicorp/go-hclog"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
	"github.com/mow/mow/sdk/pluginclient"
)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// fakeManifestPlugin 是一个不需要 gRPC 的 sdk.Plugin 实现，用于验证 MatchMetadata。
type fakeManifestPlugin struct{ meta sdk.Metadata }

func (p *fakeManifestPlugin) Metadata() sdk.Metadata                       { return p.meta }
func (p *fakeManifestPlugin) Init(context.Context, sdk.InitRequest) error  { return nil }
func (p *fakeManifestPlugin) Shutdown(context.Context) error               { return nil }
func (p *fakeManifestPlugin) HealthCheck(context.Context) sdk.HealthStatus { return sdk.StatusHealthy }
func (p *fakeManifestPlugin) Commands() []sdk.CommandHandler               { return nil }

// stubLoadBinary 把 loadBinary 替换成一个不启动子进程的桩，t.Cleanup 内还原。
// closedFlag 会在 LoadedPlugin.Close 被调用时置 true。
func stubLoadBinary(t *testing.T, meta sdk.Metadata) *bool {
	t.Helper()
	// Manager.Register 要求 Name 非空；测试里未指定时用 ID 顶上。
	if meta.Name == "" {
		meta.Name = meta.ID
	}
	closed := false
	orig := loadBinary
	loadBinary = func(path string, logger hclog.Logger) (*LoadedPlugin, error) {
		return pluginclient.NewLoadedPlugin(
			&fakeManifestPlugin{meta: meta},
			func() { closed = true },
		), nil
	}
	t.Cleanup(func() { loadBinary = orig })
	return &closed
}

// stubPlatform 把 GOOS/GOARCH 替换成 linux/amd64，与下面 buildPackage 保持一致，
// 使测试在真实 Windows 或 macOS 上仍能命中 Manifest 里的 linux/amd64 条目。
func stubPlatform(t *testing.T) {
	t.Helper()
	origOS, origArch := currentGOOS, currentGOARCH
	currentGOOS = func() string { return "linux" }
	currentGOARCH = func() string { return "amd64" }
	t.Cleanup(func() {
		currentGOOS, currentGOARCH = origOS, origArch
	})
}

// buildPackage 建一份最小可用的 plugin 包，包含 plugin.json + bin/entrypoint。
// core / sdk 兼容范围可参数化，方便测不同错误分支。
func buildPackage(t *testing.T, id, version, coreRange, sdkRange string) string {
	t.Helper()
	dir := t.TempDir()

	// dummy binary，只保证 os.Stat 能通过
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "entry"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	compat := `"core": "` + coreRange + `"`
	if sdkRange != "" {
		compat += `, "sdk": "` + sdkRange + `"`
	}
	m := `{
  "manifestVersion": 1,
  "id": "` + id + `",
  "name": "` + id + `",
  "version": "` + version + `",
  "compatibility": {` + compat + `},
  "platforms": [
    {"os": "linux", "arch": "amd64", "entrypoint": "bin/entry", "checksum": "sha256:2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"}
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, manifest.ManifestFileName), []byte(m), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	return dir
}

// -----------------------------------------------------------------------------
// tests
// -----------------------------------------------------------------------------

func TestLoadFromPackage_Happy(t *testing.T) {
	stubPlatform(t)
	closed := stubLoadBinary(t, sdk.Metadata{ID: "ssh", Version: "0.4.1"})

	dir := buildPackage(t, "ssh", "0.4.1", ">=0.4.0,<0.6.0", "")

	lp, m, err := LoadFromPackage(dir, &ManifestGate{
		CoreVersion:     "0.4.1",
		SDKVersion:      "0.4.1",
		ProtocolVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer lp.Close()

	if m.ID != "ssh" || m.Version != "0.4.1" {
		t.Errorf("returned manifest mismatch: %+v", m)
	}
	if *closed {
		t.Error("plugin should not be closed on happy path")
	}
}

func TestLoadInstalledPrefersPackage(t *testing.T) {
	stubPlatform(t)
	stubLoadBinary(t, sdk.Metadata{ID: "ai", Version: "0.5.0"})
	root := t.TempDir()
	pkg := buildPackage(t, "ai", "0.5.0", ">=0.4.0,<0.6.0", "")
	if err := os.Rename(pkg, filepath.Join(root, "ai")); err != nil {
		t.Fatal(err)
	}
	lp, mf, legacy, err := LoadInstalled(root, "ai", &ManifestGate{CoreVersion: "0.5.0", SDKVersion: "0.5.0", ProtocolVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer lp.Close()
	if legacy || mf == nil || mf.ID != "ai" {
		t.Fatalf("legacy=%v manifest=%+v", legacy, mf)
	}
}

func TestLoadInstalledLegacyFallback(t *testing.T) {
	stubLoadBinary(t, sdk.Metadata{ID: "ai", Version: "0.4.1"})
	root := t.TempDir()
	name := "ai"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	lp, mf, legacy, err := LoadInstalled(root, "ai", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lp.Close()
	if !legacy || mf != nil {
		t.Fatalf("legacy=%v manifest=%+v", legacy, mf)
	}
}

func TestLoadFromPackageChecksumBlocksSubprocess(t *testing.T) {
	stubPlatform(t)
	dir := buildPackage(t, "ai", "0.5.0", ">=0.4.0,<0.6.0", "")
	if err := os.WriteFile(filepath.Join(dir, "bin", "entry"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	orig := loadBinary
	loadBinary = func(string, hclog.Logger) (*LoadedPlugin, error) {
		called = true
		return nil, errors.New("must not start")
	}
	t.Cleanup(func() { loadBinary = orig })
	_, _, err := LoadFromPackage(dir, &ManifestGate{CoreVersion: "0.5.0", SDKVersion: "0.5.0", ProtocolVersion: "1.0.0"})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeChecksumMismatch {
		t.Fatalf("error=%v", err)
	}
	if called {
		t.Fatal("subprocess started before checksum validation")
	}
}

func TestLoadFromPackage_CheckCompatibilityBlocksSubprocess(t *testing.T) {
	stubPlatform(t)
	// 计数：loadBinary 被调用一次即失败
	origLoad := loadBinary
	called := false
	loadBinary = func(path string, logger hclog.Logger) (*LoadedPlugin, error) {
		called = true
		return pluginclient.NewLoadedPlugin(&fakeManifestPlugin{}, func() {}), nil
	}
	t.Cleanup(func() { loadBinary = origLoad })

	dir := buildPackage(t, "ssh", "0.4.1", ">=1.0.0", "")
	_, _, err := LoadFromPackage(dir, &ManifestGate{CoreVersion: "0.4.1"})
	if err == nil {
		t.Fatal("expected incompatibility error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeIncompatible {
		t.Fatalf("expected %s, got %v", manifest.ErrCodeIncompatible, err)
	}
	if got, _ := se.Details["layer"].(string); got != "core" {
		t.Errorf("expected layer=core, got %q", got)
	}
	if called {
		t.Error("loadBinary must NOT be called when compatibility check fails")
	}
}

func TestLoadFromPackage_ManifestMismatchClosesSubprocess(t *testing.T) {
	stubPlatform(t)
	closed := stubLoadBinary(t, sdk.Metadata{ID: "ssh", Version: "9.9.9"}) // 运行时报错版本

	dir := buildPackage(t, "ssh", "0.4.1", ">=0.4.0,<0.6.0", "")
	_, _, err := LoadFromPackage(dir, &ManifestGate{CoreVersion: "0.4.1"})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeManifestMismatch {
		t.Fatalf("expected %s, got %v", manifest.ErrCodeManifestMismatch, err)
	}
	if !*closed {
		t.Error("subprocess must be closed when metadata mismatch")
	}
}

func TestLoadFromPackage_ManifestIDMismatchClosesSubprocess(t *testing.T) {
	stubPlatform(t)
	closed := stubLoadBinary(t, sdk.Metadata{ID: "ftp", Version: "0.4.1"})

	dir := buildPackage(t, "ssh", "0.4.1", ">=0.4.0,<0.6.0", "")
	_, _, err := LoadFromPackage(dir, &ManifestGate{CoreVersion: "0.4.1"})
	if err == nil {
		t.Fatal("expected id mismatch error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeManifestMismatch {
		t.Fatalf("expected %s, got %v", manifest.ErrCodeManifestMismatch, err)
	}
	if got, _ := se.Details["field"].(string); got != "id" {
		t.Errorf("expected field=id, got %q", got)
	}
	if !*closed {
		t.Error("subprocess must be closed when id mismatches")
	}
}

func TestLoadFromPackage_EntrypointMissing(t *testing.T) {
	stubPlatform(t)
	stubLoadBinary(t, sdk.Metadata{ID: "ssh", Version: "0.4.1"})

	dir := buildPackage(t, "ssh", "0.4.1", ">=0.4.0,<0.6.0", "")
	// 删掉二进制
	if err := os.Remove(filepath.Join(dir, "bin", "entry")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	_, _, err := LoadFromPackage(dir, &ManifestGate{CoreVersion: "0.4.1"})
	if err == nil {
		t.Fatal("expected entrypoint missing error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeEntrypointMissing {
		t.Fatalf("expected %s, got %v", manifest.ErrCodeEntrypointMissing, err)
	}
}

func TestLoadFromPackage_NoPlatformEntry(t *testing.T) {
	origOS, origArch := currentGOOS, currentGOARCH
	currentGOOS = func() string { return "plan9" } // Manifest 里没有 plan9
	currentGOARCH = func() string { return "amd64" }
	t.Cleanup(func() { currentGOOS, currentGOARCH = origOS, origArch })
	stubLoadBinary(t, sdk.Metadata{ID: "ssh", Version: "0.4.1"})

	dir := buildPackage(t, "ssh", "0.4.1", ">=0.4.0,<0.6.0", "")
	_, _, err := LoadFromPackage(dir, &ManifestGate{CoreVersion: "0.4.1"})
	if err == nil {
		t.Fatal("expected missing platform entry error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeEntrypointMissing {
		t.Fatalf("expected %s, got %v", manifest.ErrCodeEntrypointMissing, err)
	}
	if got, _ := se.Details["os"].(string); got != "plan9" {
		t.Errorf("expected os=plan9 in details, got %q", got)
	}
}

func TestManagerRegisterFromPackage_Happy(t *testing.T) {
	stubPlatform(t)
	stubLoadBinary(t, sdk.Metadata{ID: "ssh", Version: "0.4.1"})

	dir := buildPackage(t, "ssh", "0.4.1", ">=0.4.0,<0.6.0", "")
	mgr := NewManager(Options{})
	lp, m, err := mgr.RegisterFromPackage(dir, &ManifestGate{CoreVersion: "0.4.1"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer lp.Close()

	if m == nil {
		t.Fatal("manifest should be returned")
	}
	entry, ok := mgr.Get("ssh")
	if !ok {
		t.Fatal("plugin not registered")
	}
	if entry.State != StateRegistered {
		t.Errorf("expected StateRegistered, got %s", entry.State)
	}
}

func TestManagerRegisterFromPackage_RegisterFailureClosesSubprocess(t *testing.T) {
	stubPlatform(t)
	closed := stubLoadBinary(t, sdk.Metadata{ID: "ssh", Version: "0.4.1"})

	dir := buildPackage(t, "ssh", "0.4.1", ">=0.4.0,<0.6.0", "")
	mgr := NewManager(Options{})

	// 先注册一个同 id 的 plugin，占位以触发 Register 失败
	if err := mgr.Register(&fakeManifestPlugin{meta: sdk.Metadata{ID: "ssh", Name: "ssh", Version: "0.4.1"}}); err != nil {
		t.Fatalf("prime register: %v", err)
	}

	_, _, err := mgr.RegisterFromPackage(dir, &ManifestGate{CoreVersion: "0.4.1"})
	if err == nil {
		t.Fatal("expected duplicate register error")
	}
	if !*closed {
		t.Error("subprocess must be closed when Manager.Register fails")
	}
}

// TestManifestGate_ResolveDefaults 覆盖 nil gate 与部分 zero-value 分支。
func TestManifestGate_ResolveDefaults(t *testing.T) {
	var g *ManifestGate
	r := g.resolve()
	if r.CoreVersion == "" || r.SDKVersion == "" || r.ProtocolVersion == "" {
		t.Errorf("nil gate should still yield defaults, got %+v", r)
	}
	// Windows/Linux/macOS 上应尊重传入值
	custom := (&ManifestGate{CoreVersion: "1.2.3"}).resolve()
	if custom.CoreVersion != "1.2.3" {
		t.Errorf("custom CoreVersion overridden: %q", custom.CoreVersion)
	}
}
