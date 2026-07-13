package plugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateChecksumFormat(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	if err := validateChecksumFormat(good); err != nil {
		t.Fatalf("want ok, got %v", err)
	}
	cases := []string{
		"",
		"sha256:",
		"sha1:" + strings.Repeat("a", 40),
		"sha256:" + strings.Repeat("z", 64),
		"sha256:" + strings.Repeat("a", 63),
	}
	for _, c := range cases {
		if err := validateChecksumFormat(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestDownloadTarGzHappyPath(t *testing.T) {
	pkg := buildSamplePackage(t)
	archive := tarGzOfDir(t, pkg)
	sum := sha256Sum(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir, err := Download(context.Background(), srv.URL+"/pkg.tar.gz", "sha256:"+sum, DownloadOptions{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer os.RemoveAll(dir)

	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatalf("plugin.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "plugin")); err != nil {
		t.Fatalf("entrypoint missing: %v", err)
	}
}

func TestDownloadZipWithTopLevelDir(t *testing.T) {
	pkg := buildSamplePackage(t)
	archive := zipOfDirWithPrefix(t, pkg, "sample-plugin-0.5.1/")
	sum := sha256Sum(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir, err := Download(context.Background(), srv.URL+"/pkg.zip", "sha256:"+sum, DownloadOptions{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer os.RemoveAll(dir)
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatalf("plugin.json missing (top-level dir unwrapping failed): %v", err)
	}
}

func TestDownloadRejectsChecksumMismatch(t *testing.T) {
	pkg := buildSamplePackage(t)
	archive := tarGzOfDir(t, pkg)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	bogus := "sha256:" + strings.Repeat("0", 64)
	if _, err := Download(context.Background(), srv.URL+"/pkg.tar.gz", bogus, DownloadOptions{}); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestDownloadRespectsMaxBytes(t *testing.T) {
	pkg := buildSamplePackage(t)
	archive := tarGzOfDir(t, pkg)
	sum := sha256Sum(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	// 上限设成极小值
	if _, err := Download(context.Background(), srv.URL+"/pkg.tar.gz", "sha256:"+sum, DownloadOptions{MaxBytes: 10}); err == nil {
		t.Fatal("expected max-bytes error")
	}
}

func TestDownloadRejectsExpandedArchiveOverMax(t *testing.T) {
	large := bytes.Repeat([]byte("x"), 64*1024)

	tarBuf := new(bytes.Buffer)
	gz := gzip.NewWriter(tarBuf)
	tw := tar.NewWriter(gz)
	writeTarFile(t, tw, "plugin.json", []byte("{}"))
	writeTarFile(t, tw, "bin/plugin", large)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	zipBuf := new(bytes.Buffer)
	zw := zip.NewWriter(zipBuf)
	for name, data := range map[string][]byte{"plugin.json": []byte("{}"), "bin/plugin": large} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	for name, archive := range map[string][]byte{"pkg.tar.gz": tarBuf.Bytes(), "pkg.zip": zipBuf.Bytes()} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(archive)
			}))
			defer srv.Close()
			maxBytes := int64(len(archive) + 1024)
			_, err := Download(context.Background(), srv.URL+"/"+name, "sha256:"+sha256Sum(archive), DownloadOptions{MaxBytes: maxBytes})
			if err == nil || !strings.Contains(err.Error(), "extracted content exceeds") {
				t.Fatalf("expected expanded-size rejection, got %v", err)
			}
		})
	}
}

func TestDownloadFileSchemeAndRejectTraversal(t *testing.T) {
	pkg := buildSamplePackage(t)
	// 制造一个包含穿越条目的 tar.gz
	buf := new(bytes.Buffer)
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(gz)
	// 正常条目
	writeTarFile(t, tw, "plugin.json", []byte("{}"))
	// 穿越条目
	writeTarFile(t, tw, "../evil.txt", []byte("nope"))
	tw.Close()
	gz.Close()
	sum := sha256Sum(buf.Bytes())

	blob := filepath.Join(t.TempDir(), "malicious.tar.gz")
	if err := os.WriteFile(blob, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(blob)}
	if runtime.GOOS == "windows" {
		u.Path = "/" + filepath.ToSlash(blob)
	}
	if _, err := Download(context.Background(), u.String(), "sha256:"+sum, DownloadOptions{}); err == nil {
		t.Fatal("expected path traversal rejection")
	}

	// 正常 file:// 场景
	good := tarGzOfDir(t, pkg)
	goodPath := filepath.Join(t.TempDir(), "good.tar.gz")
	if err := os.WriteFile(goodPath, good, 0o644); err != nil {
		t.Fatal(err)
	}
	u2 := &url.URL{Scheme: "file", Path: filepath.ToSlash(goodPath)}
	if runtime.GOOS == "windows" {
		u2.Path = "/" + filepath.ToSlash(goodPath)
	}
	dir, err := Download(context.Background(), u2.String(), "sha256:"+sha256Sum(good), DownloadOptions{})
	if err != nil {
		t.Fatalf("file:// download failed: %v", err)
	}
	defer os.RemoveAll(dir)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func buildSamplePackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("plugin-binary")
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifest := fmt.Sprintf(`{
  "manifestVersion": 1,
  "id": "sample",
  "name": "Sample",
  "version": "0.5.1",
  "compatibility": {"core": ">=0.5.0,<0.6.0"},
  "platforms": [{"os": %q, "arch": %q, "entrypoint": "bin/plugin", "checksum": "sha256:%s"}]
}`, runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func tarGzOfDir(t *testing.T, root string) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644}
		if info.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
			hdr.Mode = 0o755
			return tw.WriteHeader(hdr)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hdr.Typeflag = tar.TypeReg
		hdr.Size = int64(len(data))
		if strings.Contains(path, "bin") {
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

func zipOfDirWithPrefix(t *testing.T, root, prefix string) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := prefix + filepath.ToSlash(rel)
		if info.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTarFile(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Typeflag: tar.TypeReg,
		Size:     int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
}

func sha256Sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
