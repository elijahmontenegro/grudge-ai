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
