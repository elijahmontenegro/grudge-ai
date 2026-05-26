package core

import "context"

// Scorer scores candidates against a query. The single contract for
// every reranker / similarity classifier in the system —
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

// Classifier is a Scorer used for relevance scoring. Production
// wires bge-reranker-v2-m3 via TEI's /rerank endpoint. Kept as a
// named alias so callers and Provider methods can express the role
// explicitly even though it shares Scorer's contract.
type Classifier = Scorer
