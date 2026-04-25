package core

import "context"

// Scorer scores candidates against a query. The single contract for
// every reranker / entailer / similarity classifier in the system —
// (query, []candidates) → []float64, one score per candidate aligned
// with the input order, higher = more relevant. The semantic
// difference between "rerank for surface relevance" and "entail for
// directional dependency" lives in the consumer (which Scorer
// instance is used for what), not in the method shape.
//
// A single query+[]candidates call is one round-trip to the model,
// so callers should prefer batching into this shape (one new
// message against many priors) over pairwise loops.
//
// Empty candidates returns an empty slice with no backend call.
type Scorer interface {
	Score(ctx context.Context, query string, candidates []string) ([]float64, error)
}

// Classifier is a Scorer used for surface-relevance reranking
// (bge-reranker-v2-m3 in production). Kept as a named interface so
// callers and Provider methods can express the role explicitly even
// though it shares Scorer's contract.
type Classifier = Scorer

// Entailer is a Scorer used for NLI-style entailment (DeBERTa-MNLI
// in production). Same contract as Scorer; the named alias documents
// the consumer role.
type Entailer = Scorer
