package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sampleJSON = `{
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
          "version": "0.5.0",
          "compatibility": {"core": ">=0.5.0,<0.6.0"},
          "platforms": [
            {"os": "linux",   "arch": "amd64", "url": "https://example/ssh-0.5.0-linux-amd64.tar.gz",   "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
            {"os": "windows", "arch": "amd64", "url": "https://example/ssh-0.5.0-windows-amd64.tar.gz", "checksum": "sha256:1111111111111111111111111111111111111111111111111111111111111111"}
          ]
        },
        {
          "version": "0.5.1",
          "compatibility": {"core": ">=0.5.1,<0.6.0"},
          "platforms": [
            {"os": "linux",   "arch": "amd64", "url": "https://example/ssh-0.5.1-linux-amd64.tar.gz",   "checksum": "sha256:2222222222222222222222222222222222222222222222222222222222222222"}
          ]
        }
      ]
    },
    {
      "id": "docker",
      "name": "Docker",
      "versions": [
        {
          "version": "0.5.0",
          "compatibility": {"core": ">=0.4.0"},
          "platforms": [
            {"os": "linux", "arch": "amd64", "url": "https://example/docker.tar.gz", "checksum": "sha256:3333333333333333333333333333333333333333333333333333333333333333"}
          ]
        }
      ]
    }
  ]
}`

