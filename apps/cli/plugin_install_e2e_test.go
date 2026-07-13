package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mow/mow/core/config"
	"github.com/mow/mow/sdk/manifest"
	sdkversion "github.com/mow/mow/sdk/version"
)

// TestPluginInstallFromCatalogE2E 覆盖从 catalog 拉取、下载、原子安装 → 升级 → 卸载
// 的完整链路，只依赖 httptest（不需要真的启动子进程或 gRPC）。
func TestPluginInstallFromCatalogE2E(t *testing.T) {
	// -------------------------------------------------------------------------
	// 1) 打两个 tar.gz 包（同 id，不同版本）
	// -------------------------------------------------------------------------
	v1Archive := packSamplePackage(t, "sample", "0.5.0")
	v2Archive := packSamplePackage(t, "sample", "0.5.1")
	v1Sum := sha256Hex(v1Archive)
	v2Sum := sha256Hex(v2Archive)

	var (
		artServer *httptest.Server
	)
	// artifacts server：/v1、/v2
	artServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.tar.gz":
			_, _ = w.Write(v1Archive)
		case "/v2.tar.gz":
			_, _ = w.Write(v2Archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer artServer.Close()

	// -------------------------------------------------------------------------
	// 2) 拼一个 catalog.json 并通过 http server 提供
	// -------------------------------------------------------------------------
	catalogJSON := fmt.Sprintf(`{
  "catalogVersion": 1,
  "source": "official",
  "entries": [
    {
      "id": "sample",
      "name": "Sample",
      "versions": [
        {
          "version": "0.5.0",
          "compatibility": {"core": ">=0.5.0,<0.6.0"},
          "platforms": [
            {"os": %q, "arch": %q, "url": %q, "checksum": "sha256:%s"}
          ]
        },
        {
          "version": "0.5.1",
          "compatibility": {"core": ">=0.5.0,<0.6.0"},
          "platforms": [
            {"os": %q, "arch": %q, "url": %q, "checksum": "sha256:%s"}
          ]
        }
      ]
    }
  ]
}`,
		runtime.GOOS, runtime.GOARCH, artServer.URL+"/v1.tar.gz", v1Sum,
		runtime.GOOS, runtime.GOARCH, artServer.URL+"/v2.tar.gz", v2Sum,
	)
	catServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalogJSON))
	}))
	defer catServer.Close()

	// -------------------------------------------------------------------------
	// 3) 配置：指向上面的 catalog server
	// -------------------------------------------------------------------------
	root := t.TempDir()
	cfg := config.Default()
	cfg.App.DataDir = filepath.Join(root, "data")
	cfg.App.PluginsDir = filepath.Join(root, "plugins")
	cfg.App.Catalog.CacheDir = filepath.Join(root, "catalog-cache")
	cfg.App.Catalog.Sources = []config.CatalogSource{{Name: "official", URL: catServer.URL}}
	cfgPath := filepath.Join(root, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// -------------------------------------------------------------------------
	// 4) 依次跑 install / list / update / uninstall
	// -------------------------------------------------------------------------
	// 未指定版本 → 应装最高 (0.5.1)
	if out, _, err := runMOW(t, cfgPath, "plugin", "install", "sample"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	} else if !strings.Contains(out, "Installed sample@0.5.1") || !strings.Contains(out, "from catalog") {
		t.Fatalf("unexpected install output: %s", out)
	}
	// list 里应能看到 sample@0.5.1
	if out, _, err := runMOW(t, cfgPath, "plugin", "list"); err != nil || !strings.Contains(out, "sample\t0.5.1") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	// 显式指定 0.5.0 走 update → 回滚版本
	if out, _, err := runMOW(t, cfgPath, "plugin", "update", "sample@0.5.0"); err != nil {
		t.Fatalf("update to 0.5.0: %v\n%s", err, out)
	} else if !strings.Contains(out, "Updated sample@0.5.0") {
		t.Fatalf("unexpected update output: %s", out)
	}
	// 校验磁盘上的 manifest 版本
	mf, err := manifest.Load(filepath.Join(cfg.App.PluginsDir, "sample"))
	if err != nil || mf.Version != "0.5.0" {
		t.Fatalf("on-disk manifest = %+v err=%v", mf, err)
	}
	// uninstall --purge
	if out, _, err := runMOW(t, cfgPath, "plugin", "uninstall", "sample", "--purge"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cfg.App.PluginsDir, "sample")); !os.IsNotExist(err) {
		t.Fatalf("plugin dir should be gone: %v", err)
	}
}

