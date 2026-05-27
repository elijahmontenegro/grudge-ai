package core

// Provider role interfaces. The framework expresses what each role
// (Completer, Embedder, Scorer) requires from an adapter; an
// adapter implements only the role interfaces matching the
// capabilities it actually exposes. Callers type-assert on the
// concrete provider returned by NewProvider:
//
//	p, err := core.NewProvider(cfg)
//	if cp, ok := p.(core.CompleterProvider); ok {
//	    c, err := cp.Completer(cfg.Model)
//	    ...
//	}
//
// A god-interface combining all three methods used to live here. It
// forced every adapter to stub the methods it didn't implement,
// returning ErrUnsupported. Splitting the interface removes those
// stubs entirely and makes the adapter's capability surface
// observable from its method set, not from a runtime probe.
//
// ErrUnsupported is still defined (errors.go) — it now signals
// "the registered adapter for X is not a Y provider" at the lookup
// boundary rather than from individual method returns.

// CompleterProvider produces Completer instances for a specific model.
type CompleterProvider interface {
	Completer(model string) (Completer, error)
}

// EmbedderProvider produces Embedder instances for a specific model.
type EmbedderProvider interface {
	Embedder(model string) (Embedder, error)
}

// ScorerProvider produces Scorer instances for the cross-encoder
// reranker role. A backend that exposes both embedder and reranker
// (e.g. TEI) implements both role interfaces.
type ScorerProvider interface {
	Scorer(model string) (Scorer, error)
}
