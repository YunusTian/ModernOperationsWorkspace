package settings

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeSchema 用来在 secret_store_test 里频繁构造 schema。
func makeSchema(t *testing.T, raw string) *Schema {
	t.Helper()
	s, err := Compile(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func TestSplitRemovesSecretLeavesAndKeepsShape(t *testing.T) {
	schema := makeSchema(t, `{
	  "type":"object",
	  "properties":{
	    "port":{"type":"integer"},
	    "api_key":{"type":"string","secret":true},
	    "opts":{
	      "type":"object",
	      "properties":{"token":{"type":"string","secret":true},"model":{"type":"string"}}
	    }
	  }
	}`)
	raw := json.RawMessage(`{"port":22,"api_key":"sk-xxx","opts":{"token":"tk-yyy","model":"m1"}}`)
	clean, secrets, err := Split(schema, raw)
	if err != nil {
		t.Fatal(err)
	}
	// clean 中不应包含 secret
	if strings.Contains(string(clean), "sk-xxx") || strings.Contains(string(clean), "tk-yyy") {
		t.Fatalf("clean leaked secrets: %s", clean)
	}
	if !strings.Contains(string(clean), `"port":22`) || !strings.Contains(string(clean), `"model":"m1"`) {
		t.Fatalf("clean missing non-secret data: %s", clean)
	}
	// secrets 应包含 api_key 与 opts.token
	if !strings.Contains(string(secrets), "sk-xxx") || !strings.Contains(string(secrets), "tk-yyy") {
		t.Fatalf("secrets missing: %s", secrets)
	}
	// secrets 不应包含非 secret
	if strings.Contains(string(secrets), `"port"`) || strings.Contains(string(secrets), `"model"`) {
		t.Fatalf("secrets carried non-secret keys: %s", secrets)
	}
}

func TestSplitArraysOfSecret(t *testing.T) {
	schema := makeSchema(t, `{
	  "type":"object",
	  "properties":{
	    "providers":{
	      "type":"array",
	      "items":{
	        "type":"object",
	        "properties":{
	          "name":{"type":"string"},
	          "options":{"type":"object","properties":{"api_key":{"type":"string","secret":true}}}
	        }
	      }
	    }
	  }
	}`)
	raw := json.RawMessage(`{"providers":[{"name":"openai","options":{"api_key":"sk-a"}},{"name":"local","options":{"api_key":"sk-b"}}]}`)
	clean, secrets, err := Split(schema, raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(clean), "sk-a") || strings.Contains(string(clean), "sk-b") {
		t.Fatalf("clean leaked: %s", clean)
	}
	// secrets 应保留数组形状
	var sec map[string]any
	if err := json.Unmarshal(secrets, &sec); err != nil {
		t.Fatal(err)
	}
	arr, ok := sec["providers"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("expected 2-element secrets.providers: %v", sec)
	}
	// Merge 回来应完全等价（字段顺序无关）
	full, err := Merge(clean, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(t, full, raw) {
		t.Fatalf("merge != original\n got=%s\nwant=%s", full, raw)
	}
}

func TestSplitNoSecrets(t *testing.T) {
	schema := makeSchema(t, `{"type":"object","properties":{"port":{"type":"integer"}}}`)
	raw := json.RawMessage(`{"port":22}`)
	clean, secrets, err := Split(schema, raw)
	if err != nil {
		t.Fatal(err)
	}
	if secrets != nil {
		t.Fatalf("expected no secrets, got %s", secrets)
	}
	if !equalJSON(t, clean, raw) {
		t.Fatalf("clean should equal raw: %s vs %s", clean, raw)
	}
}

func TestSecretStoreCRUD(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin-secrets")
	store := SecretStore{BaseDir: dir}
	// Load 不存在时返回 (nil, false, nil)
	if v, ok, err := store.Load("demo"); v != nil || ok || err != nil {
		t.Fatalf("expected empty load: %v %v %v", v, ok, err)
	}
	// Save + reload
	payload := json.RawMessage(`{"api_key":"sk-x"}`)
	if err := store.Save("demo", payload); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Load("demo")
	if err != nil || !ok {
		t.Fatalf("load after save: %v ok=%v", err, ok)
	}
	if !strings.Contains(string(got), "sk-x") {
		t.Fatalf("payload wrong: %s", got)
	}
	// 文件权限（POSIX 才校验）
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "demo.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("permissions too open: %v", info.Mode())
		}
		// 目录也应 0o700
		if dinfo, err := os.Stat(dir); err == nil {
			if dinfo.Mode().Perm()&0o077 != 0 {
				t.Fatalf("dir permissions too open: %v", dinfo.Mode())
			}
		}
	}
	// Save 空 object 应移除文件
	if err := store.Save("demo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo.json")); !os.IsNotExist(err) {
		t.Fatalf("expected file gone: %v", err)
	}
	// Delete 不存在的 id 是 no-op
	if err := store.Delete("demo"); err != nil {
		t.Fatal(err)
	}
}

func TestSecretStoreRejectsBadID(t *testing.T) {
	store := SecretStore{BaseDir: t.TempDir()}
	for _, id := range []string{"", "../evil", "Bad", "with/slash", "with\\slash"} {
		if err := store.Save(id, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("expected error for id %q", id)
		}
		if _, _, err := store.Load(id); err == nil {
			t.Fatalf("expected load error for id %q", id)
		}
	}
}

func TestMergeSecretsOverridesBase(t *testing.T) {
	base := json.RawMessage(`{"port":22,"api_key":"public"}`)
	secrets := json.RawMessage(`{"api_key":"sk-real"}`)
	merged, err := Merge(base, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), `"api_key":"sk-real"`) {
		t.Fatalf("merge should override: %s", merged)
	}
	if !strings.Contains(string(merged), `"port":22`) {
		t.Fatalf("merge should preserve non-secret: %s", merged)
	}
}

func TestMergeArrayIndexAlignment(t *testing.T) {
	base := json.RawMessage(`{"a":[{"name":"one"},{"name":"two"}]}`)
	secrets := json.RawMessage(`{"a":[null,{"tok":"x"}]}`)
	merged, err := Merge(base, secrets)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(merged, &m); err != nil {
		t.Fatal(err)
	}
	arr := m["a"].([]any)
	if arr[0].(map[string]any)["name"] != "one" {
		t.Fatalf("index 0 lost: %+v", arr[0])
	}
	e1 := arr[1].(map[string]any)
	if e1["name"] != "two" || e1["tok"] != "x" {
		t.Fatalf("index 1 merged wrong: %+v", e1)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func equalJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	as, _ := json.Marshal(av)
	bs, _ := json.Marshal(bv)
	return string(as) == string(bs)
}

// 静态断言 os.FileMode 被引用；避免 goimports 剔除 io/fs 依赖。
var _ fs.FileMode = 0