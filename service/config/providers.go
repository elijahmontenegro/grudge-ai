package config

import (
	"fmt"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/anthropic"
	"github.com/emontenegr/spidey/core/adapter/googleai"
	"github.com/emontenegr/spidey/core/adapter/ollama"
	"github.com/emontenegr/spidey/core/adapter/openai"
	"github.com/emontenegr/spidey/core/adapter/tei"
	"github.com/emontenegr/spidey/core/adapter/vllm"
)

// BuildProvider constructs a core.Provider from a ProviderConfig.
func BuildProvider(cfg ProviderConfig) (core.Provider, error) {
	apiKey, _ := GetAPIKey(cfg.Adapter)

	switch cfg.Adapter {
	case "ollama":
		return ollama.New(ollama.Config{BaseURL: cfg.BaseURL}), nil
	case "anthropic":
		return anthropic.New(anthropic.Config{APIKey: apiKey, BaseURL: cfg.BaseURL}), nil
	case "openai":
		return openai.New(openai.Config{APIKey: apiKey, BaseURL: cfg.BaseURL}), nil
	case "googleai":
		return googleai.New(googleai.Config{APIKey: apiKey}), nil
	case "vllm":
		return vllm.New(vllm.Config{BaseURL: cfg.BaseURL, APIKey: apiKey}), nil
	case "tei":
		return tei.New(tei.Config{BaseURL: cfg.BaseURL}), nil
	default:
		return nil, fmt.Errorf("unknown adapter: %s", cfg.Adapter)
	}
}
