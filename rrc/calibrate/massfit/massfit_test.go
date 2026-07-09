package massfit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// charEstimator: deterministic token units for the replay's chunking.
type charEstimator struct{}

func (charEstimator) Estimate(s string) int { return (len(s) + 3) / 4 }

func testChunkCfg() chunk.Config {
	cfg := chunk.DefaultConfig()
	cfg.Estimator = charEstimator{}
	return cfg
}

// markerScorer scores 0.9 for candidates carrying the marker, 0.2
// otherwise — a separable live-scorer stand-in.
type markerScorer struct{}

func (markerScorer) Score(_ context.Context, _ string, candidates []string) ([]float64, error) {
	out := make([]float64, len(candidates))
	for i, c := range candidates {
		if strings.Contains(c, "MAGICROOT") {
			out[i] = 0.9
		} else {
			out[i] = 0.2
		}
	}
	return out, nil
}

// idJudge labels exactly one candidate id a prerequisite.
type idJudge struct{ prereqID string }

func (j idJudge) IsPrerequisite(_ context.Context, _, candidateID string) (bool, error) {
	return candidateID == j.prereqID, nil
}

// flakyJudge fails its first failN calls (a transient cloud hiccup),
// then labels like idJudge. Tracks total call count for retry assertions.
type flakyJudge struct {
	prereqID string
	failN    int
	calls    int
}

func (j *flakyJudge) IsPrerequisite(_ context.Context, _, candidateID string) (bool, error) {
	j.calls++
	if j.calls <= j.failN {
		return false, fmt.Errorf("transient: context deadline exceeded")
	}
	return candidateID == j.prereqID, nil
}

// deadJudge fails every call — systematic breakage.
type deadJudge struct{}

func (deadJudge) IsPrerequisite(_ context.Context, _, _ string) (bool, error) {
	return false, fmt.Errorf("provider endpoint unreachable")
}

func msg(id, threadID, turnID string, role threadv1.Role, text string, at time.Time) *threadv1.Message {
	return &threadv1.Message{
		Id:       id,
		ThreadId: threadID,
		Role:     role,
		TurnId:   turnID,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}},
		},
		CreatedAt: timestamppb.New(at),
	}
}

func provEdge(from, to string, weight float32, at time.Time) *rrcv1.Edge {
	return &rrcv1.Edge{
		FromMessageId: from,
		ToMessageId:   to,
		Score:         weight,
		Source:        rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE,
		DetectedAt:    timestamppb.New(at),
		FromThreadId:  "t1",
		ToThreadId:    "t1",
	}
}

// fixture mirrors the live-run shape: an early turn plants the root,
// a middle turn banks provenance from it (selected-prerequisite
// contributor), a later trigger-only turn is replayed. turn-c's trigger
// has no incoming provenance edges (they are recorded at generation),
// so the mass walk enters the graph through the provenance SPINE —
// turn-b, the immediately preceding turn — through which it finds m0's
// banked mass, while m0 itself stays a candidate.
func fixture() ([]*threadv1.Message, []*rrcv1.Edge) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	corpus := []*threadv1.Message{
		msg("m0", "t1", "turn-0", threadv1.Role_ROLE_USER, "the MAGICROOT fact: overflow wraps at INT_MAX", t0),
		msg("m1", "t1", "turn-0", threadv1.Role_ROLE_ASSISTANT, "noted, the wraparound detail", t0.Add(1*time.Second)),
		msg("m2", "t1", "turn-b", threadv1.Role_ROLE_USER, "name three pasta shapes", t0.Add(10*time.Second)),
		msg("m3", "t1", "turn-b", threadv1.Role_ROLE_ASSISTANT, "penne rigatoni fusilli", t0.Add(11*time.Second)),
		msg("m4", "t1", "turn-c", threadv1.Role_ROLE_USER, "remind me of that overflow case", t0.Add(20*time.Second)),
	}
	edges := []*rrcv1.Edge{
		// turn-0's generation provenance: its trigger fed its answer.
		provEdge("m0", "m1", 1.0, t0.Add(2*time.Second)),
		// turn-b's generation provenance: local trigger + the selected
		// prerequisite m0 at its calibrated P.
		provEdge("m2", "m3", 1.0, t0.Add(12*time.Second)),
		provEdge("m0", "m3", 0.8, t0.Add(12*time.Second)),
	}
	return corpus, edges
}

