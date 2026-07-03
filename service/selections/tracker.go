// Package selections owns the per-thread selection-introspection
// cache and citation tally. The runner factory's selection callback
// funnels every Retrieval Event through Record; resolvers read from
// the in-memory map (hot path) and fall through to the DB for events
// older than this process.
package selections

import (
	"log"
	"sync"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// Tracker keeps the in-memory selection introspection state and
// write-through audit to the selections table. Construct via New.
type Tracker struct {
	mu        sync.RWMutex
	results   map[string]*pb.SelectionResult
	latest    map[string]string
	citations map[string]int

	db *storage.DB
}

// New constructs an empty Tracker bound to db for write-through audit.
func New(db *storage.DB) *Tracker {
	return &Tracker{
		results:   make(map[string]*pb.SelectionResult),
		latest:    make(map[string]string),
		citations: make(map[string]int),
		db:        db,
	}
}

// Record persists a selection event in the in-memory citation tally
// and writes it through to the selections table for audit. Called by
// the runner factory's selection callback.
func (t *Tracker) Record(threadID string, result *pb.SelectionResult) {
	t.mu.Lock()
	t.results[result.EventId] = result
	t.latest[threadID] = result.EventId
	for _, sel := range result.Selected {
		t.citations[sel.MessageId]++
	}
	t.mu.Unlock()

	if err := t.db.SaveSelection(result, result.AnchorMessageId, threadID); err != nil {
		log.Printf("SaveSelection(event=%s anchor=%s): %v", result.EventId, result.AnchorMessageId, err)
	}
}

// Get returns the in-memory cached selection for an event id. Used
// by graph.queryResolver.SelectionResult as the hot path before
// falling back to DB lookup.
func (t *Tracker) Get(eventID string) (*pb.SelectionResult, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	res, ok := t.results[eventID]
	return res, ok
}

// CitationCount returns how many times this message has been selected
// as a prerequisite since the tracker was constructed (process boot).
func (t *Tracker) CitationCount(messageID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.citations[messageID]
}
