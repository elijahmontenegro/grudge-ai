package kernel

import pb "github.com/emontenegr/spidey/proto/gen/go/spidey/v1"

// Selection introspection delegation. Kernel holds a
// *selections.Tracker (see kernel.go); these wrappers preserve the
// resolver-side API while the state + DB write-through live in the
// focused service/selections package.

// RecordSelection persists a selection event in the in-memory
// citation tally and writes it through to the selections table for
// audit. Called by the runner factory's selection callback.
func (k *Kernel) RecordSelection(threadID string, result *pb.SelectionResult) {
	k.Selections.Record(threadID, result)
}

// GetSelection returns the in-memory cached selection for an event
// id. Used by graph.queryResolver.SelectionResult as the hot path
// before falling back to DB lookup.
func (k *Kernel) GetSelection(eventID string) (*pb.SelectionResult, bool) {
	return k.Selections.Get(eventID)
}

// CitationCount returns how many times this message has been
// selected as a prerequisite this session.
func (k *Kernel) CitationCount(messageID string) int {
	return k.Selections.CitationCount(messageID)
}
