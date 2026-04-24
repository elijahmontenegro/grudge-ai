package rrc

import "context"

// Classifier scores candidates against a query. The engine's only
// external classifier dependency. A reranker model (bge-reranker-v2-m3
// in the production build) outputs one relevance score per candidate
// — the signal RRC actually needs for prerequisite detection. Earlier
// iterations used an NLI classifier (pair-wise entailment); that was
// structurally wrong for reference material, which *informs* rather
// than *entails* the content that cites it.
//
// The interface duplicates core.Classifier intentionally — rrc is
// reusable across deployments that may wire a different Classifier
// implementation, and depending on core.Classifier would require
// rrc to import the `core` package.
type Classifier interface {
	// Rerank scores each candidate against query. Returns one score
	// per candidate, aligned with the input order. Higher = more
	// relevant. Empty candidates returns an empty slice with no
	// backend call.
	Rerank(ctx context.Context, query string, candidates []string) ([]float64, error)
}
