package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
	"github.com/spf13/cobra"
)

// -----------------------------------------------------------------------------
// 插件开发者体验命令（v0.5.4）
//
//   mow plugin init      —— 生成最小骨架 plugin.json + main.go + go.mod + README
//   mow plugin lint      —— 只做 Manifest schema + 语义校验（不触磁盘）
//   mow plugin package   —— go build 编译入口二进制、注入 checksum、打成 tar.gz + .sha256
//
// 这三个子命令都作用于源码目录（`--dir`，默认为 CWD），不需要 App 依赖，
// 因此不通过 appHolder 传入。
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// plugin init
// -----------------------------------------------------------------------------

func newPluginInitCmd() *cobra.Command {
	var (
		dir    string
		name   string
		author string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "init <id>",
		Short: "Scaffold a minimal plugin package (plugin.json + main.go + go.mod)",
		Long: `Init generates a minimal source-tree skeleton for a new MOW plugin:

  <dir>/plugin.json   Manifest with a single "hello" command
  <dir>/main.go       pluginserve entry that registers the hello command
  <dir>/go.mod        module <module>, replace ../../sdk when inside repo
  <dir>/README.md     next-step instructions

The generated package is ready to be built with 'mow plugin package'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				name = strings.Title(args[0]) //nolint:staticcheck // 生成场景的展示名，无需 unicode 大小写库
			}
			if author == "" {
				author = "unknown"
			}
			return runPluginInit(cmd.OutOrStdout(), pluginInitOpts{
				Dir:    dir,
				ID:     args[0],
				Name:   name,
				Author: author,
				Force:  force,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "output directory (default: <id>)")
	f.StringVar(&name, "name", "", "display name (default: capitalized id)")
	f.StringVar(&author, "author", "", "author metadata (default: unknown)")
	f.BoolVar(&force, "force", false, "overwrite existing files if present")
	return cmd
}

type pluginInitOpts struct {
	Dir    string
	ID     string
	Name   string
	Author string
	Force  bool
}

func runPluginInit(stdout io.Writer, o pluginInitOpts) error {
	if !isValidPluginID(o.ID) {
		return fmt.Errorf("invalid plugin id %q: must match [a-z][a-z0-9_-]{1,63}", o.ID)
	}
	dir := o.Dir
	if dir == "" {
		dir = o.ID
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve dir: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", absDir, err)
	}

	files := map[string]string{
		"plugin.json": renderInitManifest(o.ID, o.Name, o.Author),
		"main.go":     renderInitMainGo(o.ID, o.Name),
		"go.mod":      renderInitGoMod(o.ID),
		"README.md":   renderInitReadme(o.ID),
	}
	// 稳定顺序，便于用户阅读输出。
	for _, rel := range []string{"plugin.json", "main.go", "go.mod", "README.md"} {
		content := files[rel]
		full := filepath.Join(absDir, rel)
		if !o.Force {
			if _, statErr := os.Stat(full); statErr == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", full)
			}
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		fmt.Fprintf(stdout, "created %s\n", full)
	}
	fmt.Fprintf(stdout, "\nNext steps:\n")
	fmt.Fprintf(stdout, "  cd %s\n", dir)
	fmt.Fprintf(stdout, "  mow plugin lint\n")
	fmt.Fprintf(stdout, "  mow plugin package --os %s --arch %s\n", runtime.GOOS, runtime.GOARCH)
	return nil
}

// isValidPluginID 与 sdk/manifest.idPattern 语义一致：^[a-z][a-z0-9_-]{1,63}$
func isValidPluginID(id string) bool {
	if len(id) < 2 || len(id) > 64 {
		return false
	}
	if id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

func renderInitManifest(id, name, author string) string {
	// 保持与 plugins/ssh/plugin.json 相同的字段顺序，便于对照阅读。
	// entrypoint checksum 用占位符；`mow plugin package` 会替换为真实哈希。
	tmpl := `{
  "manifestVersion": 1,
  "id": "%s",
  "name": "%s",
  "version": "0.1.0",
  "author": "%s",
  "license": "Apache-2.0",
  "description": "TODO: describe %s",
  "compatibility": {
    "core": ">=0.5.0,<0.6.0",
    "sdk":  ">=0.5.0,<0.6.0"
  },
  "platforms": [
    {"os": "linux",   "arch": "amd64", "entrypoint": "bin/mow-%s-plugin",     "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
    {"os": "linux",   "arch": "arm64", "entrypoint": "bin/mow-%s-plugin",     "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
    {"os": "darwin",  "arch": "amd64", "entrypoint": "bin/mow-%s-plugin",     "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
    {"os": "darwin",  "arch": "arm64", "entrypoint": "bin/mow-%s-plugin",     "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
    {"os": "windows", "arch": "amd64", "entrypoint": "bin/mow-%s-plugin.exe", "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
  ],
  "permissions": ["read"],
  "commands": [
    {"id": "hello", "permission": "read", "description": "sanity-check command emitted by 'mow plugin init'"}
  ]
}
`
	return fmt.Sprintf(tmpl, id, name, author, id, id, id, id, id, id)
}

func renderInitMainGo(id, name string) string {
	// 生成一个 pluginserve.Serve 入口 + 单个 hello command。
	// 不引入额外依赖，避免脚手架构建失败。
	//
	// 用占位符 __ID__ / __NAME__ 替换，而不是 fmt.Sprintf —— 生成的 Go 源码
	// 里保留了 `hello from %s` 等 fmt 动词，直接 Sprintf 会被 `go vet` 误判。
	tmpl := `// Package main is the entry point of the mow-__ID__-plugin generated by
// 'mow plugin init'. Extend Commands() with your real command handlers.
package main

import (
	"context"
	"encoding/json"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginserve"
	"github.com/mow/mow/sdk/version"
)

type plugin struct{}

func (p *plugin) Metadata() sdk.Metadata {
	return sdk.Metadata{
		ID:          "__ID__",
		Name:        "__NAME__",
		Version:     version.Version,
		Author:      "unknown",
		Description: "generated by 'mow plugin init'",
		CoreVersion: ">=0.5.0,<0.6.0",
	}
}

func (p *plugin) Init(ctx context.Context, req sdk.InitRequest) error { return nil }
func (p *plugin) Shutdown(ctx context.Context) error                  { return nil }
func (p *plugin) HealthCheck(ctx context.Context) sdk.HealthStatus    { return sdk.StatusHealthy }
func (p *plugin) Commands() []sdk.CommandHandler                      { return []sdk.CommandHandler{&helloCmd{}} }

type helloCmd struct{}

func (c *helloCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "hello",
		Permission:  sdk.PermRead,
		Description: "sanity-check command emitted by 'mow plugin init'",
	}
}

func (c *helloCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	payload, err := json.Marshal(map[string]string{"message": "hello from __ID__"})
	if err != nil {
		return nil, err
	}
	return &sdk.ExecuteResponse{Data: payload}, nil
}

// ExecuteStream is required by sdk.CommandHandler; hello is one-shot.
func (c *helloCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func main() {
	pluginserve.Serve(&plugin{})
}
`
	out := strings.ReplaceAll(tmpl, "__ID__", id)
	out = strings.ReplaceAll(out, "__NAME__", name)
	return out
}

func renderInitGoMod(id string) string {
	// module path 用 example.com/<id>-plugin，避免与真实仓库冲突；
	// 用户可按需改成自己的模块路径。SDK 依赖靠 `go get` 或 replace 指向本地。
	return fmt.Sprintf(`module example.com/mow-%s-plugin

go 1.25.0

require github.com/mow/mow/sdk v0.5.3
`, id)
}

func renderInitReadme(id string) string {
	return fmt.Sprintf(`# mow-%s-plugin

Scaffold generated by `+"`mow plugin init`"+`. Next steps:

1. Adjust `+"`plugin.json`"+` metadata (name, description, permissions, commands).
2. Implement real command handlers in `+"`main.go`"+`.
3. Fetch the SDK: `+"`go get github.com/mow/mow/sdk@latest`"+`.
4. Lint the manifest: `+"`mow plugin lint`"+`.
5. Build a release artifact: `+"`mow plugin package --os linux --arch amd64`"+`.
`, id)
}

// -----------------------------------------------------------------------------
// plugin lint
// -----------------------------------------------------------------------------

func newPluginLintCmd() *cobra.Command {
	var (
		dir     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Lint plugin.json (Manifest schema + semantic checks, no disk IO)",
		Long: `Lint parses <dir>/plugin.json against the v0.5.0 Manifest schema and
runs semantic validation identical to 'mow plugin validate' — but does
not touch entrypoints, checksums, recipes or workflows on disk. Use it
for fast pre-commit / pre-package feedback.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginLint(cmd.OutOrStdout(), cmd.ErrOrStderr(), dir, jsonOut)
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", ".", "plugin source directory containing plugin.json")
	f.BoolVar(&jsonOut, "json", false, "emit JSON report")
	return cmd
}

type pluginLintReport struct {
	OK       bool                 `json:"ok"`
	Path     string               `json:"path"`
	Manifest *pluginValidateMeta  `json:"manifest,omitempty"`
	Error    *pluginValidateError `json:"error,omitempty"`
}

func runPluginLint(stdout, stderr io.Writer, dir string, jsonOut bool) error {
	if dir == "" {
		dir = "."
	}
	report := &pluginLintReport{Path: filepath.Join(dir, manifest.ManifestFileName)}

	m, err := manifest.Load(dir)
	if err != nil {
		var se *sdk.Error
		if errors.As(err, &se) {
			report.Error = &pluginValidateError{Code: se.Code, Message: se.Message, Details: se.Details}
		} else {
			report.Error = &pluginValidateError{Code: "UNKNOWN", Message: err.Error()}
		}
		emitLintReport(stdout, stderr, report, jsonOut)
		return err
	}
	report.OK = true
	report.Manifest = &pluginValidateMeta{ID: m.ID, Name: m.Name, Version: m.Version}
	emitLintReport(stdout, stderr, report, jsonOut)
	return nil
}

func emitLintReport(stdout, stderr io.Writer, r *pluginLintReport, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return
	}
	if r.OK {
		fmt.Fprintf(stdout, "OK  %s@%s (%s)\n", r.Manifest.ID, r.Manifest.Version, r.Manifest.Name)
		return
	}
	fmt.Fprintf(stderr, "FAIL %s\n", r.Path)
	if r.Error != nil {
		fmt.Fprintf(stderr, "  [%s] %s\n", r.Error.Code, r.Error.Message)
		for _, k := range detailKeys(r.Error.Details) {
			fmt.Fprintf(stderr, "    %s: %v\n", k, r.Error.Details[k])
		}
	}
}

// -----------------------------------------------------------------------------
// plugin package
// -----------------------------------------------------------------------------

func newPluginPackageCmd() *cobra.Command {
	var (
		dir      string
		outDir   string
		goos     string
		goarch   string
		version  string
		ldflags  string
		trimpath bool
		keepDir  bool
	)
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Build + package a plugin into mow-<id>-plugin-<os>-<arch>.tar.gz",
		Long: `Package compiles the plugin entrypoint from <dir> for the requested
target (defaults to host GOOS/GOARCH), rewrites plugin.json to keep only
the selected platforms[] entry with the real SHA-256 checksum, and emits:

  <out>/mow-<id>-plugin-<os>-<arch>.tar.gz
  <out>/mow-<id>-plugin-<os>-<arch>.tar.gz.sha256

The tar.gz layout matches the release artifact so it can be dropped into
a catalog or unpacked into <plugins_dir>/<id>.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginPackage(cmd.OutOrStdout(), pluginPackageOpts{
				Dir:      dir,
				OutDir:   outDir,
				GOOS:     goos,
				GOARCH:   goarch,
				Version:  version,
				LDFlags:  ldflags,
				Trimpath: trimpath,
				KeepDir:  keepDir,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", ".", "plugin source directory containing plugin.json + main.go")
	f.StringVar(&outDir, "out", "dist", "output directory for the tar.gz + .sha256")
	f.StringVar(&goos, "os", runtime.GOOS, "target GOOS")
	f.StringVar(&goarch, "arch", runtime.GOARCH, "target GOARCH")
	f.StringVar(&version, "version", "", "override plugin.json version (default: keep manifest version)")
	f.StringVar(&ldflags, "ldflags", "-s -w", "extra ldflags passed to 'go build'")
	f.BoolVar(&trimpath, "trimpath", true, "pass -trimpath to 'go build'")
	f.BoolVar(&keepDir, "keep-staging", false, "keep the intermediate staging directory for debugging")
	return cmd
}

type pluginPackageOpts struct {
	Dir      string
	OutDir   string
	GOOS     string
	GOARCH   string
	Version  string
	LDFlags  string
	Trimpath bool
	KeepDir  bool
}

func runPluginPackage(stdout io.Writer, o pluginPackageOpts) error {
	if o.Dir == "" {
		o.Dir = "."
	}
	if o.OutDir == "" {
		o.OutDir = "dist"
	}
	if o.GOOS == "" || o.GOARCH == "" {
		return fmt.Errorf("--os and --arch must be non-empty")
	}
	if _, ok := allowedGOOS[o.GOOS]; !ok {
		return fmt.Errorf("unsupported GOOS %q (expected linux / darwin / windows)", o.GOOS)
	}
	if _, ok := allowedGOARCH[o.GOARCH]; !ok {
		return fmt.Errorf("unsupported GOARCH %q (expected amd64 / arm64)", o.GOARCH)
	}

	srcDir, err := filepath.Abs(o.Dir)
	if err != nil {
		return fmt.Errorf("resolve dir: %w", err)
	}
	outDir, err := filepath.Abs(o.OutDir)
	if err != nil {
		return fmt.Errorf("resolve out: %w", err)
	}

	// 1) 加载并校验 Manifest；同时定位 (os,arch) 对应的 entrypoint。
	m, err := manifest.Load(srcDir)
	if err != nil {
		return err
	}
	var target *manifest.Platform
	for i := range m.Platforms {
		if m.Platforms[i].OS == o.GOOS && m.Platforms[i].Arch == o.GOARCH {
			p := m.Platforms[i]
			target = &p
			break
		}
	}
	if target == nil {
		return fmt.Errorf("plugin.json has no platforms entry for %s/%s", o.GOOS, o.GOARCH)
	}

	// 2) 解析原始 plugin.json 为通用 map，便于保留未识别字段 & 修改 platforms/version。
	rawManifest, err := os.ReadFile(filepath.Join(srcDir, manifest.ManifestFileName))
	if err != nil {
		return fmt.Errorf("read plugin.json: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rawManifest, &doc); err != nil {
		return fmt.Errorf("decode plugin.json: %w", err)
	}

	// 3) 编译二进制到临时 staging 目录。
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out: %w", err)
	}
	staging, err := os.MkdirTemp(outDir, fmt.Sprintf(".stage-%s-%s-%s-*", m.ID, o.GOOS, o.GOARCH))
	if err != nil {
		return fmt.Errorf("mkdir staging: %w", err)
	}
	if !o.KeepDir {
		defer os.RemoveAll(staging)
	}

	entryRel := filepath.FromSlash(target.Entrypoint)
	binOut := filepath.Join(staging, entryRel)
	if err := os.MkdirAll(filepath.Dir(binOut), 0o755); err != nil {
		return fmt.Errorf("mkdir entrypoint dir: %w", err)
	}

	fmt.Fprintf(stdout, "building %s/%s → %s\n", o.GOOS, o.GOARCH, target.Entrypoint)
	if err := runGoBuild(srcDir, binOut, o); err != nil {
		return err
	}

	// 4) 计算真实 checksum，并重写 platforms[] 为只留当前目标。
	sum, err := hashFileSHA256(binOut)
	if err != nil {
		return fmt.Errorf("hash entrypoint: %w", err)
	}
	target.Checksum = "sha256:" + sum
	newPlatforms, _ := json.Marshal([]manifest.Platform{*target})
	doc["platforms"] = newPlatforms
	if o.Version != "" {
		versionJSON, _ := json.Marshal(o.Version)
		doc["version"] = versionJSON
	}
	outManifest, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plugin.json: %w", err)
	}
	outManifest = append(outManifest, '\n')
	if err := os.WriteFile(filepath.Join(staging, manifest.ManifestFileName), outManifest, 0o644); err != nil {
		return fmt.Errorf("write plugin.json: %w", err)
	}

	// 5) staging → tar.gz + .sha256（与 scripts/package-plugin.go 产物形态一致）。
	displayTarget := shortTarget(o.GOOS)
	tarName := fmt.Sprintf("mow-%s-plugin-%s-%s.tar.gz", m.ID, displayTarget, o.GOARCH)
	tarPath := filepath.Join(outDir, tarName)
	if err := writeTarGz(tarPath, staging); err != nil {
		return fmt.Errorf("write tar.gz: %w", err)
	}
	archiveSum, err := hashFileSHA256(tarPath)
	if err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	shaLine := fmt.Sprintf("%s  %s\n", archiveSum, tarName)
	if err := os.WriteFile(tarPath+".sha256", []byte(shaLine), 0o644); err != nil {
		return fmt.Errorf("write .sha256: %w", err)
	}

	fmt.Fprintf(stdout, "packaged %s\n", tarPath)
	fmt.Fprintf(stdout, "         %s.sha256\n", tarPath)
	fmt.Fprintf(stdout, "entrypoint checksum: sha256:%s\n", sum)
	return nil
}

// shortTarget 将 GOOS 转为 release.yml 中使用的 target 短名。
// 目前 linux/darwin/windows 三者 target 与 GOOS 相同，保留一层间接便于未来别名。
func shortTarget(goos string) string { return goos }

var allowedGOOS = map[string]struct{}{"linux": {}, "darwin": {}, "windows": {}}
var allowedGOARCH = map[string]struct{}{"amd64": {}, "arm64": {}}

func runGoBuild(srcDir, binOut string, o pluginPackageOpts) error {
	args := []string{"build"}
	if o.Trimpath {
		args = append(args, "-trimpath")
	}
	if strings.TrimSpace(o.LDFlags) != "" {
		args = append(args, "-ldflags", o.LDFlags)
	}
	args = append(args, "-o", binOut, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(),
		"GOOS="+o.GOOS,
		"GOARCH="+o.GOARCH,
		"CGO_ENABLED=0",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}
	return nil
}

// hashFileSHA256 与 sdk/manifest/package.go 内部使用的函数保持行为一致：
// 返回 hex64（不带 "sha256:" 前缀）。
func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeTarGz 打包 srcDir 内所有文件（不含目录本身），写入 dst 为 gzip'd tar。
// 布局与 tar -C staging -czf dst . 等价：archive 内路径为相对于 staging 的
// slash 路径（例如 "plugin.json" / "bin/mow-foo-plugin"）。
func writeTarGz(dst, srcDir string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, copyErr := io.Copy(tw, src)
		return copyErr
	})
}
