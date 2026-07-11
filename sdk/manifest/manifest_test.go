package manifest_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
)

// validManifest 是一份最小合法 Manifest 的 JSON 表达。
const validManifest = `{
  "manifestVersion": 1,
  "id": "ssh",
  "name": "SSH",
  "version": "0.5.0",
  "author": "mow",
  "license": "Apache-2.0",
  "compatibility": {
    "core": ">=0.5.0,<0.6.0",
    "sdk": ">=0.5.0,<0.6.0",
    "protocol": ">=1.0.0"
  },
  "platforms": [
    {"os": "linux",   "arch": "amd64", "entrypoint": "bin/ssh-linux-amd64",   "checksum": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
    {"os": "windows", "arch": "amd64", "entrypoint": "bin/ssh-win-amd64.exe", "checksum": "sha256:1111111111111111111111111111111111111111111111111111111111111111"}
  ],
  "connectionTypes": ["ssh"],
  "permissions": ["read", "write", "execute"],
  "commands": [
    {"id": "exec",     "permission": "execute", "streaming": false, "description": "Run a command via SSH"},
    {"id": "shell",    "permission": "execute", "streaming": true},
    {"id": "download", "permission": "read"}
  ],
  "recipes":   [{"id": "system.cpu",   "path": "recipes/system.cpu.yaml"}],
  "workflows": [{"id": "deploy.node",  "path": "workflows/deploy.node.yaml"}],
  "dataVersion": 1
}`

func TestParse_Happy(t *testing.T) {
	m, err := manifest.Parse([]byte(validManifest))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != "ssh" {
		t.Errorf("id = %q, want ssh", m.ID)
	}
	if m.Version != "0.5.0" {
		t.Errorf("version = %q, want 0.5.0", m.Version)
	}
	if got := m.PlatformFor("linux", "amd64"); got == nil || got.Entrypoint == "" {
		t.Errorf("PlatformFor linux/amd64 should return entry, got %+v", got)
	}
	if got := m.PlatformFor("darwin", "amd64"); got != nil {
		t.Errorf("PlatformFor darwin/amd64 should be nil, got %+v", got)
	}
	if len(m.Commands) != 3 {
		t.Errorf("commands = %d, want 3", len(m.Commands))
	}
}

func TestParse_UTF8BOM(t *testing.T) {
	bom := append([]byte{0xEF, 0xBB, 0xBF}, []byte(validManifest)...)
	if _, err := manifest.Parse(bom); err != nil {
		t.Fatalf("expected BOM to be tolerated, got: %v", err)
	}
}

func TestParse_EmptyAndTrailing(t *testing.T) {
	if _, err := manifest.Parse(nil); err == nil {
		t.Fatal("expected error for empty input")
	}
	if _, err := manifest.Parse([]byte(validManifest + "{}")); err == nil {
		t.Fatal("expected error for trailing content")
	}
}

func TestParse_UnknownField(t *testing.T) {
	badJSON := strings.Replace(validManifest, `"license": "Apache-2.0"`, `"license": "Apache-2.0", "unexpected": 42`, 1)
	_, err := manifest.Parse([]byte(badJSON))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	assertErrCode(t, err, manifest.ErrCodeManifestInvalid)
}

// mutation lets a test mutate the parsed manifest before validation.
type mutation func(*map[string]any)

