package core

// Provider creates capability instances for a specific backend. Not every
// provider supports all three capabilities — Completer() on a TEI provider
// returns ErrUnsupported, Classifier() on a pure LLM provider returns
// ErrUnsupported. The caller checks.
type Provider interface {
	ID() string
	Completer(model string) (Completer, error)
	Embedder(model string) (Embedder, error)
	Classifier(model string) (Classifier, error)
	Close() error
}