func TestParseHappyPathSortsAndValidates(t *testing.T) {
	c, err := Parse([]byte(sampleJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Entries) != 2 {
		t.Fatalf("entries=%d", len(c.Entries))
	}
	// Sort by ID ascending.
	if c.Entries[0].ID != "docker" || c.Entries[1].ID != "ssh" {
		t.Fatalf("entry order: %+v", c.Entries)
	}
	// Versions sort descending.
	if c.Entries[1].Versions[0].Version != "0.5.1" {
		t.Fatalf("version order: %+v", c.Entries[1].Versions)
	}
}

func TestParseRejectsUnknownFieldAndDup(t *testing.T) {
	if _, err := Parse([]byte(`{"catalogVersion":1,"unexpected":true,"entries":[]}`)); err == nil {
		t.Fatal("expected unknown field error")
	}
	dup := `{"catalogVersion":1,"entries":[
	  {"id":"x","versions":[{"version":"1.0.0","platforms":[{"os":"linux","arch":"amd64","url":"u","checksum":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}]}]},
	  {"id":"x","versions":[{"version":"1.0.0","platforms":[{"os":"linux","arch":"amd64","url":"u","checksum":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}]}]}
	]}`
	if _, err := Parse([]byte(dup)); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestSearchFiltersByPlatformAndCompat(t *testing.T) {
	c, err := Parse([]byte(sampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	// Windows/amd64, core 0.5.0 → ssh 0.5.0 only (ssh 0.5.1 has no windows artifact
	// and requires core >=0.5.1).
	res := c.Search(Filter{OS: "windows", Arch: "amd64", CoreVersion: "0.5.0"})
	if len(res) != 1 || res[0].ID != "ssh" || len(res[0].Versions) != 1 || res[0].Versions[0].Version != "0.5.0" {
		t.Fatalf("windows/0.5.0 filter: %+v", res)
	}

	// Linux/amd64, core 0.5.1 → ssh v0.5.1 & v0.5.0 (compat allows both), docker 0.5.0
	res = c.Search(Filter{OS: "linux", Arch: "amd64", CoreVersion: "0.5.1"})
	if len(res) != 2 {
		t.Fatalf("linux/0.5.1: %+v", res)
	}
	// Query filter
	res = c.Search(Filter{Query: "network"})
	if len(res) != 1 || res[0].ID != "ssh" {
		t.Fatalf("query filter: %+v", res)
	}
}

func TestLatestFor(t *testing.T) {
	c, err := Parse([]byte(sampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := c.LatestFor("ssh", Filter{OS: "linux", Arch: "amd64", CoreVersion: "0.5.1"})
	if !ok || r.Version != "0.5.1" {
		t.Fatalf("latest ssh: %+v ok=%v", r, ok)
	}
	// Windows on 0.5.1 → 0.5.0 (only windows artifact)
	r, ok = c.LatestFor("ssh", Filter{OS: "windows", Arch: "amd64", CoreVersion: "0.5.1"})
	if !ok || r.Version != "0.5.0" {
		t.Fatalf("latest ssh windows: %+v ok=%v", r, ok)
	}
	// Missing id
	if _, ok := c.LatestFor("nope", Filter{}); ok {
		t.Fatal("expected miss")
	}
}

func TestClientFetchHTTPWritesCacheAndFallsBackWhenOffline(t *testing.T) {
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleJSON))
	}))
	defer srv.Close()

	cacheDir := filepath.Join(t.TempDir(), "cache")
	client, err := NewClient(Options{
		Sources:  []Source{{Name: "official", URL: srv.URL}},
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 1) 首次拉取：写入缓存
	res := client.Fetch(ctx, client.Sources()[0], false)
	if res.Err != nil || res.Catalog == nil {
		t.Fatalf("first fetch: %+v", res)
	}
	if res.FromCache {
		t.Fatal("first fetch should not come from cache")
	}
	// 2) 关闭 server 后 force=true → 错误；force=false → 命中缓存
	srv.Close()
	res = client.Fetch(ctx, client.Sources()[0], true)
	if res.Err == nil {
		t.Fatal("expected force fetch to fail after server closed")
	}
	res = client.Fetch(ctx, client.Sources()[0], false)
	if res.Err != nil || res.Catalog == nil || !res.FromCache {
		t.Fatalf("expected cache fallback, got: %+v", res)
	}
	if res.Catalog.Source != "official" {
		t.Fatalf("cached catalog source lost: %q", res.Catalog.Source)
	}
	// 缓存文件存在
	if _, err := os.Stat(client.CachePath(client.Sources()[0])); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
}

func TestClientRejectsBadRemoteAndKeepsCache(t *testing.T) {
	// server 返回坏 JSON
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"nope":`))
	}))
	defer srv.Close()

	cacheDir := filepath.Join(t.TempDir(), "cache")
	client, err := NewClient(Options{
		Sources:  []Source{{Name: "official", URL: srv.URL}},
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 手工种一份合法缓存
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client.CachePath(client.Sources()[0]), []byte(sampleJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	res := client.Fetch(context.Background(), client.Sources()[0], false)
	if res.Err != nil || !res.FromCache {
		t.Fatalf("expected cache fallback, got: %+v", res)
	}
}

func TestClientFileScheme(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, []byte(sampleJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if runtime.GOOS == "windows" {
		// file:///C:/... 形式
		u.Path = "/" + filepath.ToSlash(path)
	}
	client, err := NewClient(Options{
		Sources: []Source{{Name: "local", URL: u.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := client.Fetch(context.Background(), client.Sources()[0], true)
	if res.Err != nil || res.Catalog == nil || len(res.Catalog.Entries) != 2 {
		t.Fatalf("file scheme fetch: %+v", res)
	}
}

func TestNewClientRejectsInvalidSources(t *testing.T) {
	cases := []Options{
		{Sources: []Source{{Name: "", URL: "http://x"}}},
		{Sources: []Source{{Name: "a", URL: ""}}},
		{Sources: []Source{{Name: "a", URL: "ftp://x"}}},
		{Sources: []Source{{Name: "a", URL: "http://x"}, {Name: "a", URL: "http://y"}}},
	}
	for _, opt := range cases {
		if _, err := NewClient(opt); err == nil {
			t.Fatalf("expected error for %+v", opt)
		}
	}
}

// Sanity check: ensure sample constant does not accidentally omit sha prefix.
func TestSampleShaFormat(t *testing.T) {
	if !strings.Contains(sampleJSON, "sha256:") {
		t.Fatal("sample missing sha256 prefix")
	}
}
