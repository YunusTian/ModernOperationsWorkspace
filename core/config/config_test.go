package config

import (
	"encoding/json"
	"os"
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

func TestLoadMigratesV03Config(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
  "app": {"data_dir": "legacy-data", "plugins_dir": "legacy-plugins"},
  "logger": {"level": "debug", "format": "text"},
  "plugins": {"ssh": {"enabled": true, "settings": {"keepalive": 15}}}
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != CurrentVersion {
		t.Fatalf("version=%d", cfg.Version)
	}
	if cfg.App.DataDir != "legacy-data" || !cfg.Plugins["ssh"].Enabled {
		t.Fatalf("legacy fields lost: %+v", cfg)
	}
	var settings map[string]any
	if err = json.Unmarshal(cfg.Plugins["ssh"].Settings, &settings); err != nil || settings["keepalive"] != float64(15) {
		t.Fatalf("settings=%v err=%v", settings, err)
	}
	if len(cfg.AI.AllowedTools) != 0 || cfg.AI.MaxRounds != 0 {
		t.Fatalf("AI defaults must be safe: %+v", cfg.AI)
	}
	if err = Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.Version != CurrentVersion {
		t.Fatalf("reload=%+v err=%v", reloaded, err)
	}
}

func TestLoadRejectsFutureConfigVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected future version rejection")
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
