package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
)

// runPluginDevCLI executes `mow <args...>` and captures stdout/stderr.
// plugin_catalog_test.go already defines a `runCLI(t, cfgPath, args...)` helper
// with a different signature, so this file uses a dedicated name.
func runPluginDevCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// -----------------------------------------------------------------------------
// mow plugin init
// -----------------------------------------------------------------------------

func TestPluginInit_GeneratesSkeletonAndLintsClean(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "acme")

	stdout, _, err := runPluginDevCLI(t, "plugin", "init", "acme", "--dir", target)
	if err != nil {
		t.Fatalf("plugin init failed: %v\nstdout:\n%s", err, stdout)
	}

	for _, rel := range []string{"plugin.json", "main.go", "go.mod", "README.md"} {
		if _, statErr := os.Stat(filepath.Join(target, rel)); statErr != nil {
			t.Errorf("missing generated file %s: %v", rel, statErr)
		}
	}
	if !strings.Contains(stdout, "Next steps:") {
		t.Errorf("stdout should print next-step hint:\n%s", stdout)
	}

	// The generated plugin.json must pass sdk/manifest.Load so that
	// `mow plugin lint` is guaranteed to succeed on a fresh scaffold.
	m, err := manifest.Load(target)
	if err != nil {
		t.Fatalf("generated manifest failed manifest.Load: %v", err)
	}
	if m.ID != "acme" {
		t.Errorf("generated manifest ID = %q, want %q", m.ID, "acme")
	}
	if len(m.Platforms) == 0 {
		t.Errorf("generated manifest should declare platforms[]")
	}
	if len(m.Commands) == 0 || m.Commands[0].ID != "hello" {
		t.Errorf("generated manifest should declare hello command: %+v", m.Commands)
	}

	goMod, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(goMod), "module example.com/mow-acme-plugin") {
		t.Errorf("go.mod missing module directive:\n%s", string(goMod))
	}
	if !strings.Contains(string(goMod), "github.com/mow/mow/sdk") {
		t.Errorf("go.mod should require the SDK:\n%s", string(goMod))
	}

	mainGo, err := os.ReadFile(filepath.Join(target, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if strings.Contains(string(mainGo), "__ID__") || strings.Contains(string(mainGo), "__NAME__") {
		t.Errorf("main.go still contains unrendered placeholder:\n%s", string(mainGo))
	}
	if !strings.Contains(string(mainGo), `"acme"`) {
		t.Errorf("main.go should embed plugin id:\n%s", string(mainGo))
	}
}

func TestPluginInit_RejectsInvalidID(t *testing.T) {
	tmp := t.TempDir()
	_, stderr, err := runPluginDevCLI(t, "plugin", "init", "BadID", "--dir", filepath.Join(tmp, "x"))
	if err == nil {
		t.Fatal("expected error for invalid id")
	}
	if !strings.Contains(err.Error(), "invalid plugin id") {
		t.Errorf("error should mention invalid id, got: %v (stderr=%s)", err, stderr)
	}
}

func TestPluginInit_RefusesOverwriteWithoutForce(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "acme")

	if _, _, err := runPluginDevCLI(t, "plugin", "init", "acme", "--dir", target); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	before, err := os.ReadFile(filepath.Join(target, "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	_, _, err = runPluginDevCLI(t, "plugin", "init", "acme", "--dir", target)
	if err == nil {
		t.Fatal("second init without --force should fail")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention existing file, got: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(target, "plugin.json"))
	if err != nil {
		t.Fatalf("re-read plugin.json: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("plugin.json must remain unchanged when overwrite is refused")
	}

	if _, _, err := runPluginDevCLI(t, "plugin", "init", "acme", "--dir", target, "--force"); err != nil {
		t.Fatalf("init --force should succeed: %v", err)
	}
}

// -----------------------------------------------------------------------------
// mow plugin lint
// -----------------------------------------------------------------------------

func TestPluginLint_HappyText(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "acme")
	if _, _, err := runPluginDevCLI(t, "plugin", "init", "acme", "--dir", target); err != nil {
		t.Fatalf("scaffold acme: %v", err)
	}

	stdout, _, err := runPluginDevCLI(t, "plugin", "lint", "--dir", target)
	if err != nil {
		t.Fatalf("plugin lint should pass: %v\nstdout:\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "OK") || !strings.Contains(stdout, "acme@0.1.0") {
		t.Errorf("lint output missing OK header:\n%s", stdout)
	}
}

func TestPluginLint_JSONReport(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "acme")
	if _, _, err := runPluginDevCLI(t, "plugin", "init", "acme", "--dir", target); err != nil {
		t.Fatalf("scaffold acme: %v", err)
	}

	stdout, _, err := runPluginDevCLI(t, "plugin", "lint", "--dir", target, "--json")
	if err != nil {
		t.Fatalf("plugin lint --json: %v", err)
	}
	var r pluginLintReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("lint output not JSON: %v\n%s", err, stdout)
	}
	if !r.OK {
		t.Errorf("lint report OK=false; error=%+v", r.Error)
	}
	if r.Manifest == nil || r.Manifest.ID != "acme" {
		t.Errorf("lint report manifest wrong: %+v", r.Manifest)
	}
}

func TestPluginLint_ReportsManifestInvalid(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, manifest.ManifestFileName),
		[]byte(`{"manifestVersion": 1}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	stdout, stderr, err := runPluginDevCLI(t, "plugin", "lint", "--dir", tmp, "--json")
	if err == nil {
		t.Fatal("lint should fail for invalid manifest")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != manifest.ErrCodeManifestInvalid {
		t.Fatalf("expected %s error, got: %v", manifest.ErrCodeManifestInvalid, err)
	}
	var r pluginLintReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("json decode: %v\n%s", err, stdout)
	}
	if r.OK {
		t.Error("lint report should be OK=false")
	}
	if r.Error == nil || r.Error.Code != manifest.ErrCodeManifestInvalid {
		t.Errorf("lint report error code mismatch: %+v", r.Error)
	}
	_ = stderr
}
