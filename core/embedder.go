package core

import "context"

// EmbedRole distinguishes query-side encoding from document-side
// encoding. Some embedders are asymmetric — instruction-tuned models
// (zembed-1, Qwen3-Embedding, jina-v5, etc.) prepend a task-specific
// instruction to queries and encode passages plain. Others are
// symmetric (bge-m3 in dense mode) and ignore the role distinction.
// Encoding the role as a typed parameter makes the framework
// express what RRC requires (the role distinction) without
// committing to a specific dispatch shape on the adapter side.
type EmbedRole int

const (
	// RoleQuery is the search target — a brand-new incoming
	// message being scored against the corpus.
	RoleQuery EmbedRole = iota
	// RoleDocument is a corpus entry being indexed.
	RoleDocument
)

// Embedder produces vector embeddings from text. Asymmetric
// implementations dispatch on role internally; symmetric ones
// ignore it. Batch failure is all-or-nothing — any item fails →
// entire batch fails. Implementations must be safe for concurrent
// use.
//
// Engine call sites: an incoming message embeds with RoleQuery
// (the search target); chunks being added to the corpus index
// embed with RoleDocument (the searchables).
type Embedder interface {
	Embed(ctx context.Context, role EmbedRole, texts []string) ([][]float32, error)
}
