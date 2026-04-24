package core

import "context"

// Classifier scores candidates against a query. This replaces the
// pair-based NLI interface — bge-reranker-v2-m3 (our new cross-encoder)
// outputs a single relevance score per (query, candidate), which maps
// directly to the "is this a prerequisite?" question RRC needs to ask.
// NLI-entailment was the wrong tool: reference material doesn't
// *entail* the content that consults it, but a reranker trained for
// relevance captures the "informs" relationship cleanly.
type Classifier interface {
	// Rerank scores each candidate against query. Returns one score
	// per candidate, aligned with the input order. Scores are in
	// [0, 1] for sigmoid-output rerankers; semantics depend on the
	// model but "higher = more relevant" is universal.
	//
	// A single query+[]candidates call is one HTTP round-trip to the
	// reranker, so callers should prefer batching into this shape
	// (one new message against many priors) over pairwise loops.
	Rerank(ctx context.Context, query string, candidates []string) ([]float64, error)
}
