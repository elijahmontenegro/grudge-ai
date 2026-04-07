package core

import "context"

// Embedder produces vector embeddings from text. Batch failure is all-or-nothing:
// any item fails → entire batch fails. Implementations must be safe for concurrent use.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}
