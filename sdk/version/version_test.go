package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryVersionConsistency(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	b, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(b))
	if Version != want {
		t.Fatalf("sdk version %q != VERSION %q", Version, want)
	}

	for _, name := range []string{"package.json", "package-lock.json"} {
		b, err = os.ReadFile(filepath.Join(root, "apps", "desktop", "frontend", name))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Version string `json:"version"`
		}
		if err = json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if doc.Version != want {
			t.Fatalf("%s version %q != VERSION %q", name, doc.Version, want)
		}
	}
}
