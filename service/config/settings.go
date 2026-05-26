package config

import (
	"encoding/json"
	net_http "net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/service/secrets"
)

// Settings represents the service configuration from config.json.
type Settings struct {
	Providers   map[string]ProviderConfig `json:"providers"`
	Permissions map[string]string         `json:"permissions"`
	MCPServers  []MCPServer               `json:"mcp_servers"`
	Hooks       []HookConfig              `json:"hooks"`
	Preferences map[string]string         `json:"preferences"`
	Engine      EngineConfig              `json:"engine"`
	UserName    string                    `json:"user_name,omitempty"`
}

// EngineConfig holds live-tunable RRC engine parameters. Mirrors
// rrc.EngineConfig but lives in the config package to avoid a
// service→rrc cycle at settings-serialization time. Zero-value
// Engine means "use the rrc default" — handled at the service
// boundary.
type EngineConfig struct {
	EdgeThreshold         float64 `json:"edge_threshold"`
	ScoreFloor            float64 `json:"score_floor"`
	ZScoreThreshold       float64 `json:"z_score_threshold"`
	MinBatchStdDev        float64 `json:"min_batch_stddev"`
	RadiusSize            int     `json:"radius_size"`
	RerankTopK            int     `json:"rerank_top_k"`
	ContextBudgetTokens   int     `json:"context_budget_tokens"`
	DiversityLambda       float64 `json:"diversity_lambda"`
	BudgetHeadroomPct     float64 `json:"budget_headroom_pct"`
	PerMsgDelimiterTokens int     `json:"per_msg_delimiter_tokens"`
}

// GetUserName returns the configured display name, in priority:
//  1. `preferences.name` — the field the Settings UI writes to.
//  2. top-level `user_name` — a legacy slot some callers may still set.
//  3. `$USER` / `$USERNAME` — OS login fallback (e.g. "alice" on Windows).
//  4. literal "User" as a last resort.
// Previously the UI-entered name never reached the agent because the
// UI wrote to preferences.name but this function only checked
// s.UserName, falling through to the Windows login on every turn.
func (s *Settings) GetUserName() string {
	if s.Preferences != nil {
		if name := strings.TrimSpace(s.Preferences["name"]); name != "" {
			return name
		}
	}
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
	APIKey  string `json:"api_key,omitempty"`
}

// ToCore converts to a core.ProviderConfig, resolving the API key
// from the secrets store when not set inline. Lets callers say
// `core.NewProvider(cfg.ToCore())` instead of repeating the
// adapter→key lookup at every callsite.
func (p ProviderConfig) ToCore() core.ProviderConfig {
	apiKey := p.APIKey
	if apiKey == "" {
		apiKey, _ = secrets.Get(p.Adapter)
	}
	return core.ProviderConfig{
		Adapter: p.Adapter,
		Model:   p.Model,
		BaseURL: p.BaseURL,
		APIKey:  apiKey,
	}
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
	// vLLM-served zerank-1-small at port 8000 — the production reranker.
	// 1.7B Apache 2.0; uses identical Yes-token logprob recipe as zerank-2
	// (the larger NC sibling that was the prior session's eval winner but
	// didn't fit alongside a 4B embedder on 12GB). vLLM exposes an
	// OpenAI-compatible /v1/models endpoint.
	if probeHTTP("http://localhost:8000/v1/models") {
		s.Providers["classifier"] = ProviderConfig{
			Adapter: "zerank",
			Model:   "zeroentropy/zerank-1-small",
			BaseURL: "http://localhost:8000",
		}
	}
	// TEI-served Qwen3-Embedding-0.6B at port 8080 — the production embedder.
	// 0.6B Apache 2.0 instruction-tuned asymmetric embedder. The Instruct/
	// Query prefix is applied client-side by core/adapter/tei — TEI doesn't
	// need per-request prompt support because the prefix is part of the
	// query text. Replaces the custom services/zembed/ Python service from
	// the prior session.
	if probeHTTP("http://localhost:8080/health") {
		s.Providers["embedder"] = ProviderConfig{
			Adapter: "tei",
			Model:   "Qwen/Qwen3-Embedding-0.6B",
			BaseURL: "http://localhost:8080",
		}
	}
	// SearXNG-served meta-search at port 8888 — the production search
	// provider for the WebSearch tool. Container source at
	// containers/searxng/ (or infra/searxng/ post-Phase-14). Brought up
	// alongside vllm + tei-embed by task substrate:up; the FirstRun
	// probe defaults the config when the service answers.
	if probeHTTP("http://localhost:8888/") {
		s.Providers["search"] = ProviderConfig{
			Adapter: "searxng",
			Model:   "",
			BaseURL: "http://localhost:8888",
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
