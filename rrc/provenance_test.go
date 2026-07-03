package rrc

import (
	"context"
	"testing"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// TestRecordProvenance_WritesWeightedEdges confirms RecordProvenance emits
// one EDGE_SOURCE_PROVENANCE edge per contributor (contributor → anchor)
// with the contribution weight in Score, skipping self and duplicates.
func TestRecordProvenance_WritesWeightedEdges(t *testing.T) {
	e := testEngine(newMockScorer(), newMockChunkOracle())
	anchor := makeMsg("anchor", 5, "t1", "current turn")

	edges := e.RecordProvenance(anchor, []Contributor{
		{MessageID: "root", ThreadID: "t1", Weight: 1.0},
		{MessageID: "sel", ThreadID: "t1", Weight: 0.42},
		{MessageID: "anchor", ThreadID: "t1", Weight: 1.0}, // self — skipped
		{MessageID: "root", ThreadID: "t1", Weight: 1.0},   // dup — skipped
		{MessageID: "", ThreadID: "t1", Weight: 1.0},       // empty — skipped
	})

	if len(edges) != 2 {
		t.Fatalf("expected 2 provenance edges (self/dup/empty skipped), got %d", len(edges))
	}
	byFrom := map[string]*rrcv1.Edge{}
	for _, ed := range edges {
		if ed.Source != rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE {
			t.Fatalf("edge %s->%s source=%v, want PROVENANCE", ed.FromMessageId, ed.ToMessageId, ed.Source)
		}
		if ed.ToMessageId != "anchor" {
			t.Fatalf("edge should point to anchor, got to=%s", ed.ToMessageId)
		}
		byFrom[ed.FromMessageId] = ed
	}
	if byFrom["root"].Score != 1.0 {
		t.Fatalf("root weight = %v, want 1.0", byFrom["root"].Score)
	}
	if byFrom["sel"].Score != 0.42 {
		t.Fatalf("sel weight = %v, want 0.42", byFrom["sel"].Score)
	}
}

// TestRecordProvenance_DoesNotCorruptSelection is the isolation guarantee:
// provenance edges live in the same DAG (so A2 traversal can reach them via
// Dependents) but must NOT be walked by the CE-scored prerequisite
// selection. Here a provenance edge from an UNRELATED message to the anchor
// would, if traversed, wrongly appear in selection. It must not.
func TestRecordProvenance_DoesNotCorruptSelection(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("a", "b", 0.8) // a real CE prerequisite: a <- b
	o := newMockChunkOracle()
	e := testEngine(mc, o)
	ctx := context.Background()

	msgs := []*threadv1.Message{
		addMsg(o, "m0", 0, "t1", "a"),
		addMsg(o, "m1", 1, "t1", "b"),
		addMsg(o, "unrelated", 2, "t1", "z"),
	}
	// Form the CE edge m0 <- m1.
	if _, _, err := e.selectViaFixture(ctx, msgs[1], msgs[:1]); err != nil {
		t.Fatal(err)
	}

	// Record a provenance edge from "unrelated" into m1 (the anchor). If
	// extractSubgraph traversed provenance, "unrelated" would surface.
	e.RecordProvenance(msgs[1], []Contributor{{MessageID: "unrelated", ThreadID: "t1", Weight: 1.0}})

	result, err := e.Select("m1", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range result.Selected {
		if s.MessageId == "unrelated" {
			t.Fatalf("provenance edge leaked into CE selection: %v", messageIDsOf(result.Selected))
		}
	}
	// The real CE prerequisite m0 must still be selected — proving the
	// guard skips only provenance, not the CE walk.
	if !selectionContains(result.Selected, "m0") {
		t.Fatalf("CE prerequisite m0 missing from selection: %v", messageIDsOf(result.Selected))
	}
}

// TestProvenanceReach_SurfacesAmputatedRoot is the A2 payoff: a message that
// top-K cosine amputates (ranks outside k) but is provenance-connected to the
// active discourse MUST reach the scorer and be scorable. Without the
// provenance recall path it would never form an edge.
func TestProvenanceReach_SurfacesAmputatedRoot(t *testing.T) {
	mc := newMockScorer()
	// The root is a strong TRUE prerequisite once scored — but its cosine
	// retrieval score is low, so top-K=1 drops it before scoring.
	mc.SetScore("root text", "current context", 0.9)
	mc.SetScore("noise text", "current context", 0.1)

	o := newMockChunkOracle()
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	cfg.MinBatchStdDev = 0
	cfg.RerankTopK = 1 // cosine surfaces only ONE candidate — the amputation
	e := NewEngine(cfg, mc, WithChunkOracle(o))
	ctx := context.Background()

	root := addMsg(o, "root", 0, "t1", "root text")
	noise := addMsg(o, "noise", 1, "t1", "noise text")
	anchor := addMsg(o, "q", 2, "t1", "current context")
	// Cosine ranking: noise (0.5) ranks above root (0.05); with k=1 only
	// noise survives the cosine prefilter.
	o.SetRetrievalScore("noise text", 0.5)
	o.SetRetrievalScore("root text", 0.05)
	o.SetRetrievalScore("current context", 0.9)

	// Record provenance: the anchor's turn was generated from the root
	// (banked earlier, when root was recent and load-bearing).
	e.RecordProvenance(anchor, []Contributor{{MessageID: "root", ThreadID: "t1", Weight: 1.0}})

	local := &SerializedLocalContext{
		EventID:     "sel-q",
		Fingerprint: "fp-q",
		MessageIDs:  []string{"q"},
		Chunks:      []SerializedLocalContextChunk{{Index: 0, Text: "current context"}},
	}
	corpus := []*threadv1.Message{root, noise, anchor}

	edges, tel, err := e.SelectPrerequisites(ctx, local, anchor, corpus, threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if tel.ProvenanceReached == 0 {
		t.Fatal("expected provenance recall to reach and score the amputated root")
	}
	// The root must have formed an edge (score 0.9 ≥ 0.5) — proving it
	// reached the scorer despite being outside top-K cosine.
	var rootEdged bool
	for _, ed := range edges {
		if ed.FromMessageId == "root" && ed.Source == rrcv1.EdgeSource_EDGE_SOURCE_CROSS_ENCODER {
			rootEdged = true
		}
	}
	if !rootEdged {
		t.Fatalf("amputated root did not form a CE edge via provenance recall; edges=%d reached=%d", len(edges), tel.ProvenanceReached)
	}
}

// TestCalibratedAcceptance_MassLiftsLowSimilarityRoot is the A4 payoff: under
// the calibrated acceptance model, a low-similarity candidate that the old
// flat EdgeThreshold=0.60 would have cut IS accepted when structural mass
// lifts its P(prereq) over the loss-ratio floor — the /\ working end to end.
func TestCalibratedAcceptance_MassLiftsLowSimilarityRoot(t *testing.T) {
	cfg := DefaultConfig() // default bootstrap calibrator (sim@0.60, mass on) + LossRatio 0.5

	// Low similarity alone: below the boundary → rejected.
	pSimOnly := cfg.Calibrator.Predict(0.15, 0.0)
	if accept(pSimOnly, cfg.LossRatio, 0, 0) {
		t.Fatalf("low-sim/no-mass should be rejected; P=%.3f", pSimOnly)
	}
	// Same low similarity, but high structural mass → lifted over the floor.
	pWithMass := cfg.Calibrator.Predict(0.15, 1.0)
	if !accept(pWithMass, cfg.LossRatio, 0, 0) {
		t.Fatalf("low-sim + high-mass root should be accepted (the /\\); P=%.3f", pWithMass)
	}
	// The retired flat threshold (0.60) would have cut the raw 0.15
	// score unconditionally — mass had no voice under it.
}

// TestCalibratedAcceptance_AbstainsWhenNothingClears confirms abstention
// (valid zero-return) emerges: with budget slack (μ=0) and no candidate
// clearing the precision floor, acceptance selects nothing rather than
// forcing low-confidence picks.
func TestCalibratedAcceptance_AbstainsWhenNothingClears(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	// A field of weak, structureless candidates — none clears the floor.
	for _, sim := range []float64{0.1, 0.2, 0.3, 0.35} {
		if accept(cfg.Calibrator.Predict(sim, 0.0), cfg.LossRatio, 0, 0) {
			t.Fatalf("weak candidate sim=%.2f should not clear the floor", sim)
		}
	}
}

func messageIDsOf(sel []*rrcv1.SelectedMessage) []string {
	out := make([]string, len(sel))
	for i, s := range sel {
		out[i] = s.MessageId
	}
	return out
}

func selectionContains(sel []*rrcv1.SelectedMessage, id string) bool {
	for _, s := range sel {
		if s.MessageId == id {
			return true
		}
	}
	return false
}
