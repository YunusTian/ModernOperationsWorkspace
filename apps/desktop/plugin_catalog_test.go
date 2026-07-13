package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/logger"
)

func newAppForCatalog(t *testing.T, root string, sourceURL string) *App {
	t.Helper()
	cfg := config.Config{
		App: config.AppConfig{
			DataDir:    filepath.Join(root, "data"),
			PluginsDir: filepath.Join(root, "plugins"),
			Catalog: config.CatalogConfig{
				CacheDir: filepath.Join(root, "cache"),
				Sources:  []config.CatalogSource{{Name: "official", URL: sourceURL}},
			},
		},
	}
	return &App{
		log:     logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON}),
		cfg:     cfg,
		enabled: map[string]bool{},
	}
}

func packDesktopArchive(t *testing.T, id, version string) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("bin-" + id + "-" + version)
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	mfJSON := fmt.Sprintf(`{
  "manifestVersion": 1,
  "id": %q,
  "name": "Demo",
  "version": %q,
  "compatibility": {"core": ">=0.5.0,<99.0.0"},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/plugin", "checksum": "sha256:%s"}]
}`, id, version, runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(mfJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644}
		if info.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hdr.Typeflag = tar.TypeReg
		hdr.Size = int64(len(data))
		if strings.Contains(rel, "bin") {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sha256HexBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// TestDesktopCatalogFlow 覆盖桌面端 List/Refresh/Search/Install 的完整链路。
func TestDesktopCatalogFlow(t *testing.T) {
	// artifact server
	archive := packDesktopArchive(t, "sample", "0.5.1")
	sum := sha256HexBytes(archive)
	artSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer artSrv.Close()

	catalogJSON := fmt.Sprintf(`{
  "catalogVersion": 1,
  "source": "official",
  "entries": [{
    "id": "sample",
    "name": "Sample",
    "versions": [{
      "version": "0.5.1",
      "compatibility": {"core": ">=0.5.0,<99.0.0"},
      "platforms": [{"os": %q, "arch": %q, "url": %q, "checksum": "sha256:%s"}]
    }]
  }]
}`, runtime.GOOS, runtime.GOARCH, artSrv.URL+"/pkg.tar.gz", sum)
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalogJSON))
	}))
	defer catSrv.Close()

	root := t.TempDir()
	a := newAppForCatalog(t, root, catSrv.URL)

	// list sources
	sources, err := a.ListCatalogSources()
	if err != nil || len(sources) != 1 || sources[0].Name != "official" {
		t.Fatalf("ListCatalogSources: %+v %v", sources, err)
	}

	// refresh
	rows, err := a.RefreshCatalog(true)
	if err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}
	if len(rows) != 1 || !rows[0].OK || rows[0].NumEntries != 1 {
		t.Fatalf("unexpected refresh: %+v", rows)
	}

	// search
	results, err := a.SearchCatalog("")
	if err != nil {
		t.Fatalf("SearchCatalog: %v", err)
	}
	if len(results) != 1 || len(results[0].Entries) != 1 || results[0].Entries[0].ID != "sample" {
		t.Fatalf("unexpected search: %+v", results)
	}

	// install from catalog
	vm, err := a.InstallPluginFromCatalog(CatalogInstallInput{ID: "sample"})
	if err != nil {
		t.Fatalf("InstallPluginFromCatalog: %v", err)
	}
	if vm.ID != "sample" || vm.Version != "0.5.1" || vm.Health != "ok" {
		t.Fatalf("unexpected install VM: %+v", vm)
	}

	// 二次装同一插件应报错（已装），走 update 才对
	if _, err := a.InstallPluginFromCatalog(CatalogInstallInput{ID: "sample"}); err == nil {
		t.Fatal("expected duplicate install to fail")
	}
}

// TestDesktopCatalogNoSources 保证未配置源时给出明确错误。
func TestDesktopCatalogNoSources(t *testing.T) {
	root := t.TempDir()
	a := &App{
		log: logger.Init(logger.Options{Level: "error", Format: logger.FormatJSON}),
		cfg: config.Config{App: config.AppConfig{
			DataDir:    filepath.Join(root, "data"),
			PluginsDir: filepath.Join(root, "plugins"),
			Catalog:    config.CatalogConfig{CacheDir: filepath.Join(root, "cache")},
		}},
		enabled: map[string]bool{},
	}
	if _, err := a.RefreshCatalog(true); err == nil {
		t.Fatal("expected error when no sources configured")
	}
	if _, err := a.InstallPluginFromCatalog(CatalogInstallInput{ID: "sample"}); err == nil {
		t.Fatal("expected install error when no sources")
	}
}
