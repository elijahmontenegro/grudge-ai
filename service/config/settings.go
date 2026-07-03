package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	net_http "net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/hooks"
	"github.com/elijahmontenegro/grudge/service/mcp"
	"github.com/elijahmontenegro/grudge/service/secrets"
)

// Settings represents the service configuration from config.json.
type Settings struct {
	Providers   map[string]ProviderConfig `json:"providers"`
	Permissions map[string]string         `json:"permissions"`
	MCPServers  []mcp.ServerConfig        `json:"mcp_servers"`
	Hooks       []hooks.HookConfig        `json:"hooks"`
	Preferences map[string]string         `json:"preferences"`
	Engine      EngineConfig              `json:"engine"`
}

// EngineConfig is the explicit settings wire schema for the RRC
// engine's live-tunable knobs. It deliberately mirrors the tunable
// subset of rrc.EngineConfig rather than serializing it directly: the
// wire contract (required keys, DisallowUnknownFields, validation)
// lives here, and construction-time fields on the rrc side (Calibrator,
// Chunk.Estimator) never leak into the settings file. ApplyTo /
// EngineConfigFromRRC are the only conversion points. Zero-value
// Engine means "use the rrc default" — handled at the service
// boundary.
type EngineConfig struct {
	// LossRatio is the live acceptance operating point (the precision
	// stance): a candidate is accepted when its calibrated P(prereq)
	// clears LossRatio (plus the budget's marginal token price). This is
	// the knob that actually gates edge formation and DAG traversal.
	LossRatio float64 `json:"loss_ratio"`

	MinBatchStdDev        float64 `json:"min_batch_stddev"`
	LocalContextSize      int     `json:"local_context_size"`
	RerankTopK            int     `json:"rerank_top_k"`
	ContextBudgetTokens   int     `json:"context_budget_tokens"`
	DiversityLambda       float64 `json:"diversity_lambda"`
	BudgetHeadroomPct     float64 `json:"budget_headroom_pct"`
	PerMsgDelimiterTokens int     `json:"per_msg_delimiter_tokens"`
}

func (e *EngineConfig) UnmarshalJSON(data []byte) error {
	type plain EngineConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded plain
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	// loss_ratio is optional (absent = keep the default stance); every
	// other live knob is required — a partial engine config is a config
	// error, not a request for defaults.
	required := []string{
		"min_batch_stddev", "local_context_size", "rerank_top_k",
		"context_budget_tokens", "diversity_lambda",
		"budget_headroom_pct", "per_msg_delimiter_tokens",
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("engine config missing required field %q", key)
		}
	}
	*e = EngineConfig(decoded)
	return e.Validate()
}

// ApplyTo copies this settings snapshot's live knobs onto base
// (typically rrc.DefaultConfig() or the currently-live engine config)
// and returns it. LossRatio 0 means "unset — keep base's stance".
// The single conversion point from settings to engine config.
func (e EngineConfig) ApplyTo(base rrc.EngineConfig) rrc.EngineConfig {
	if e.LossRatio > 0 {
		base.LossRatio = e.LossRatio
	}
	base.MinBatchStdDev = e.MinBatchStdDev
	base.LocalContextSize = e.LocalContextSize
	base.RerankTopK = e.RerankTopK
	base.ContextBudgetTokens = e.ContextBudgetTokens
	base.DiversityLambda = e.DiversityLambda
	base.BudgetHeadroomPct = e.BudgetHeadroomPct
	base.PerMsgDelimiterTokens = e.PerMsgDelimiterTokens
	return base
}

// EngineConfigFromRRC captures the live engine config's tunable knobs
// as a settings snapshot. The single conversion point from engine
// config to settings.
func EngineConfigFromRRC(ec rrc.EngineConfig) EngineConfig {
	return EngineConfig{
		LossRatio:             ec.LossRatio,
		MinBatchStdDev:        ec.MinBatchStdDev,
		LocalContextSize:      ec.LocalContextSize,
		RerankTopK:            ec.RerankTopK,
		ContextBudgetTokens:   ec.ContextBudgetTokens,
		DiversityLambda:       ec.DiversityLambda,
		BudgetHeadroomPct:     ec.BudgetHeadroomPct,
		PerMsgDelimiterTokens: ec.PerMsgDelimiterTokens,
	}
}

