package genaikit

import (
	"context"
	"fmt"

	"github.com/elijahmontenegro/grudge/core"
	"google.golang.org/genai"
)

// Embedder implements core.Embedder over a genai client.
type Embedder struct {
	Client *genai.Client
	Model  string
	Name   string
}

// Embed maps the RRC role to the SDK's TaskType (query vs document
// asymmetry). OutputDimensionality is left unset to take the model's
// native dimension; the storage layer (EnsureEmbeddingDim) adapts the
// vector table to whatever comes back.
//
// gemini-embedding-001 accepts a single content per request, so this
// loops per text and preserves order in the returned slice.
func (e *Embedder) Embed(ctx context.Context, role core.EmbedRole, texts []string) ([][]float32, error) {
	taskType := "RETRIEVAL_DOCUMENT"
	if role == core.RoleQuery {
		taskType = "RETRIEVAL_QUERY"
	}
	cfg := &genai.EmbedContentConfig{TaskType: taskType}

	out := make([][]float32, len(texts))
	for i, text := range texts {
		contents := []*genai.Content{{Parts: []*genai.Part{{Text: text}}}}
		resp, err := e.Client.Models.EmbedContent(ctx, e.Model, contents, cfg)
		if err != nil {
			return nil, fmt.Errorf("%s embed: %w", e.Name, err)
		}
		if len(resp.Embeddings) == 0 || resp.Embeddings[0] == nil {
			return nil, fmt.Errorf("%s embed: no embedding returned for input %d", e.Name, i)
		}
		out[i] = resp.Embeddings[0].Values
	}
	return out, nil
}
