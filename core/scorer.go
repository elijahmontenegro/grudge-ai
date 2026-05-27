package core

import "context"

// Scorer scores candidates against a query. The single contract for
// every reranker / similarity scorer in the system —
// (query, []candidates) → []float64, one score per candidate aligned
// with the input order, higher = more relevant.
//
// A single query+[]candidates call is one round-trip to the model,
// so callers should prefer batching into this shape (one new
// message against many priors) over pairwise loops.
//
// Empty candidates returns an empty slice with no backend call.
type Scorer interface {
	Score(ctx context.Context, query string, candidates []string) ([]float64, error)
}
