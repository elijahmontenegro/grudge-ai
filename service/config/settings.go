package config

import (
	"encoding/json"
	net_http "net/http"
	"os"
	"path/filepath"
	"time"
)

// Settings represents the service configuration from config.json.
type Settings struct {
	Providers   map[string]ProviderConfig `json:"providers"`
	Permissions map[string]string         `json:"permissions"`
	MCPServers  []MCPServer               `json:"mcp_servers"`
	Hooks       []HookConfig              `json:"hooks"`
	Preferences map[string]string         `json:"preferences"`
	UserName    string                    `json:"user_name,omitempty"`
}

// GetUserName returns the configured user name, falling back to OS user.
func (s *Settings) GetUserName() string {
	if s.UserName != "" {
		return s.UserName
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	if name := os.Getenv("USERNAME"); name != "" {
		return name
	}
	return "User"
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
	s := Settings{
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
	// Auto-detect local providers on first run
	probeProviders(&s)
	return s
}

// probeProviders checks localhost for common providers and pre-configures them.
func probeProviders(s *Settings) {
	// Ollama at default port
	if probeHTTP("http://localhost:11434/api/tags") {
		s.Providers["main"] = ProviderConfig{
			Adapter: "ollama",
			Model:   "",
			BaseURL: "http://localhost:11434",
		}
	}
	// TEI for NLI at default port
	if probeHTTP("http://localhost:8080/info") {
		s.Providers["classifier"] = ProviderConfig{
			Adapter: "tei",
			Model:   "cross-encoder/nli-deberta-v3-base",
			BaseURL: "http://localhost:8080",
		}
	}
	// TEI for embeddings at port 8081
	if probeHTTP("http://localhost:8081/info") {
		s.Providers["embedder"] = ProviderConfig{
			Adapter: "tei",
			Model:   "BAAI/bge-small-en-v1.5",
			BaseURL: "http://localhost:8081",
		}
	}
}

func probeHTTP(url string) bool {
	client := &net_http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
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
