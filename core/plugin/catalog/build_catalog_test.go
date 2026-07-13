package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBuildCatalogScript 通过 `go run scripts/build-catalog.go` 端到端验证
// 官方 Catalog 生成脚本，确保：
//   - 从 plugins/<id>/plugin.json 抽取元信息
//   - 计算 tar.gz 的 SHA-256 并写入 checksum
//   - 生成的 catalog.json 可被 Parse 正确解析
//   - 缺失 artifact 时脚本以非零码退出
func TestBuildCatalogScript(t *testing.T) {
	repoRoot := findRepoRoot(t)

	pluginsDir := t.TempDir()
	artifactsDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "catalog.json")

	// 1) 写一个假的 plugin.json
	if err := os.MkdirAll(filepath.Join(pluginsDir, "sample"), 0o755); err != nil {
		t.Fatal(err)
	}
	mfJSON := `{
  "manifestVersion": 1,
  "id": "sample",
  "name": "Sample",
  "description": "sample plugin",
  "author": "acme",
  "version": "0.5.1",
  "compatibility": {"core": ">=0.5.0,<0.6.0"},
  "platforms": [
    {"os": "linux",   "arch": "amd64", "entrypoint": "bin/plugin", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
    {"os": "darwin",  "arch": "arm64", "entrypoint": "bin/plugin", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
    {"os": "windows", "arch": "amd64", "entrypoint": "bin/plugin.exe", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
  ]
}`
	if err := os.WriteFile(filepath.Join(pluginsDir, "sample", "plugin.json"), []byte(mfJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2) 生成三份假的 tar.gz（内容任意，脚本只关心 sha256）
	files := []string{
		"mow-sample-plugin-linux-amd64.tar.gz",
		"mow-sample-plugin-darwin-arm64.tar.gz",
		"mow-sample-plugin-windows-amd64.tar.gz",
	}
	for i, f := range files {
		body := []byte("fake-tar-" + f)
		body = append(body, byte(i))
		if err := os.WriteFile(filepath.Join(artifactsDir, f), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 3) 缺 windows artifact 时应报错：先删一份跑一次
	tmpMissingDir := t.TempDir()
	for _, f := range files[:2] {
		body, err := os.ReadFile(filepath.Join(artifactsDir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpMissingDir, f), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := runScript(t, repoRoot, pluginsDir, tmpMissingDir, "https://example/download", "v0.5.1", outPath); err == nil {
		t.Fatalf("expected build-catalog to fail when artifact is missing")
	}

	// 4) 完整 artifacts 应成功
	if err := runScript(t, repoRoot, pluginsDir, artifactsDir, "https://example/download", "v0.5.1", outPath); err != nil {
		t.Fatalf("build-catalog failed: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("parse produced catalog: %v", err)
	}
	if c.SchemaVersion != 1 || c.Source != "official" || len(c.Entries) != 1 {
		t.Fatalf("unexpected catalog: %+v", c)
	}
	e := c.Entries[0]
	if e.ID != "sample" || e.Name != "Sample" || e.Author != "acme" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if len(e.Versions) != 1 || e.Versions[0].Version != "0.5.1" {
		t.Fatalf("unexpected versions: %+v", e.Versions)
	}
	arts := e.Versions[0].Platforms
	if len(arts) != 3 {
		t.Fatalf("expected 3 artifacts, got %d", len(arts))
	}
	// 校验 URL 拼接 & checksum 前缀
	for _, a := range arts {
		if a.URL == "" || a.Checksum == "" {
			t.Fatalf("artifact missing url/checksum: %+v", a)
		}
		if len(a.Checksum) != len("sha256:")+64 {
			t.Fatalf("bad checksum shape: %q", a.Checksum)
		}
	}
}

func runScript(t *testing.T, repoRoot, pluginsDir, artifactsDir, baseURL, version, out string) error {
	t.Helper()
	cmd := exec.Command("go", "run", "./scripts/build-catalog.go",
		"-plugins", "sample",
		"-plugins-dir", pluginsDir,
		"-artifacts", artifactsDir,
		"-base-url", baseURL,
		"-version", version,
		"-out", out,
	)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOFLAGS=") // 避免继承一些 CI-only 标志
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("build-catalog output:\n%s", string(output))
	}
	return err
}

// findRepoRoot 通过向上查找 go.work 定位仓库根。
func findRepoRoot(t *testing.T) string {
	t.Helper()
	// 从 catalog_test 的绝对路径开始向上，最多五级足够（core/plugin/catalog → repo）
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find go.work above cwd (runtime=%s)", runtime.GOOS)
	return ""
}
