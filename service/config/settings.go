package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings represents the service configuration from config.json.
type Settings struct {
	Providers   map[string]ProviderConfig `json:"providers"`
	Permissions map[string]string         `json:"permissions"`
	MCPServers  []MCPServer               `json:"mcp_servers"`
	Hooks       []HookConfig              `json:"hooks"`
	Preferences map[string]string         `json:"preferences"`
}

// ProviderConfig configures a model role (main, classifier, small_fast).
type ProviderConfig struct {
	Adapter string `json:"adapter"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url"`
}

// MCPServer configures an MCP endpoint.
type MCPServer struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}

// HookConfig defines a lifecycle event hook.
type HookConfig struct {
	Event   string `json:"event"`
	Command string `json:"command"`
	Match   string `json:"match"`
	Timeout string `json:"timeout"`
}

// Paths holds resolved XDG base directories.
type Paths struct {
	ConfigDir string // $XDG_CONFIG_HOME/spidey/
	DataDir   string // $XDG_DATA_HOME/spidey/
	CacheDir  string // $XDG_CACHE_HOME/spidey/
}

// Config is the full runtime configuration.
type Config struct {
	Paths
	Settings Settings
}

// Load reads configuration from XDG paths.
func Load() (*Config, error) {
	paths := resolvePaths()

	for _, dir := range []string{paths.ConfigDir, paths.DataDir, paths.CacheDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	cfg := &Config{Paths: paths}

	settingsPath := filepath.Join(paths.ConfigDir, "config.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.Settings = defaultSettings()
			return cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, &cfg.Settings); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save persists the current settings to config.json.
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c.Settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.ConfigDir, "config.json"), data, 0o644)
}

func defaultSettings() Settings {
	return Settings{
		Providers:   make(map[string]ProviderConfig),
		Permissions: map[string]string{
			"FileRead":  "allow",
			"Glob":      "allow",
			"Grep":      "allow",
			"WebSearch": "allow",
			"WebFetch":  "allow",
			"FileEdit":  "ask",
			"FileWrite": "ask",
			"Bash":      "ask",
		},
		Preferences: make(map[string]string),
	}
}

func resolvePaths() Paths {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".config")
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}

	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, _ := os.UserHomeDir()
		cacheHome = filepath.Join(home, ".cache")
	}

	return Paths{
		ConfigDir: filepath.Join(configHome, "spidey"),
		DataDir:   filepath.Join(dataHome, "spidey"),
		CacheDir:  filepath.Join(cacheHome, "spidey"),
	}
}