// TestPluginInstallRejectsChecksumMismatch 保证 catalog 声明的 checksum 与
// 实际字节不匹配时 install 会失败，磁盘上不留半成品。
func TestPluginInstallRejectsChecksumMismatch(t *testing.T) {
	archive := packSamplePackage(t, "sample", "0.5.1")
	bogus := "sha256:" + strings.Repeat("0", 64) // 与实际字节不匹配

	artServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer artServer.Close()

	catalogJSON := fmt.Sprintf(`{
  "catalogVersion": 1,
  "entries": [{
    "id": "sample",
    "versions": [{
      "version": "0.5.1",
      "compatibility": {"core": ">=0.5.0,<0.6.0"},
      "platforms": [{"os": %q, "arch": %q, "url": %q, "checksum": %q}]
    }]
  }]
}`, runtime.GOOS, runtime.GOARCH, artServer.URL+"/pkg.tar.gz", bogus)
	catServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalogJSON))
	}))
	defer catServer.Close()

	root := t.TempDir()
	cfg := config.Default()
	cfg.App.DataDir = filepath.Join(root, "data")
	cfg.App.PluginsDir = filepath.Join(root, "plugins")
	cfg.App.Catalog.CacheDir = filepath.Join(root, "catalog-cache")
	cfg.App.Catalog.Sources = []config.CatalogSource{{Name: "official", URL: catServer.URL}}
	cfgPath := filepath.Join(root, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runMOW(t, cfgPath, "plugin", "install", "sample"); err == nil {
		t.Fatal("expected checksum mismatch to fail install")
	}
	if _, err := os.Stat(filepath.Join(cfg.App.PluginsDir, "sample")); !os.IsNotExist(err) {
		t.Fatalf("plugin dir should not exist after failed install: %v", err)
	}
}

// TestPluginInstallLocalPathStillWorks 保证 install <path> 语义未被 catalog 改动破坏。
func TestPluginInstallLocalPathStillWorks(t *testing.T) {
	// 在磁盘写一个包目录（不打包），直接给本地路径
	pkgDir := t.TempDir()
	writeSamplePackage(t, pkgDir, "sample", "0.5.1")

	root := t.TempDir()
	cfg := config.Default()
	cfg.App.DataDir = filepath.Join(root, "data")
	cfg.App.PluginsDir = filepath.Join(root, "plugins")
	cfgPath := filepath.Join(root, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// path 形式（含 filepath separator） → 走本地路径分支
	if _, _, err := runMOW(t, cfgPath, "plugin", "install", pkgDir); err != nil {
		t.Fatalf("install path: %v", err)
	}
	if out, _, err := runMOW(t, cfgPath, "plugin", "list"); err != nil || !strings.Contains(out, "sample\t0.5.1") {
		t.Fatalf("list after local install: %v\n%s", err, out)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func runMOW(t *testing.T, cfgPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// runCLI 定义在 plugin_catalog_test.go 里，签名一致。
	return runCLI(t, cfgPath, args...)
}

// writeSamplePackage 生成一个仅在磁盘上的包目录（未打包）。
func writeSamplePackage(t *testing.T, dir, id, version string) {
	t.Helper()
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
  "name": "Sample",
  "version": %q,
  "compatibility": {"core": %q},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/plugin", "checksum": "sha256:%s"}]
}`, id, version, ">=0.5.0,<99.0.0", runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	// 用当前 sdk 版本做兼容 —— compat 范围必须涵盖 sdkversion.Version
	_ = sdkversion.Version
	if err := os.WriteFile(filepath.Join(dir, manifest.ManifestFileName), []byte(mfJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

// packSamplePackage 把一个临时目录形式的插件包压成 tar.gz 字节返回。
func packSamplePackage(t *testing.T, id, version string) []byte {
	t.Helper()
	dir := t.TempDir()
	writeSamplePackage(t, dir, id, version)
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
		hdr.Typeflag = tar.TypeReg
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
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
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// 静态断言 json 编解码可用，避免 goimports 剔除 encoding/json 依赖。
var _ = json.Marshal