func (e EngineConfig) Validate() error {
	if e.MinBatchStdDev < 0 ||
		e.LocalContextSize <= 0 || e.RerankTopK <= 0 ||
		e.ContextBudgetTokens < 0 ||
		e.DiversityLambda < 0 || e.DiversityLambda > 1 ||
		e.BudgetHeadroomPct < 0 || e.BudgetHeadroomPct > 1 ||
		e.LossRatio < 0 || e.LossRatio > 1 ||
		e.PerMsgDelimiterTokens < 0 {
		return fmt.Errorf("engine config: Local Context size and top-K must be positive; other values cannot be negative; lambda, headroom, and loss_ratio must be in [0,1]")
	}
	return nil
}

// GetUserName returns the configured display name. Priority:
// `preferences.name` (Settings UI writes here) → `$USER` / `$USERNAME`
// (OS login) → literal "User".
func (s *Settings) GetUserName() string {
	if s.Preferences != nil {
		if name := strings.TrimSpace(s.Preferences["name"]); name != "" {
			return name
		}
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	if name := os.Getenv("USERNAME"); name != "" {
		return name
	}
	return "User"
}

// ProviderConfig configures a model role (main, scorer, small_fast).
type ProviderConfig struct {
	Adapter string `json:"adapter"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
	// Options carries provider-specific transport config (e.g. GCP
	// project/location for vertex). Passed through to core.ProviderConfig.
	Options map[string]string `json:"options,omitempty"`
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
		Options: p.Options,
	}
}

// Paths holds resolved XDG base directories.
type Paths struct {
	ConfigDir string // $XDG_CONFIG_HOME/grudge/
	DataDir   string // $XDG_DATA_HOME/grudge/
	CacheDir  string // $XDG_CACHE_HOME/grudge/
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

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg.Settings); err != nil {
		return nil, err
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		return nil, err
	}
	if _, ok := topLevel["engine"]; !ok {
		return nil, fmt.Errorf("settings missing required field %q", "engine")
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
		Providers: make(map[string]ProviderConfig),
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
		Engine: EngineConfig{
			MinBatchStdDev:        0.05,
			LocalContextSize:      10,
			RerankTopK:            64,
			ContextBudgetTokens:   150000,
			DiversityLambda:       0.7,
			BudgetHeadroomPct:     0.90,
			PerMsgDelimiterTokens: 5,
		},
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
	// 1.7B Apache 2.0; uses identical Yes-token logprob recipe as
	// zerank-2 (the larger NC sibling — strong on quality but doesn't
	// fit alongside a 4B embedder on 12GB; see docs/eval-reports/).
	// vLLM exposes an OpenAI-compatible /v1/models endpoint.
	if probeHTTP("http://localhost:8000/v1/models") {
		s.Providers["scorer"] = ProviderConfig{
			Adapter: "zerank",
			Model:   "zeroentropy/zerank-1-small",
			BaseURL: "http://localhost:8000",
		}
	}
	// TEI-served Qwen3-Embedding-0.6B at port 8080 — the production embedder.
	// 0.6B Apache 2.0 instruction-tuned asymmetric embedder. The Instruct/
	// Query prefix is applied client-side by core/adapter/tei — TEI doesn't
	// need per-request prompt support because the prefix is part of the
	// query text.
	if probeHTTP("http://localhost:8080/health") {
		s.Providers["embedder"] = ProviderConfig{
			Adapter: "tei",
			Model:   "Qwen/Qwen3-Embedding-0.6B",
			BaseURL: "http://localhost:8080",
		}
	}
	// SearXNG-served meta-search at port 8888 — the production search
	// provider for the WebSearch tool. Container source at
	// containers/searxng/. Brought up alongside vllm + tei-embed by
	// task substrate:up; the FirstRun probe defaults the config when
	// the service answers.
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
		ConfigDir: filepath.Join(configHome, "grudge"),
		DataDir:   filepath.Join(dataHome, "grudge"),
		CacheDir:  filepath.Join(cacheHome, "grudge"),
	}
}
