package main

import (
	"testing"

	"github.com/mow/mow/sdk/manifest"
)

// TestManifestValidatesAgainstRuntimeMetadata 保证 plugins/pve/plugin.json
// 通过 Manifest.Validate() 且与运行时 Metadata 一致（id/version 匹配、
// commands 全部对应到 CommandHandler 列表）。
func TestManifestValidatesAgainstRuntimeMetadata(t *testing.T) {
	m, err := manifest.Load("plugin.json")
	if err != nil {
		t.Fatalf("load plugin.json: %v", err)
	}
	meta := newPVEPlugin().Metadata()
	if err := m.MatchMetadata(meta); err != nil {
		t.Fatalf("manifest does not match runtime metadata: %v", err)
	}
	handlers := newPVEPlugin().Commands()
	byID := map[string]struct{}{}
	for _, h := range handlers {
		byID[h.Spec().ID] = struct{}{}
	}
	for _, c := range m.Commands {
		if _, ok := byID[c.ID]; !ok {
			t.Errorf("manifest declares command %q but it is not registered at runtime", c.ID)
		}
	}
	// 反向：所有运行时 command 也必须在 manifest 里声明。
	declared := map[string]struct{}{}
	for _, c := range m.Commands {
		declared[c.ID] = struct{}{}
	}
	for id := range byID {
		if _, ok := declared[id]; !ok {
			t.Errorf("runtime registers command %q but manifest does not declare it", id)
		}
	}
}
