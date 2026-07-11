package manifest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
)

// buildValidPackage 在临时目录里搭一份可通过校验的完整包：
// - plugin.json（一份合法 Manifest，checksum 与实际二进制匹配）
// - bin/entrypoint-linux-amd64（内容 "linux-binary"）
// - recipes/system.cpu.yaml
// - workflows/deploy.node.yaml
func buildValidPackage(t *testing.T) (dir string, entryContent []byte, entryChecksum string) {
	t.Helper()
	dir = t.TempDir()

	entryContent = []byte("linux-binary\n")
	sum := sha256.Sum256(entryContent)
	entryChecksum = "sha256:" + hex.EncodeToString(sum[:])

	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "recipes"), 0o755); err != nil {
		t.Fatalf("mkdir recipes: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workflows"), 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	writeFile(t, filepath.Join(dir, "bin", "entrypoint-linux-amd64"), entryContent)
	writeFile(t, filepath.Join(dir, "recipes", "system.cpu.yaml"), []byte("id: system.cpu\n"))
	writeFile(t, filepath.Join(dir, "workflows", "deploy.node.yaml"), []byte("name: deploy\n"))

	m := `{
  "manifestVersion": 1,
  "id": "sample",
  "name": "Sample",
  "version": "0.5.0",
  "compatibility": {"core": ">=0.5.0,<0.6.0"},
  "platforms": [
    {"os": "linux", "arch": "amd64", "entrypoint": "bin/entrypoint-linux-amd64", "checksum": "` + entryChecksum + `"}
  ],
  "recipes":   [{"id": "system.cpu",  "path": "recipes/system.cpu.yaml"}],
  "workflows": [{"id": "deploy.node", "path": "workflows/deploy.node.yaml"}]
}`
	writeFile(t, filepath.Join(dir, manifest.ManifestFileName), []byte(m))
	return dir, entryContent, entryChecksum
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestValidatePackage_Happy(t *testing.T) {
	dir, _, _ := buildValidPackage(t)
	rep, err := manifest.ValidatePackage(dir)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("report not OK: %+v", rep)
	}
	kinds := map[string]int{}
	for _, c := range rep.Checks {
		kinds[c.Kind]++
		if !c.OK {
			t.Errorf("unexpected failed check: %+v", c)
		}
	}
	// 1 entrypoint + 1 checksum + 1 recipe + 1 workflow
	if kinds["entrypoint"] != 1 || kinds["checksum"] != 1 || kinds["recipe"] != 1 || kinds["workflow"] != 1 {
		t.Errorf("unexpected check kinds: %+v", kinds)
	}
}

func TestValidatePackage_MissingEntrypoint(t *testing.T) {
	dir, _, _ := buildValidPackage(t)
	if err := os.Remove(filepath.Join(dir, "bin", "entrypoint-linux-amd64")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	rep, err := manifest.ValidatePackage(dir)
	if err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
	assertErrCode(t, err, manifest.ErrCodeEntrypointMissing)
	if rep == nil {
		t.Fatal("expected partial report even on failure")
	}
	found := false
	for _, c := range rep.Checks {
		if c.Kind == "entrypoint" && !c.OK {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected an entrypoint failure in report")
	}
}

func TestValidatePackage_ChecksumMismatch(t *testing.T) {
	dir, _, _ := buildValidPackage(t)
	// 覆盖二进制内容 → sha256 变化，Manifest 的声明就成了坏数据
	writeFile(t, filepath.Join(dir, "bin", "entrypoint-linux-amd64"), []byte("tampered\n"))
	rep, err := manifest.ValidatePackage(dir)
	if err == nil {
		t.Fatal("expected checksum error")
	}
	assertErrCode(t, err, manifest.ErrCodeChecksumMismatch)
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("error is not *sdk.Error: %T", err)
	}
	if !strings.HasPrefix(se.Details["expected"].(string), "sha256:") {
		t.Errorf("expected sha256:<hex>, got %v", se.Details["expected"])
	}
	if !strings.HasPrefix(se.Details["actual"].(string), "sha256:") {
		t.Errorf("actual sha256 form invalid: %v", se.Details["actual"])
	}
	_ = rep
}

func TestValidatePackage_MissingRecipe(t *testing.T) {
	dir, _, _ := buildValidPackage(t)
	if err := os.Remove(filepath.Join(dir, "recipes", "system.cpu.yaml")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	_, err := manifest.ValidatePackage(dir)
	if err == nil {
		t.Fatal("expected error for missing recipe path")
	}
	assertErrCode(t, err, manifest.ErrCodeEntrypointMissing)
}

func TestValidatePackage_InvalidManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, manifest.ManifestFileName), []byte(`{"manifestVersion": 1}`))
	_, err := manifest.ValidatePackage(dir)
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
	assertErrCode(t, err, manifest.ErrCodeManifestInvalid)
}

func TestValidatePackage_EntrypointIsDirectory(t *testing.T) {
	dir, _, _ := buildValidPackage(t)
	// 删除文件，用同名目录顶替
	target := filepath.Join(dir, "bin", "entrypoint-linux-amd64")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := manifest.ValidatePackage(dir)
	if err == nil {
		t.Fatal("expected error for entrypoint being a directory")
	}
	assertErrCode(t, err, manifest.ErrCodeEntrypointMissing)
}
