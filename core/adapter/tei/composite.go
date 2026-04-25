package tei

import (
	"context"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
)

// CompositeClassifier is the TEI-backed Classifier + companion embedder
// wired to separate TEI endpoints (reranker on one port, embedder on
// another). It exposes both the Classifier surface (Rerank) and the
// underlying embedder so the engine can do cosine-based prefiltering
// without a second adapter.
//
// The "composite" name is historical — under the old NLI+embedding
// design, this struct fused two signals before returning. Now it's a
// thin wrapper that just makes the two services addressable through
// one handle. Max-merge of cosine and reranker scores happens in the
// engine, which owns the chunk-level score cache and the aggregation.
type CompositeClassifier struct {
	reranker *classifier // TEI /rerank
	emb      *embedder   // TEI /embed
}

// NewCompositeClassifier creates the composite. Either URL may be
// empty to disable that signal. Missing reranker → engine falls back
// to cosine-only (weaker but functional); missing embedder → engine
// runs all pairs through the reranker without prefiltering.
func NewCompositeClassifier(rerankURL, embedURL string) *CompositeClassifier {
	cc := &CompositeClassifier{}
	if rerankURL != "" {
		cc.reranker = &classifier{
			baseURL: rerankURL,
			client:  httpc.New(httpc.TimeoutTEI, nil),
		}
	}
	if embedURL != "" {
		cc.emb = &embedder{
			baseURL: embedURL,
			client:  httpc.New(httpc.TimeoutTEI, nil),
		}
	}
	return cc
}

// Embedder returns the underlying embedder so engine / backfill code
// can produce vectors. nil if no embed URL was configured.
func (c *CompositeClassifier) Embedder() core.Embedder {
	if c.emb == nil {
		return nil
	}
	return c.emb
}

// Rerank delegates to the reranker. Without one wired, returns zeros
// so callers don't need to conditionally skip — "no signal" is a
// valid answer per protocol §I13 (zero-return valid).
func (c *CompositeClassifier) Score(ctx context.Context, query string, candidates []string) ([]float64, error) {
	if c.reranker == nil || len(candidates) == 0 {
		return make([]float64, len(candidates)), nil
	}
	return c.reranker.Score(ctx, query, candidates)
}