func TestReplay_LabelsMassAndContrastPairs(t *testing.T) {
	corpus, edges := fixture()

	samples, stats, err := Replay(context.Background(), corpus, edges, markerScorer{}, idJudge{prereqID: "m0"}, testChunkCfg())
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if stats.MassPairs == 0 || stats.ContrastPairs == 0 {
		t.Fatalf("expected both mass and contrast pairs, got %+v", stats)
	}

	var rootSampled, contrastSampled bool
	for _, s := range samples {
		if s.Mass > 0 && s.IsPrereq {
			// The banked root: reachable only via provenance (mass),
			// scored high by the live scorer, labeled true by the judge.
			if s.Sim < 0.8 {
				t.Fatalf("mass-bearing prerequisite should score high under the live scorer: %+v", s)
			}
			rootSampled = true
		}
		if s.Mass == 0 && !s.IsPrereq {
			contrastSampled = true
		}
	}
	if !rootSampled {
		t.Fatalf("the provenance-banked root never became a labeled mass sample: %+v / %+v", samples, stats)
	}
	if !contrastSampled {
		t.Fatalf("no zero-mass contrast sample was labeled: %+v", samples)
	}
}

func TestReplay_Deterministic(t *testing.T) {
	corpus, edges := fixture()
	a, _, err := Replay(context.Background(), corpus, edges, markerScorer{}, idJudge{prereqID: "m0"}, testChunkCfg())
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Replay(context.Background(), corpus, edges, markerScorer{}, idJudge{prereqID: "m0"}, testChunkCfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("replay not deterministic: %d vs %d samples", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("replay not deterministic at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestReplay_ErrorsWithoutReachableMass(t *testing.T) {
	corpus, _ := fixture()
	// Edges exist nowhere near the cones: structure count could arm the
	// caller, but nothing is reachable — that must surface, not fit B
	// from nothing.
	stray := []*rrcv1.Edge{provEdge("x1", "x2", 1.0, time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC))}
	_, _, err := Replay(context.Background(), corpus, stray, markerScorer{}, idJudge{prereqID: "m0"}, testChunkCfg())
	// It must surface as the typed cold-start deferral, not fit B from nothing
	// and not a bare error the caller would log as a failure.
	if !errors.Is(err, ErrCorpusTooYoung) {
		t.Fatalf("replay with no reachable mass must return ErrCorpusTooYoung, got %v", err)
	}
}

func TestCorpusProvider_ResolvesTurnAndCandidate(t *testing.T) {
	corpus, _ := fixture()
	p := NewCorpusProvider(corpus)
	tc, err := p.Resolve("turn-b", "m0")
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.LocalContext) != 2 || tc.Candidate.Id != "m0" {
		t.Fatalf("unexpected resolution: %+v", tc)
	}
	if _, err := p.Resolve("no-such-turn", "m0"); err == nil {
		t.Fatal("unknown turn must error")
	}
	if _, err := p.Resolve("turn-b", "no-such-msg"); err == nil {
		t.Fatal("unknown candidate must error")
	}
}

// TestReplay_ToleratesTransientJudgeFailures: a judge that fails a couple
// of early calls (a cloud rate-limit / momentary timeout) must not abort
// the replay — retryTransient re-issues the call and the fit proceeds on
// the full sample set, with no drops recorded.
func TestReplay_ToleratesTransientJudgeFailures(t *testing.T) {
	corpus, edges := fixture()
	// Fail the first 2 calls; retryTransient (3 attempts) clears them.
	j := &flakyJudge{prereqID: "m0", failN: 2}
	samples, stats, err := Replay(context.Background(), corpus, edges, markerScorer{}, j, testChunkCfg())
	if err != nil {
		t.Fatalf("transient failures must not abort the replay: %v", err)
	}
	if stats.Dropped != 0 {
		t.Fatalf("retries should have cleared the transient failures, dropped=%d", stats.Dropped)
	}
	if len(samples) == 0 || stats.MassPairs == 0 {
		t.Fatalf("replay should have produced labeled samples: %+v", stats)
	}
}

// TestReplay_AbortsOnSystematicJudgeFailure: a judge that fails EVERY
// call is systematic breakage, not a hiccup — once drops exceed the
// tolerance the replay aborts loudly rather than fit B on a thinned,
// biased set.
func TestReplay_AbortsOnSystematicJudgeFailure(t *testing.T) {
	corpus, edges := fixture()
	_, stats, err := Replay(context.Background(), corpus, edges, markerScorer{}, deadJudge{}, testChunkCfg())
	if err == nil {
		t.Fatal("a judge that fails every call must abort the replay")
	}
	if !strings.Contains(err.Error(), "infrastructure unstable") {
		t.Fatalf("abort should name the systematic cause, got: %v", err)
	}
	if stats.Dropped == 0 {
		t.Fatalf("systematic failure should record drops, got %d", stats.Dropped)
	}
}
