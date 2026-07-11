package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
)

// buildTestPackage 在临时目录里搭一份包，返回目录与真实 checksum。
func buildTestPackage(t *testing.T) (dir string, checksum string) {
	t.Helper()
	dir = t.TempDir()

	content := []byte("test-binary\n")
	sum := sha256.Sum256(content)
	checksum = "sha256:" + hex.EncodeToString(sum[:])

	mustMkdir(t, filepath.Join(dir, "bin"))
	mustMkdir(t, filepath.Join(dir, "recipes"))
	mustMkdir(t, filepath.Join(dir, "workflows"))
	mustWrite(t, filepath.Join(dir, "bin", "entrypoint"), content)
	mustWrite(t, filepath.Join(dir, "recipes", "cpu.yaml"), []byte("id: cpu\n"))
	mustWrite(t, filepath.Join(dir, "workflows", "deploy.yaml"), []byte("name: deploy\n"))

	m := `{
  "manifestVersion": 1,
  "id": "sample",
  "name": "Sample",
  "version": "0.5.0",
  "compatibility": {"core": ">=0.5.0,<0.6.0"},
  "platforms": [
    {"os": "linux", "arch": "amd64", "entrypoint": "bin/entrypoint", "checksum": "` + checksum + `"}
  ],
  "recipes":   [{"id": "cpu",    "path": "recipes/cpu.yaml"}],
  "workflows": [{"id": "deploy", "path": "workflows/deploy.yaml"}]
}`
	mustWrite(t, filepath.Join(dir, manifest.ManifestFileName), []byte(m))
	return dir, checksum
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func runPluginValidateCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"plugin", "validate"}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestPluginValidate_HappyText(t *testing.T) {
	dir, _ := buildTestPackage(t)
	stdout, _, err := runPluginValidateCLI(t, dir)
	if err != nil {
		t.Fatalf("expected success, got: %v\nstdout:\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "OK:") {
		t.Errorf("expected OK summary, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "sample@0.5.0") {
		t.Errorf("expected plugin header, got:\n%s", stdout)
	}
}

func TestPluginValidate_HappyJSON(t *testing.T) {
	dir, _ := buildTestPackage(t)
	stdout, _, err := runPluginValidateCLI(t, "--json", dir)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	var report pluginValidateReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	if !report.OK {
		t.Errorf("report.OK = false, want true; error=%+v", report.Error)
	}
	if report.Manifest == nil || report.Manifest.ID != "sample" {
		t.Errorf("unexpected manifest: %+v", report.Manifest)
	}
	// 至少 4 项：manifest + entrypoint + checksum + recipe + workflow
	if len(report.Checks) < 5 {
		t.Errorf("expected >=5 checks, got %d", len(report.Checks))
	}
}

func TestPluginValidate_VerboseListsAllChecks(t *testing.T) {
	dir, _ := buildTestPackage(t)
	stdout, _, err := runPluginValidateCLI(t, "--verbose", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verbose 应打印每一条 "  ok" 前缀
	if strings.Count(stdout, "  ok ") < 4 {
		t.Errorf("verbose output missing per-check lines:\n%s", stdout)
	}
}

func TestPluginValidate_InvalidManifest(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, manifest.ManifestFileName), []byte(`{"manifestVersion": 1}`))

	stdout, stderr, err := runPluginValidateCLI(t, dir)
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("error is not *sdk.Error: %T", err)
	}
	if se.Code != manifest.ErrCodeManifestInvalid {
		t.Errorf("code = %q, want %q", se.Code, manifest.ErrCodeManifestInvalid)
	}
	if !strings.Contains(stderr, manifest.ErrCodeManifestInvalid) {
		t.Errorf("stderr should mention error code:\n%s", stderr)
	}
	_ = stdout
}

func TestPluginValidate_ChecksumMismatch(t *testing.T) {
	dir, _ := buildTestPackage(t)
	// 篡改二进制内容
	mustWrite(t, filepath.Join(dir, "bin", "entrypoint"), []byte("tampered\n"))

	stdout, stderr, err := runPluginValidateCLI(t, "--json", dir)
	if err == nil {
		t.Fatal("expected checksum failure")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeChecksumMismatch {
		t.Errorf("expected %s, got %v", manifest.ErrCodeChecksumMismatch, err)
	}
	var report pluginValidateReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, stdout)
	}
	if report.OK {
		t.Error("report.OK should be false")
	}
	if report.Error == nil || report.Error.Code != manifest.ErrCodeChecksumMismatch {
		t.Errorf("report.Error mismatch: %+v", report.Error)
	}
	// stderr 分支应静默（--json 场景），或至少不影响
	_ = stderr
}

func TestPluginValidate_MissingEntrypoint(t *testing.T) {
	dir, _ := buildTestPackage(t)
	if err := os.Remove(filepath.Join(dir, "bin", "entrypoint")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	_, stderr, err := runPluginValidateCLI(t, dir)
	if err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeEntrypointMissing {
		t.Errorf("expected %s, got %v", manifest.ErrCodeEntrypointMissing, err)
	}
	if !strings.Contains(stderr, manifest.ErrCodeEntrypointMissing) {
		t.Errorf("stderr should mention error code:\n%s", stderr)
	}
}

func TestPluginValidate_PathNotFound(t *testing.T) {
	stdout, _, err := runPluginValidateCLI(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeManifestInvalid {
		t.Errorf("expected %s, got %v", manifest.ErrCodeManifestInvalid, err)
	}
	// happy path 里不应出现
	if strings.Contains(stdout, "OK:") {
		t.Errorf("stdout should not report OK: %s", stdout)
	}
}
