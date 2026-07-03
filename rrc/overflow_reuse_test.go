package rrc

import (
	"context"
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
)

// TestAssemble_PriorSelectionReusesNoReselect confirms A5: an Assemble call
// with PriorSelection set does NOT re-run prerequisite selection — the scorer
// is not called again and no new edges are formed. This is the overflow-retry
// reuse contract: one outbound call owns one selection event; retries only
// re-run shed-to-fit.
func TestAssemble_PriorSelectionReusesNoReselect(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("alpha", "current context", 0.95)
	mc.SetScore("beta", "current context", 0.65)
	o := newMockChunkOracle()
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	cfg.DiversityLambda = 0
	cfg.MinBatchStdDev = 0 // isolate reuse from the batch-flatness gate
	e := NewEngine(cfg, mc, WithChunkOracle(o))
	ctx := context.Background()

	prior1 := addMsg(o, "m1", 0, "t1", "alpha")
	prior2 := addMsg(o, "m2", 1, "t1", "beta")
	anchor := addMsg(o, "q", 2, "t1", "current context")
	corpus := []*threadv1.Message{prior1, prior2, anchor}

	req := AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor),
		Anchor:                 anchor,
		Corpus:                 corpus,
		LocalContext:           []*threadv1.Message{anchor},
		Scope:                  threadv1.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:               "t1",
	}

	// First attempt: full selection runs.
	first, err := e.Assemble(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := mc.callCount
	edgesAfterFirst := len(e.dag.AllEdges())
	if callsAfterFirst == 0 {
		t.Fatal("first assemble should have called the scorer")
	}

	// Retry with the prior selection: must NOT re-select.
	req.PriorSelection = first.Selection
	second, err := e.Assemble(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if mc.callCount != callsAfterFirst {
		t.Fatalf("reuse must not re-call the scorer: calls %d → %d", callsAfterFirst, mc.callCount)
	}
	if got := len(e.dag.AllEdges()); got != edgesAfterFirst {
		t.Fatalf("reuse must not add DAG edges: %d → %d", edgesAfterFirst, got)
	}
	// The reused selection carries the same event id — one selection event.
	if second.Selection.EventId != first.Selection.EventId {
		t.Fatalf("reuse should preserve the selection event id: %q vs %q",
			first.Selection.EventId, second.Selection.EventId)
	}
}

// TestAssemble_PriorSelectionShedsWholeGroups confirms that under reuse, the
// ExcludeIDs path still sheds selected delivery groups (shed-to-fit works on
// the reused selection).
func TestAssemble_PriorSelectionShedsWholeGroups(t *testing.T) {
	mc := newMockScorer()
	// Wide spread so the batch-stddev floor (Gate 3) doesn't fire; both
	// clear the calibrated floor.
	mc.SetScore("alpha", "current context", 0.95)
	mc.SetScore("beta", "current context", 0.65)
	o := newMockChunkOracle()
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	cfg.DiversityLambda = 0
	cfg.MinBatchStdDev = 0 // isolate reuse/shed from the batch-flatness gate
	e := NewEngine(cfg, mc, WithChunkOracle(o))
	ctx := context.Background()

	prior1 := addMsg(o, "m1", 0, "t1", "alpha")
	prior2 := addMsg(o, "m2", 1, "t1", "beta")
	anchor := addMsg(o, "q", 2, "t1", "current context")
	corpus := []*threadv1.Message{prior1, prior2, anchor}

	req := AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor),
		Anchor:                 anchor,
		Corpus:                 corpus,
		LocalContext:           []*threadv1.Message{anchor},
		Scope:                  threadv1.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:               "t1",
	}
	first, err := e.Assemble(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Selection.Selected) < 2 {
		t.Fatalf("expected ≥2 selected to test shedding, got %d", len(first.Selection.Selected))
	}

	// Reuse + exclude one selected message: it must be shed from the wire.
	shedID := first.Selection.Selected[0].MessageId
	req.PriorSelection = first.Selection
	req.ExcludeIDs = []string{shedID}
	second, err := e.Assemble(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range second.Wire {
		// The shed message's text must not appear as a selected wire entry.
		// (Local Context is just the anchor here, so shedID is a selected root.)
		if m.Role == threadv1.Role_ROLE_USER && pbtext.TextFromBlocks(m.Content) == "alpha" && shedID == "m1" {
			t.Fatalf("shed message %s still present in wire after reuse+exclude", shedID)
		}
	}
}
