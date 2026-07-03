package rrc

// Predicate is a typed AST for filtering chunk retrieval. Backends
// (sqlite-vec today; future vector DBs) lower a Predicate to their
// native filter representation by type-switching on the concrete
// types declared in this file. The interface is sealed via the
// unexported predicateMarker method, so backend type-switches can
// be exhaustive.
//
// New predicate types are added without touching the engine. Each
// backend that wants to support the new type adds a case to its
// compile function; backends that don't return an error or fall
// through to a safe default (typically "no filter").
type Predicate interface {
	predicateMarker()
}

// PredAll matches every chunk. The default for unconstrained retrieval.
type PredAll struct{}

func (PredAll) predicateMarker() {}

// PredThread matches chunks whose owning thread equals ThreadID.
type PredThread struct {
	ThreadID string
}

func (PredThread) predicateMarker() {}

// RetrievalScope is the user-visible retrieval-scope toggle: chunks
// from the current thread only, or chunks from any thread.
type RetrievalScope int

const (
	ScopeThread RetrievalScope = iota
	ScopeAll
)

// PredScope encodes the user-visible retrieval scope: with Scope =
// ScopeThread, matches chunks from CurrentThread; with Scope =
// ScopeAll, matches chunks from any thread. The CurrentThread field
// is informational for ScopeAll and load-bearing for ScopeThread.
type PredScope struct {
	CurrentThread string
	Scope         RetrievalScope
}

func (PredScope) predicateMarker() {}

// PredAnd matches chunks satisfying every child predicate. Empty
// Children is equivalent to PredAll.
type PredAnd struct {
	Children []Predicate
}

func (PredAnd) predicateMarker() {}

// PredOr matches chunks satisfying at least one child predicate.
// Empty Children matches no chunks.
type PredOr struct {
	Children []Predicate
}

func (PredOr) predicateMarker() {}

// PredNot matches chunks that do not satisfy Inner.
type PredNot struct {
	Inner Predicate
}

func (PredNot) predicateMarker() {}

// PredHasMetadata matches chunks whose metadata Key equals Value.
// The metadata key/value space is open — backends decide which keys
// they index. Querying an unknown key yields an empty result rather
// than an error.
type PredHasMetadata struct {
	Key   string
	Value string
}

func (PredHasMetadata) predicateMarker() {}

// PredExcludeMessageIDs rejects chunks whose owning message is already
// present in bounded Local Context.
type PredExcludeMessageIDs struct {
	MessageIDs []string
}

func (PredExcludeMessageIDs) predicateMarker() {}
