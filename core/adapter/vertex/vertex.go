// Package vertex adapts Google Vertex AI (Gemini) to grudge's Completer and
// Embedder roles over Application Default Credentials — no API key in config.
// It is the fully-hosted counterpart to the self-hosted substrate: point the
// main + embedder roles at "vertex" and grudge runs with zero local model
// infrastructure.
//
// Distinct from the googleai adapter: googleai targets the Gemini *Developer*
// API (generativelanguage.googleapis.com, API-key auth); vertex targets
// Vertex AI (aiplatform, ADC bearer) via the genai SDK's BackendVertexAI.
// Same model family, different provider identity — hence a separate adapter
// (the identity rule: one adapter = one auth model + one endpoint family).
//
// The scorer role is NOT here — Vertex's reranker is the Discovery Engine
// Ranking API, a different endpoint/wire, served by the gcpranking adapter.
//
// Project and location come from ProviderConfig.Options ("project",
// "location") with environment fallback (GOOGLE_CLOUD_PROJECT,
// GOOGLE_CLOUD_LOCATION / GOOGLE_CLOUD_REGION) — the idiomatic ADC shape.
package vertex

import (
	"context"
	"fmt"
	"os"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/genaikit"
	"google.golang.org/genai"
)

type provider struct {
	client *genai.Client
}

// New builds one genai client (BackendVertexAI, ADC) shared by the completer
// and embedder. Project/location resolve from Options then environment.
func New(cfg core.ProviderConfig) (any, error) {
	project := firstNonEmpty(cfg.Option("project"), os.Getenv("GOOGLE_CLOUD_PROJECT"))
	location := firstNonEmpty(
		cfg.Option("location"),
		os.Getenv("GOOGLE_CLOUD_LOCATION"),
		os.Getenv("GOOGLE_CLOUD_REGION"),
	)
	if project == "" {
		return nil, fmt.Errorf("vertex: GCP project required (set Options[\"project\"] or GOOGLE_CLOUD_PROJECT)")
	}
	if location == "" {
		location = "global"
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  project,
		Location: location,
		// Credentials nil → Application Default Credentials.
	})
	if err != nil {
		return nil, fmt.Errorf("vertex: new genai client: %w", err)
	}
	return &provider{client: client}, nil
}

func (p *provider) Completer(model string) (core.Completer, error) {
	return &genaikit.Completer{Client: p.client, Model: model, Name: "vertex"}, nil
}

func (p *provider) Embedder(model string) (core.Embedder, error) {
	return &genaikit.Embedder{Client: p.client, Model: model, Name: "vertex"}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
