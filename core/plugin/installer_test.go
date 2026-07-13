package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mow/mow/core/plugin/catalog"
)

// serveArtifact 启一个 http 服务返回 tar.gz。返回 url + sha256.
func serveArtifact(t *testing.T, archive []byte) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	return srv, sha256Sum(archive)
}

// buildCatalogWith 构造带一个 entry 的 Catalog Client。
func buildCatalogWith(t *testing.T, entries []catalog.Entry) *catalog.Client {
	t.Helper()
	// 通过 file:// 一份静态 JSON 作为 catalog 源
	dir := t.TempDir()
	cat := catalog.Catalog{SchemaVersion: 1, Source: "official", Entries: entries}
	// 序列化：为了避免暴露内部 JSON，使用 catalog.Parse 的反过程可能太麻烦；
	// 这里直接手写 JSON。
	data, err := marshalCatalog(cat)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	u := "file://"
	if runtime.GOOS == "windows" {
		u += "/" + filepath.ToSlash(path)
	} else {
		u += filepath.ToSlash(path)
	}
	client, err := catalog.NewClient(catalog.Options{
		Sources:  []catalog.Source{{Name: "official", URL: u}},
		CacheDir: filepath.Join(t.TempDir(), "cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestInstallerInstallAndUpdateFromCatalog(t *testing.T) {
	// v1 包
	pkgV1 := buildSamplePackageWithVersion(t, "sample", "0.5.0")
	archV1 := tarGzOfDir(t, pkgV1)
	srvV1, sumV1 := serveArtifact(t, archV1)
	defer srvV1.Close()

	// v2 包（相同 id，不同 version）
	pkgV2 := buildSamplePackageWithVersion(t, "sample", "0.5.1")
	archV2 := tarGzOfDir(t, pkgV2)
	srvV2, sumV2 := serveArtifact(t, archV2)
	defer srvV2.Close()

	entries := []catalog.Entry{
		{
			ID:   "sample",
			Name: "Sample",
			Versions: []catalog.Release{
				{
					Version:       "0.5.0",
					Compatibility: catalog.Compatibility{Core: ">=0.5.0,<0.6.0"},
					Platforms: []catalog.Artifact{
						{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: srvV1.URL + "/v1.tar.gz", Checksum: "sha256:" + sumV1},
					},
				},
				{
					Version:       "0.5.1",
					Compatibility: catalog.Compatibility{Core: ">=0.5.0,<0.6.0"},
					Platforms: []catalog.Artifact{
						{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: srvV2.URL + "/v2.tar.gz", Checksum: "sha256:" + sumV2},
					},
				},
			},
		},
	}
	client := buildCatalogWith(t, entries)

	lifecycle, err := NewLifecycle(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := NewInstaller(InstallerOptions{
		Lifecycle: lifecycle,
		Catalog:   client,
		Filter:    catalog.Filter{OS: runtime.GOOS, Arch: runtime.GOARCH, CoreVersion: "0.5.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 未指定版本 → 装最高兼容版本
	item, err := inst.Install(ctx, "sample")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if item.Version != "0.5.1" {
		t.Fatalf("expected latest=0.5.1, got %q", item.Version)
	}

	// Update 到 0.5.0（版本回滚也走 update；由 Lifecycle 决定）
	updated, err := inst.Update(ctx, "sample@0.5.0")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Version != "0.5.0" {
		t.Fatalf("expected version 0.5.0 after update, got %q", updated.Version)
	}
}

func TestInstallerRejectsIncompatibleFilter(t *testing.T) {
	pkg := buildSamplePackageWithVersion(t, "sample", "0.5.1")
	archive := tarGzOfDir(t, pkg)
	srv, sum := serveArtifact(t, archive)
	defer srv.Close()

	entries := []catalog.Entry{{
		ID: "sample",
		Versions: []catalog.Release{{
			Version:       "0.5.1",
			Compatibility: catalog.Compatibility{Core: ">=99.0.0"},
			Platforms: []catalog.Artifact{
				{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: srv.URL + "/v.tar.gz", Checksum: "sha256:" + sum},
			},
		}},
	}}
	client := buildCatalogWith(t, entries)
	lifecycle, _ := NewLifecycle(filepath.Join(t.TempDir(), "plugins"))
	inst, _ := NewInstaller(InstallerOptions{
		Lifecycle: lifecycle,
		Catalog:   client,
		Filter:    catalog.Filter{OS: runtime.GOOS, Arch: runtime.GOARCH, CoreVersion: "0.5.0"},
	})
	if _, err := inst.Install(context.Background(), "sample"); err == nil {
		t.Fatal("expected install to fail due to core compat")
	}
}

func TestInstallerRejectsMissingArtifact(t *testing.T) {
	entries := []catalog.Entry{{
		ID: "sample",
		Versions: []catalog.Release{{
			Version:       "0.5.1",
			Compatibility: catalog.Compatibility{Core: ">=0.5.0,<0.6.0"},
			Platforms: []catalog.Artifact{
				{OS: "netbsd", Arch: "riscv64", URL: "https://example/pkg.tar.gz", Checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		}},
	}}
	client := buildCatalogWith(t, entries)
	lifecycle, _ := NewLifecycle(filepath.Join(t.TempDir(), "plugins"))
	inst, _ := NewInstaller(InstallerOptions{
		Lifecycle: lifecycle,
		Catalog:   client,
		Filter:    catalog.Filter{OS: runtime.GOOS, Arch: runtime.GOARCH, CoreVersion: "0.5.0"},
	})
	if _, err := inst.Install(context.Background(), "sample"); err == nil {
		t.Fatal("expected error for missing artifact on current platform")
	}
}

func TestLooksLikeCatalogRef(t *testing.T) {
	positive := []string{"ssh", "docker@0.5.1", "ai-provider@1.0.0-rc1"}
	negative := []string{"", "./pkg", ".", "../a", "/abs/path", `C:\Users\p`, "https://x/y", "file:///x"}
	for _, s := range positive {
		if !LooksLikeCatalogRef(s) {
			t.Errorf("%q should be catalog ref", s)
		}
	}
	for _, s := range negative {
		if LooksLikeCatalogRef(s) {
			t.Errorf("%q should NOT be catalog ref", s)
		}
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func buildSamplePackageWithVersion(t *testing.T, id, version string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("plugin-binary-" + id + "-" + version)
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifest := fmt.Sprintf(`{
  "manifestVersion": 1,
  "id": %q,
  "name": "Sample",
  "version": %q,
  "compatibility": {"core": ">=0.5.0,<0.6.0"},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/plugin", "checksum": "sha256:%s"}]
}`, id, version, runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func marshalCatalog(c catalog.Catalog) ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}
