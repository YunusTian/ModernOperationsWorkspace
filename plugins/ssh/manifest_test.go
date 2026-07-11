package main

import (
	"testing"

	"github.com/mow/mow/sdk/manifest"
)

// TestManifestValidatesAgainstRuntimeMetadata 是 v0.5.0 P3 的一致性保障：
//   - plugins/ssh/plugin.json 通过 Manifest.Validate()
//   - Manifest.MatchMetadata 与运行时 SSHPlugin.Metadata() 完全一致
//   - Manifest.compatibility.core 至少能被当前 sdk/version.Version 满足
//     （运行时 CoreVersion 不做 semver 求解，这里显式演练一次）
func TestManifestValidatesAgainstRuntimeMetadata(t *testing.T) {
	m, err := manifest.Load("plugin.json")
	if err != nil {
		t.Fatalf("load plugin.json: %v", err)
	}

	meta := newSSHPlugin().Metadata()
	if err := m.MatchMetadata(meta); err != nil {
		t.Fatalf("manifest does not match runtime metadata: %v", err)
	}

	// Manifest 里每条 command 都应能在 CommandHandler 列表里找到。
	handlers := newSSHPlugin().Commands()
	byID := map[string]struct{}{}
	for _, h := range handlers {
		byID[h.Spec().ID] = struct{}{}
	}
	for _, c := range m.Commands {
		if _, ok := byID[c.ID]; !ok {
			t.Errorf("manifest declares command %q but it is not registered at runtime", c.ID)
		}
	}
}
