package vllm

import (
	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/openai"
)

// Config for the vLLM provider. vLLM exposes an OpenAI-compatible API.
// APIKey is optional — empty string means no Authorization header.
type Config struct {
	BaseURL string
	APIKey  string // empty string: adapter sends no Authorization header
}

// New creates a vLLM provider. vLLM implements the OpenAI /v1/ API, so this
// delegates to the OpenAI adapter with vLLM's base URL.
func New(cfg Config) core.Provider {
	p := openai.New(openai.Config{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
	})
	return &provider{delegate: p}
}

type provider struct {
	delegate core.Provider
}

func (p *provider) ID() string                                      { return "vllm" }
func (p *provider) Completer(model string) (core.Completer, error)  { return p.delegate.Completer(model) }
func (p *provider) Embedder(_ string) (core.Embedder, error)        { return nil, core.ErrUnsupported }
func (p *provider) Classifier(_ string) (core.Classifier, error)    { return nil, core.ErrUnsupported }
func (p *provider) Close() error                                    { return p.delegate.Close() }
