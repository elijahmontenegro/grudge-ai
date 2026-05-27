package kernel

import (
	"log"

	pb "github.com/emontenegr/spidey/proto/gen/go/spidey/v1"
)

// Selection introspection cache + citation tally. The runner factory's
// selection callback funnels every Retrieval Event through
// RecordSelection; resolvers read from the in-memory map (hot path)
// and fall through to the DB for events older than this process.

// RecordSelection persists a selection event in the in-memory
// citation tally and writes it through to the selections table for
// audit. Called by the runner factory's selection callback.
func (k *Kernel) RecordSelection(threadID string, result *pb.SelectionResult) {
	k.selectionMu.Lock()
	k.selectionResults[result.EventId] = result
	k.latestSelection[threadID] = result.EventId
	for _, sel := range result.Selected {
		k.citationCount[sel.MessageId]++
	}
	k.selectionMu.Unlock()

	// Event IDs are synthesized as sel-<target_message_id> in the
	// engine. Strip the prefix to recover the target for the
	// selections table FK.
	targetID := result.EventId
	if len(targetID) > 4 && targetID[:4] == "sel-" {
		targetID = targetID[4:]
	}
	if err := k.DB.SaveSelection(result, targetID, threadID); err != nil {
		log.Printf("SaveSelection(event=%s target=%s): %v", result.EventId, targetID, err)
	}
}

// GetSelection returns the in-memory cached selection for an event
// id. Used by graph.queryResolver.SelectionResult as the hot path
// before falling back to DB lookup.
func (k *Kernel) GetSelection(eventID string) (*pb.SelectionResult, bool) {
	k.selectionMu.RLock()
	defer k.selectionMu.RUnlock()
	res, ok := k.selectionResults[eventID]
	return res, ok
}

// CitationCount returns how many times this message has been
// selected as a prerequisite this session.
func (k *Kernel) CitationCount(messageID string) int {
	k.selectionMu.RLock()
	defer k.selectionMu.RUnlock()
	return k.citationCount[messageID]
}
