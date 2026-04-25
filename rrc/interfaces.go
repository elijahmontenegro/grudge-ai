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

// Classifier is the Scorer used for surface-relevance reranking
// (bge-reranker-v2-m3 in the production build). Earlier iterations
// used an NLI classifier (pair-wise entailment); that was
// structurally wrong for reference material, which *informs* rather
// than *entails* the content that cites it.
//
// Type alias rather than a separate interface so any Scorer instance
// fits — the consumer expresses intent ("this Scorer is the
// reranker") via field name, not type name.
type Classifier = Scorer

// Entailer is the Scorer used for the optional NLI second stage,
// layered on top of the Classifier per the composite-scoring design.
// Same contract as Classifier; the named alias documents role.
//
// Why NLI on top of reranker: bge scores surface relevance ("this
// content is about the same topic"). That conflates two signals:
//
//   - Content that INFORMS the query (a chapter body referenced for
//     continuation) — desired.
//   - Content that MIRRORS the query's language (the model's own
//     prior meta-thinking: "let me check what chapter we're on") —
//     undesired for prerequisite selection.
//
// NLI scores ENTAILMENT — does premise support hypothesis. That
// distinguishes the two cases.
type Entailer = Scorer
