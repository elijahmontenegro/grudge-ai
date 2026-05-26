// Package chunk owns the unit of embedding and cross-encoder scoring in
// RRC: a contiguous slice of a message's text. Messages remain the
// graph unit — chunks aggregate back to message-level edges via
// max-score merging at the engine layer. Protocol §6.5 explicitly
// allows message-level or paragraph-level granularity; this package
// implements paragraph-aware chunking so a cross-encoder can focus its
// limited window on the specific sub-section of a long reference
// document that matches a query rather than silently truncating to the
// document's intro.
//
// Both the algorithm (rrc/) and the persistence layer (service/storage)
// import this package without importing each other.
package chunk

// Chunk is a contiguous slice of a message's text.
type Chunk struct {
	Index     int    // position within the message's chunk list (0-based)
	Text      string // the chunk's text
	ByteStart int    // byte offset into the original message text
	ByteEnd   int    // exclusive
	TokenEst  int    // rough estimate, populated via the installed TokenEstimator
}
