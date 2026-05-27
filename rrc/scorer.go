package rrc

import "context"

// Scorer is the relevance-scoring contract — (query, []candidates) →
// []float64, aligned with input order, higher = more relevant.
//
// The interface duplicates core.Scorer intentionally. rrc/ is a
// standalone library subpackage of spidey: consumers can
// `go get github.com/emontenegr/spidey/rrc` without pulling all of
// core/. Depending on core.Scorer would force consumers to take
// core's broader interface surface (Completer, Embedder, Codec,
// Provider, retry policy) for one method. Go's structural typing
// makes any core.Scorer satisfy rrc.Scorer at the bridge — the
// substrate wires them together with no shim.
type Scorer interface {
	Score(ctx context.Context, query string, candidates []string) ([]float64, error)
}
