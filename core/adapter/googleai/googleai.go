// Package googleai adapts the Gemini Developer API (API-key auth,
// generativelanguage.googleapis.com) to grudge's Completer and
// Embedder roles.
//
// Distinct from the vertex adapter: vertex targets Vertex AI
// (aiplatform, ADC bearer) via BackendVertexAI; googleai targets the
// Gemini Developer API via BackendGeminiAPI. Same model family,
// different provider identity (the identity rule: one adapter = one
// auth model + one endpoint family) — but the same genai SDK serves
// both, so the request codec, response decode, streaming, and
// counting projection are shared via core/adapter/internal/genaikit.
// This file is only client construction and auth.
package googleai

import (
	"context"
	"fmt"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/genaikit"
	"google.golang.org/genai"
)

type provider struct {
	client *genai.Client
}

// New builds one genai client (BackendGeminiAPI, API-key auth) shared
// by the completer and embedder. BaseURL overrides the SDK's default
// endpoint when set — used for pointing at a test server; empty
// APIKey passes through to the SDK's own GOOGLE_API_KEY/GEMINI_API_KEY
// environment fallback (which fails fast if neither resolves).
func New(cfg core.ProviderConfig) (any, error) {
	genaiCfg := &genai.ClientConfig{
		Backend: genai.BackendGeminiAPI,
		APIKey:  cfg.APIKey,
	}
	if cfg.BaseURL != "" {
		genaiCfg.HTTPOptions = genai.HTTPOptions{BaseURL: cfg.BaseURL}
	}
	client, err := genai.NewClient(context.Background(), genaiCfg)
	if err != nil {
		return nil, fmt.Errorf("googleai: new genai client: %w", err)
	}
	return &provider{client: client}, nil
}

func (p *provider) Completer(model string) (core.Completer, error) {
	return &genaikit.Completer{Client: p.client, Model: model, Name: "googleai"}, nil
}

func (p *provider) Embedder(model string) (core.Embedder, error) {
	return &genaikit.Embedder{Client: p.client, Model: model, Name: "googleai"}, nil
}
