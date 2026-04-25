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

// Entailer scores candidates for directional dependency on a query
// using an NLI (Natural Language Inference) cross-encoder. Optional
// second scoring stage layered on top of the Classifier (reranker).
//
// Why an NLI stage on top of a reranker: bge-reranker-v2-m3 scores
// surface relevance — "this content is about the same topic." That
// conflates two different signals when Selection needs them split:
//
//   - Content that INFORMS the query (a chapter body referenced for
//     continuation) scores high on relevance. Desired.
//   - Content that MIRRORS the query's language (the model's own
//     prior meta-thinking: "the user wants me to continue, let me
//     check what chapter we're on") also scores high, because it
//     quotes the same phrases. Undesired for prerequisite selection.
//
// NLI scores ENTAILMENT: "does the candidate premise support the
// query hypothesis?" That distinguishes the two — reference material
// entails ongoing-work continuations, process-thinking about
// continuing does not.
//
// Returns one score per hypothesis, aligned with the input order.
// Higher = stronger entailment. Empty hypotheses returns an empty
// slice with no backend call.
type Entailer interface {
	Entail(ctx context.Context, premise string, hypotheses []string) ([]float64, error)
}
