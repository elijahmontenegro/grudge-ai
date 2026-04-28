package rrc

import "context"

// Scorer scores candidates against a query. Single contract for
// every relevance / entailment / similarity model the engine
// consumes — (query, []candidates) → []float64, aligned with the
// input order, higher = more relevant.
//
// The interface duplicates core.Scorer intentionally so rrc stays a
// standalone library — depending on core would couple the algorithm
// to spidey's adapter layer. Go's structural typing makes any
// core.Scorer satisfy rrc.Scorer at the bridge.
type Scorer interface {
	Score(ctx context.Context, query string, candidates []string) ([]float64, error)
}

// Classifier is the Scorer used for relevance-and-dependency
// scoring. Production wires the composite NLI + embedding-similarity
// classifier from core/adapter/tei; pure bge-reranker satisfies the
// same interface. The single Scorer slot is the whole substrate —
// any directional or entailment fusion happens inside the
// implementation, not as a second stage in the engine.
//
// Type alias rather than a separate interface so any Scorer instance
// fits — the consumer expresses intent ("this Scorer is the
// reranker") via field name, not type name.
type Classifier = Scorer

