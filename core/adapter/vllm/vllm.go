package vllm

import (
	"fmt"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/openai"
)

// Config for the vLLM provider. vLLM exposes an OpenAI-compatible API.
// APIKey is optional — empty string means no Authorization header.
type Config struct {
	BaseURL string
	APIKey  string // empty string: adapter sends no Authorization header
}

// New creates a vLLM provider. vLLM implements the OpenAI /v1/ API, so this
// delegates to the OpenAI adapter with vLLM's base URL.
func New(cfg Config) any {
	p := openai.New(openai.Config{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
	})
	return &provider{delegate: p}
}

// provider wraps the underlying OpenAI adapter and re-exports the
// Completer role. Embedder is not delegated — vLLM serves
// completions, not embeddings, so vllm intentionally exposes no
// EmbedderProvider interface (callers' type-assertion fails fast).
type provider struct {
	delegate any
}

func (p *provider) Completer(model string) (core.Completer, error) {
	cp, ok := p.delegate.(core.CompleterProvider)
	if !ok {
		return nil, fmt.Errorf("%w: vllm: embedded adapter is not a CompleterProvider", core.ErrUnsupported)
	}
	return cp.Completer(model)
}
