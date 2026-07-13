package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mow/mow/core/config"
)

const catalogSample = `{
  "catalogVersion": 1,
  "source": "official",
  "entries": [
    {
      "id": "ssh",
      "name": "SSH",
      "description": "SSH plugin",
      "tags": ["network", "ops"],
      "versions": [
        {
          "version": "0.5.1",
          "compatibility": {"core": ">=0.5.0,<0.6.0"},
          "platforms": [
            {"os": "linux",   "arch": "amd64", "url": "https://example/ssh.tgz", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
            {"os": "windows", "arch": "amd64", "url": "https://example/ssh.zip", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
            {"os": "darwin",  "arch": "arm64", "url": "https://example/ssh.tar", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
            {"os": "darwin",  "arch": "amd64", "url": "https://example/ssh.tar", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
          ]
        }
      ]
    }
  ]
}`

func writeConfigWithCatalog(t *testing.T, sourceURL string) (cfgPath, cacheDir string) {
	t.Helper()
	root := t.TempDir()
	cacheDir = filepath.Join(root, "catalog-cache")
	cfg := config.Default()
	cfg.App.DataDir = filepath.Join(root, "data")
	cfg.App.PluginsDir = filepath.Join(root, "plugins")
	cfg.App.Catalog.CacheDir = cacheDir
	cfg.App.Catalog.Sources = []config.CatalogSource{
		{Name: "official", URL: sourceURL},
	}
	cfgPath = filepath.Join(root, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	return cfgPath, cacheDir
}

func runCLI(t *testing.T, cfgPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--config", cfgPath}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestPluginCatalogRefreshAndSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalogSample))
	}))
	defer srv.Close()

	cfgPath, cacheDir := writeConfigWithCatalog(t, srv.URL)

	// list
	if out, _, err := runCLI(t, cfgPath, "plugin", "catalog", "list"); err != nil || !strings.Contains(out, "official") {
		t.Fatalf("catalog list: %v\n%s", err, out)
	}

	// refresh
	if out, _, err := runCLI(t, cfgPath, "plugin", "catalog", "refresh", "--json"); err != nil {
		t.Fatalf("refresh: %v\n%s", err, out)
	} else {
		var rows []refreshResultJSON
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("json: %v\n%s", err, out)
		}
		if len(rows) != 1 || !rows[0].OK || rows[0].NumEntries != 1 {
			t.Fatalf("unexpected refresh result: %+v", rows)
		}
	}
	// 缓存文件应已生成
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("cache dir empty: %v %+v", err, entries)
	}

	// search 默认按当前 GOOS/GOARCH。sample 包含 windows/amd64/linux/amd64/darwin/*
	if out, _, err := runCLI(t, cfgPath, "plugin", "search", "--json", "--all"); err != nil {
		t.Fatalf("search --all: %v\n%s", err, out)
	} else {
		var rows []searchResultJSON
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("json: %v\n%s", err, out)
		}
		if len(rows) != 1 || len(rows[0].Entries) != 1 || rows[0].Entries[0].ID != "ssh" {
			t.Fatalf("expected ssh entry: %+v", rows)
		}
	}

	// 精确匹配当前平台（sample 覆盖 linux/windows/darwin amd64/arm64）
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if out, _, err := runCLI(t, cfgPath, "plugin", "search", "network", "--json"); err != nil {
			t.Fatalf("search network: %v\n%s", err, out)
		} else {
			var rows []searchResultJSON
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatalf("json: %v\n%s", err, out)
			}
			if len(rows) != 1 || len(rows[0].Entries) != 1 || rows[0].Entries[0].ID != "ssh" {
				t.Fatalf("expected ssh in results: %+v\n%s", rows, out)
			}
		}
	}
}

func TestPluginCatalogRefreshFailsWhenServerDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalogSample))
	}))
	url := srv.URL
	srv.Close()

	cfgPath, _ := writeConfigWithCatalog(t, url)
	if _, _, err := runCLI(t, cfgPath, "plugin", "catalog", "refresh"); err == nil {
		t.Fatal("expected error when catalog unreachable")
	}
}

func TestPluginSearchFallsBackToCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalogSample))
	}))
	cfgPath, _ := writeConfigWithCatalog(t, srv.URL)

	// prime cache
	if _, _, err := runCLI(t, cfgPath, "plugin", "catalog", "refresh"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	srv.Close()

	out, _, err := runCLI(t, cfgPath, "plugin", "search", "--json", "--all")
	if err != nil {
		t.Fatalf("search after offline: %v\n%s", err, out)
	}
	var rows []searchResultJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(rows) != 1 || !rows[0].FromCache {
		t.Fatalf("expected cache fallback: %+v", rows)
	}
}
