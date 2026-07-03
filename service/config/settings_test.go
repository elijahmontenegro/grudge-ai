package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadRejectsUnknownEngineKey pins the DisallowUnknownFields
// contract: any key the engine schema doesn't declare is a config
// error, not something to silently carry.
func TestLoadRejectsUnknownEngineKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	configDir := filepath.Join(root, "config", "grudge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"providers":{},"permissions":{},"mcp_servers":[],"hooks":[],"preferences":{},"engine":{"no_such_knob":10}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown engine key must fail, got %v", err)
	}
}

func TestLoadRejectsIncompleteEngineSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	configDir := filepath.Join(root, "config", "grudge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"providers":{},"permissions":{},"mcp_servers":[],"hooks":[],"preferences":{},"engine":{"local_context_size":10}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("incomplete engine config must fail, got %v", err)
	}
}

func TestFreshSettingsContainCompleteCanonicalEngine(t *testing.T) {
	settings := defaultSettings()
	if err := settings.Engine.Validate(); err != nil {
		t.Fatal(err)
	}
	if settings.Engine.LocalContextSize != 10 || settings.Engine.RerankTopK != 64 {
		t.Fatalf("unexpected canonical defaults: %+v", settings.Engine)
	}
}
