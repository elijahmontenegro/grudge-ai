package rrc

import "context"

// Scorer is the relevance-scoring contract — (query, []candidates) →
// []float64, aligned with input order, higher = more relevant.
//
// The interface duplicates core.Scorer intentionally so rrc stays a
// standalone library — depending on core would couple the algorithm
// to spidey's adapter layer. Go's structural typing makes any
// core.Scorer satisfy rrc.Scorer at the bridge.
type Scorer interface {
	Score(ctx context.Context, query string, candidates []string) ([]float64, error)
}

// Classifier is the Scorer used for prerequisite-relevance scoring.
// Production wires bge-reranker-v2-m3 via TEI's /rerank endpoint.
// The engine consumes a single Scorer; any future signal fusion
// lives inside the Scorer implementation, not as a second stage in
// the engine.
//
// Type alias rather than a separate interface so any Scorer instance
// fits — the consumer expresses intent ("this Scorer is the
// reranker") via field name, not type name.
type Classifier = Scorer