func mutated(t *testing.T, mut mutation) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(validManifest), &doc); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	mut(&doc)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestValidate_FieldFailures(t *testing.T) {
	cases := []struct {
		name  string
		mut   mutation
		field string
	}{
		{
			name:  "wrong manifest version",
			mut:   func(d *map[string]any) { (*d)["manifestVersion"] = 2 },
			field: "manifestVersion",
		},
		{
			name:  "bad id",
			mut:   func(d *map[string]any) { (*d)["id"] = "Bad_ID" },
			field: "id",
		},
		{
			name:  "empty name",
			mut:   func(d *map[string]any) { (*d)["name"] = "  " },
			field: "name",
		},
		{
			name:  "bad version",
			mut:   func(d *map[string]any) { (*d)["version"] = "1.2" },
			field: "version",
		},
		{
			name: "missing core constraint",
			mut: func(d *map[string]any) {
				(*d)["compatibility"] = map[string]any{"core": ""}
			},
			field: "compatibility.core",
		},
		{
			name: "invalid core constraint",
			mut: func(d *map[string]any) {
				(*d)["compatibility"] = map[string]any{"core": "not a semver"}
			},
			field: "compatibility.core",
		},
		{
			name: "empty platforms",
			mut: func(d *map[string]any) {
				(*d)["platforms"] = []any{}
			},
			field: "platforms",
		},
		{
			name: "bad platform os",
			mut: func(d *map[string]any) {
				(*d)["platforms"] = []any{
					map[string]any{"os": "plan9", "arch": "amd64", "entrypoint": "bin/x", "checksum": "sha256:" + strings.Repeat("0", 64)},
				}
			},
			field: "platforms[0].os",
		},
		{
			name: "duplicate platform",
			mut: func(d *map[string]any) {
				(*d)["platforms"] = []any{
					map[string]any{"os": "linux", "arch": "amd64", "entrypoint": "bin/a", "checksum": "sha256:" + strings.Repeat("0", 64)},
					map[string]any{"os": "linux", "arch": "amd64", "entrypoint": "bin/b", "checksum": "sha256:" + strings.Repeat("1", 64)},
				}
			},
			field: "platforms[1]",
		},
		{
			name: "absolute entrypoint",
			mut: func(d *map[string]any) {
				(*d)["platforms"] = []any{
					map[string]any{"os": "linux", "arch": "amd64", "entrypoint": "/usr/bin/x", "checksum": "sha256:" + strings.Repeat("0", 64)},
				}
			},
			field: "platforms[0].entrypoint",
		},
		{
			name: "bad checksum",
			mut: func(d *map[string]any) {
				(*d)["platforms"] = []any{
					map[string]any{"os": "linux", "arch": "amd64", "entrypoint": "bin/x", "checksum": "md5:abc"},
				}
			},
			field: "platforms[0].checksum",
		},
		{
			name: "duplicate command id",
			mut: func(d *map[string]any) {
				(*d)["commands"] = []any{
					map[string]any{"id": "exec", "permission": "execute"},
					map[string]any{"id": "exec", "permission": "read"},
				}
			},
			field: "commands[1].id",
		},
		{
			name: "unknown command permission",
			mut: func(d *map[string]any) {
				(*d)["commands"] = []any{
					map[string]any{"id": "exec", "permission": "godlike"},
				}
			},
			field: "commands[0].permission",
		},
		{
			name: "duplicate connection type",
			mut: func(d *map[string]any) {
				(*d)["connectionTypes"] = []any{"ssh", "ssh"}
			},
			field: "connectionTypes[1]",
		},
		{
			name: "unknown permission at top level",
			mut: func(d *map[string]any) {
				(*d)["permissions"] = []any{"bogus"}
			},
			field: "permissions[0]",
		},
		{
			name: "recipe path escapes package",
			mut: func(d *map[string]any) {
				(*d)["recipes"] = []any{
					map[string]any{"id": "escape", "path": "../etc/passwd"},
				}
			},
			field: "recipes[0].path",
		},
		{
			name: "duplicate recipe id",
			mut: func(d *map[string]any) {
				(*d)["recipes"] = []any{
					map[string]any{"id": "dup", "path": "a.yaml"},
					map[string]any{"id": "dup", "path": "b.yaml"},
				}
			},
			field: "recipes[1].id",
		},
		{
			name: "duplicate recipe path",
			mut: func(d *map[string]any) {
				(*d)["recipes"] = []any{
					map[string]any{"id": "a", "path": "same.yaml"},
					map[string]any{"id": "b", "path": "same.yaml"},
				}
			},
			field: "recipes[1].path",
		},
		{
			name: "migration to <= from",
			mut: func(d *map[string]any) {
				(*d)["migrations"] = []any{
					map[string]any{"from": 2, "to": 1},
				}
			},
			field: "migrations[0].to",
		},
		{
			name: "unknown signature algorithm",
			mut: func(d *map[string]any) {
				(*d)["signature"] = map[string]any{"algorithm": "rot13", "value": "x"}
			},
			field: "signature.algorithm",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			raw := mutated(t, tc.mut)
			_, err := manifest.Parse(raw)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			assertErrCode(t, err, manifest.ErrCodeManifestInvalid)
			assertField(t, err, tc.field)
		})
	}
}

func TestLoad_FromDirectoryAndFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, manifest.ManifestFileName)
	if err := os.WriteFile(p, []byte(validManifest), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 传目录
	if _, err := manifest.Load(dir); err != nil {
		t.Fatalf("load dir: %v", err)
	}
	// 传文件
	if _, err := manifest.Load(p); err != nil {
		t.Fatalf("load file: %v", err)
	}
}

func TestLoad_Missing(t *testing.T) {
	_, err := manifest.Load(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	assertErrCode(t, err, manifest.ErrCodeManifestInvalid)
}

func TestLoad_EmptyPath(t *testing.T) {
	_, err := manifest.Load("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	assertErrCode(t, err, manifest.ErrCodeManifestInvalid)
}

func TestMatchMetadata(t *testing.T) {
	m, err := manifest.Parse([]byte(validManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := m.MatchMetadata(sdk.Metadata{ID: "ssh", Version: "0.5.0"}); err != nil {
		t.Fatalf("expected match, got: %v", err)
	}
	err = m.MatchMetadata(sdk.Metadata{ID: "ftp", Version: "0.5.0"})
	if err == nil {
		t.Fatal("expected id mismatch")
	}
	assertErrCode(t, err, manifest.ErrCodeManifestMismatch)
	assertField(t, err, "id")

	err = m.MatchMetadata(sdk.Metadata{ID: "ssh", Version: "0.4.9"})
	if err == nil {
		t.Fatal("expected version mismatch")
	}
	assertErrCode(t, err, manifest.ErrCodeManifestMismatch)
	assertField(t, err, "version")
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func assertErrCode(t *testing.T, err error, code string) {
	t.Helper()
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("error is not *sdk.Error: %T (%v)", err, err)
	}
	if se.Code != code {
		t.Fatalf("error code = %q, want %q (message=%s)", se.Code, code, se.Message)
	}
}

func assertField(t *testing.T, err error, want string) {
	t.Helper()
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("error is not *sdk.Error: %T", err)
	}
	got, _ := se.Details["field"].(string)
	if got != want {
		t.Fatalf("Details.field = %q, want %q (message=%s)", got, want, se.Message)
	}
}
