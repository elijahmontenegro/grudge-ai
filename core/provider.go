package core

// Provider creates capability instances for a specific backend. Not every
// provider supports all three capabilities — Completer() on a TEI provider
// returns ErrUnsupported, Classifier() on a pure LLM provider returns
// ErrUnsupported. The caller checks.
//
// Provider.ID() and Close() were dropped (Move D-narrow). ID was the
// registry key the caller already knew (and the registry returns the
// concrete provider). Close was vestigial — every adapter returned
// nil; no resource was actually released. Future capability extension
// happens by adding a method here OR (preferred for new optional
// capabilities) by defining a separate interface and using type
// assertion at the call site (cf. ScorerProvider pattern in Move
// D-scorer).
type Provider interface {
	Completer(model string) (Completer, error)
	Embedder(model string) (Embedder, error)
	Classifier(model string) (Classifier, error)
}
