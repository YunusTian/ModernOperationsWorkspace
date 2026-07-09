package config

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Logger.Level != "info" {
		t.Errorf("want default level info, got %q", cfg.Logger.Level)
	}
	if cfg.Plugins == nil {
		t.Error("Plugins map should be non-nil")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")

	orig := Default()
	orig.Logger.Level = "debug"
	orig.Logger.Format = "text"
	orig.Plugins["ssh"] = PluginConfig{Enabled: true}

	if err := Save(path, orig); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Logger.Level != "debug" || got.Logger.Format != "text" {
		t.Errorf("logger mismatch: %+v", got.Logger)
	}
	if !got.Plugins["ssh"].Enabled {
		t.Error("ssh plugin should be enabled")
	}
}

func TestLoadEmptyPath(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.App.DataDir == "" {
		t.Error("DataDir should be defaulted")
	}
}
