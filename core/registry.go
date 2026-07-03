package core

import (
	"encoding/json"
	"fmt"
)

// ProviderConfig is the input shape for NewProvider — describes
// which adapter to instantiate and how to configure it. Minimal
// surface so consumer applications don't need to maintain their own
// switch-on-adapter-name.
type ProviderConfig struct {
	Adapter string `json:"adapter"`            // e.g. "ollama", "openai", "anthropic"
	Model   string `json:"model,omitempty"`    // model name; semantics provider-specific
	BaseURL string `json:"base_url,omitempty"` // override for custom or self-hosted endpoints
	APIKey  string `json:"api_key,omitempty"`  // hosted API authentication

	// Options carries provider-specific transport configuration that does
	// not fit the four common fields above — GCP project/location for the
	// vertex adapter, AWS region/profile for a future bedrock adapter, and
	// so on. Adapters that need it read typed values by key (falling back to
	// environment where idiomatic, e.g. GOOGLE_CLOUD_PROJECT); adapters that
	// don't (openai, ollama) ignore it. Keeps the shared config minimal
	// while scaling to any provider's transport needs without struct bloat.
	Options map[string]string `json:"options,omitempty"`
}

// Option returns the Options value for key, or "" if unset. Convenience for
// adapters reading their provider-specific transport config.
func (c ProviderConfig) Option(key string) string {
	if c.Options == nil {
		return ""
	}
	return c.Options[key]
}

// ProviderFactory constructs a provider from the config. The
// returned value is `any` — callers type-assert against the role
// interfaces (CompleterProvider, EmbedderProvider, ScorerProvider)
// declared in provider.go. Adapters register themselves via
// RegisterProvider(name, factory) in init(); callers invoke
// NewProvider(cfg) to dispatch by adapter name.
type ProviderFactory func(cfg ProviderConfig) (any, error)

var providerRegistry = map[string]ProviderFactory{}

// RegisterProvider associates an adapter name with its constructor.
// Standard pattern is to call this from each adapter's init() so
// importing the adapter side-effects the registration.
func RegisterProvider(name string, factory ProviderFactory) {
	if name == "" || factory == nil {
		return
	}
	providerRegistry[name] = factory
}

// NewProvider dispatches on cfg.Adapter to the registered factory.
// Returns ErrUnsupported wrapped with the adapter name if no factory
// is registered — typically because the adapter's package wasn't
// imported (Go strips unused imports, so the init() never ran).
// The concrete returned value is the adapter's *provider struct;
// callers type-assert it against the role interface they need.
func NewProvider(cfg ProviderConfig) (any, error) {
	if cfg.Adapter == "" {
		return nil, fmt.Errorf("core.NewProvider: empty adapter name")
	}
	factory, ok := providerRegistry[cfg.Adapter]
	if !ok {
		return nil, fmt.Errorf("%w: adapter %q not registered (import the adapter package to enable it)",
			ErrUnsupported, cfg.Adapter)
	}
	return factory(cfg)
}

// NewProviderFromJSON decodes raw JSON into ProviderConfig and
// invokes NewProvider. Convenience for callers that read provider
// config from settings files or HTTP requests.
func NewProviderFromJSON(raw json.RawMessage) (any, error) {
	var cfg ProviderConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("core.NewProviderFromJSON: %w", err)
	}
	return NewProvider(cfg)
}

// RegisteredProviders returns the names of all registered adapters.
// Used by settings UIs to enumerate options.
func RegisteredProviders() []string {
	out := make([]string, 0, len(providerRegistry))
	for name := range providerRegistry {
		out = append(out, name)
	}
	return out
}
