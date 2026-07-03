package vertex

import (
	"context"
	"fmt"

	"github.com/elijahmontenegro/grudge/core"
	"google.golang.org/genai"
)

type embedder struct {
	client *genai.Client
	model  string
}

// Embed maps the RRC role to Vertex's TaskType (query vs document
// asymmetry — the googleai adapter dropped the role; vertex honors it) and
// returns one vector per input in input order. OutputDimensionality is left
// unset to take the model's native dimension; the storage layer
// (EnsureEmbeddingDim) adapts the vector table to whatever comes back.
//
// gemini-embedding-001 accepts a single content per request, so this loops
// per text and preserves order in the returned slice.
func (e *embedder) Embed(ctx context.Context, role core.EmbedRole, texts []string) ([][]float32, error) {
	taskType := "RETRIEVAL_DOCUMENT"
	if role == core.RoleQuery {
		taskType = "RETRIEVAL_QUERY"
	}
	cfg := &genai.EmbedContentConfig{TaskType: taskType}

	out := make([][]float32, len(texts))
	for i, text := range texts {
		contents := []*genai.Content{{Parts: []*genai.Part{{Text: text}}}}
		resp, err := e.client.Models.EmbedContent(ctx, e.model, contents, cfg)
		if err != nil {
			return nil, fmt.Errorf("vertex embed: %w", err)
		}
		if len(resp.Embeddings) == 0 || resp.Embeddings[0] == nil {
			return nil, fmt.Errorf("vertex embed: no embedding returned for input %d", i)
		}
		out[i] = resp.Embeddings[0].Values
	}
	return out, nil
}
