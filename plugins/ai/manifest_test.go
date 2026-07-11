package main

import (
	"testing"

	"github.com/mow/mow/sdk/manifest"
)

// TestManifestValidatesAgainstRuntimeMetadata 保证 plugins/ai/plugin.json
// 通过 Manifest.Validate() 且与运行时 Metadata 一致。AI 插件没有连接类型，
// 但保留 commands 一致性检查（含 streaming 标记）。
func TestManifestValidatesAgainstRuntimeMetadata(t *testing.T) {
	m, err := manifest.Load("plugin.json")
	if err != nil {
		t.Fatalf("load plugin.json: %v", err)
	}

	meta := newAIPlugin().Metadata()
	if err := m.MatchMetadata(meta); err != nil {
		t.Fatalf("manifest does not match runtime metadata: %v", err)
	}

	handlers := newAIPlugin().Commands()
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
