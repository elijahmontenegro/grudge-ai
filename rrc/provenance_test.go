package rrc

import (
	"context"
	"fmt"
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
	if _, _, err := e.selectViaFixture(ctx, msgs[1]); err != nil {
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

	addMsg(o, "root", 0, "t1", "root text")   // registered as a candidate + provenance target
	addMsg(o, "noise", 1, "t1", "noise text") // registered as a candidate
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

	edges, tel, err := e.SelectPrerequisites(ctx, local, anchor, threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
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

// TestProvenanceWindowTailBridgesFreshTurn pins the walk's entry
// mechanics: a fresh turn's trigger has NO incoming provenance edges
// (they are recorded contributor → anchor at generation time, i.e.
// after), so a trigger-only anchor set finds nothing — and the WINDOW
// TAIL (the delivered preceding turn, seeded via the serialized
// membership) is what bridges the walk into the recorded graph. This is
// the invariant whose silent violation killed both live mass recall at
// every trigger call and the entire mass calibration (replay
// reconstructs trigger-call windows). Seeds are the delivered set: they
// never surface in the walk's own output — what surfaces is their
// undelivered ancestry, at its banked mass.
func TestProvenanceWindowTailBridgesFreshTurn(t *testing.T) {
	mk := func(from, to string, w float32) *rrcv1.Edge {
		return &rrcv1.Edge{
			FromMessageId: from, ToMessageId: to, Score: w,
			Source:       rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE,
			FromThreadId: "t1", ToThreadId: "t1",
		}
	}
	// History: root fed the prior turn's anchor (recorded at that turn's
	// generation). The fresh trigger has no incoming edges — by construction.
	edges := []*rrcv1.Edge{
		mk("root", "prior-anchor", 0.8),
		mk("prior-trigger", "prior-anchor", 1.0),
	}

	// Trigger-only seeding (a window with no tail — the thread's first
	// turn): the walk finds nothing.
	mass, _ := ProvenanceMass(edges, []string{"trigger"}, "t1",
		threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS)
	if len(mass) != 0 {
		t.Fatalf("trigger-only walk must find nothing (no incoming edges exist), got %v", mass)
	}

	// The full window (current turn ∪ preceding turn): the walk enters
	// through the tail and finds the root's banked mass.
	window := []string{"trigger", "prior-trigger", "prior-anchor"}
	mass, truncated := ProvenanceMass(edges, window, "t1",
		threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS)
	if truncated {
		t.Fatal("tiny graph must not truncate")
	}
	if got := mass["root"]; got < 0.799 || got > 0.801 {
		t.Fatalf("mass[root] = %v, want 0.8 (banked contribution through the window tail)", got)
	}
	// Delivered seeds never surface from their own seeding.
	for _, id := range window {
		if _, ok := mass[id]; ok {
			t.Fatalf("window member %q surfaced in the walk's own output: %v", id, mass)
		}
	}
}

// TestProvenanceMass_DiamondAccumulatesBeforePropagating pins the
// path-sum semantics against the order-sensitivity defect the
// adversarial review found: a node reachable through multiple paths
// must propagate its FULL accumulated mass, not whichever single
// path's partial happened to pop first — and the result must be
// identical for every edge ordering.
func TestProvenanceMass_DiamondAccumulatesBeforePropagating(t *testing.T) {
	mk := func(from, to string, w float32) *rrcv1.Edge {
		return &rrcv1.Edge{
			FromMessageId: from, ToMessageId: to, Score: w,
			Source:       rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE,
			FromThreadId: "t1", ToThreadId: "t1",
		}
	}
	// Q feeds R twice over: Q→A→C (1.0·0.9) and Q→B→C (1.0·0.1).
	// mass[Q] must be 0.9·1.0 + 0.1·1.0 = 1.0 under every permutation.
	edges := []*rrcv1.Edge{
		mk("a", "cone", 0.9),
		mk("b", "cone", 0.1),
		mk("q", "a", 1.0),
		mk("q", "b", 1.0),
	}
	perms := [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 3, 0, 2}, {2, 0, 3, 1}}
	for _, p := range perms {
		ordered := make([]*rrcv1.Edge, len(edges))
		for i, j := range p {
			ordered[i] = edges[j]
		}
		mass, truncated := ProvenanceMass(ordered, []string{"cone"}, "t1",
			threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS)
		if truncated {
			t.Fatal("tiny graph must not truncate")
		}
		if got := mass["q"]; got < 0.999 || got > 1.001 {
			t.Fatalf("perm %v: diamond mass through q = %v, want 1.0 (path-sum)", p, got)
		}
		if got := mass["a"]; got < 0.899 || got > 0.901 {
			t.Fatalf("perm %v: mass[a] = %v, want 0.9", p, got)
		}
	}
}

// TestProvenanceMass_CapKeepsStrongestPathsNotNearest pins A4-D4: when
// the compute cap bites, truncation keeps the top-K by the law's own
// currency — the strongest recorded path into the anchor set — not the
// hop-nearest K. The old layered BFS filled the cap with 64 weak DIRECT
// contributors and never reached a maximal-weight root one hop deeper:
// an arbitrary WHICH leaking into truth exactly when the cap mattered.
func TestProvenanceMass_CapKeepsStrongestPathsNotNearest(t *testing.T) {
	mk := func(from, to string, w float32) *rrcv1.Edge {
		return &rrcv1.Edge{
			FromMessageId: from, ToMessageId: to, Score: w,
			Source:       rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE,
			FromThreadId: "t1", ToThreadId: "t1",
		}
	}
	var edges []*rrcv1.Edge
	// 66 weak direct contributors — hop-nearest, contribution 0.05 each.
	for i := 0; i < 66; i++ {
		edges = append(edges, mk(fmt.Sprintf("weak-%02d", i), "anchor", 0.05))
	}
	// One maximal-strength TWO-hop chain: root → mid → anchor at 1.0.
	edges = append(edges, mk("mid", "anchor", 1.0), mk("root", "mid", 1.0))

	mass, truncated := ProvenanceMass(edges, []string{"anchor"}, "t1",
		threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS)
	if !truncated {
		t.Fatal("68 reachable > cap: truncation must be reported")
	}
	if got := mass["mid"]; got < 0.999 {
		t.Fatalf("mass[mid] = %v, want 1.0 (strongest direct path must settle first)", got)
	}
	if got := mass["root"]; got < 0.999 {
		t.Fatalf("mass[root] = %v, want 1.0 — the strongest path's root must survive the cap; hop-order truncation would have dropped it", got)
	}
	weak := 0
	for id := range mass {
		if len(id) >= 5 && id[:5] == "weak-" {
			weak++
		}
	}
	if weak != provenanceReachCap-2 {
		t.Fatalf("cap should keep mid + root + %d weakest-path fillers, got %d weak", provenanceReachCap-2, weak)
	}
}
