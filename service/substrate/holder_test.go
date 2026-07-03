package substrate

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"github.com/elijahmontenegro/grudge/rrc/calibrate/seedfit"
	"github.com/elijahmontenegro/grudge/service/config"
	"github.com/elijahmontenegro/grudge/service/datadir"
	"github.com/elijahmontenegro/grudge/service/storage"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeSeedScorer scores the seed set the way a competent reranker would:
// the true prerequisite sits at the query-derived rotation index
// (seedfit.PositiveIndex), so scoring that slot high yields a separable
// fit. Replay scoring (single-candidate batches from the mass refit)
// keys on content instead — candidates carrying the fixture marker
// score high — so replay sims are informative rather than positionally
// constant. Registered through the real provider registry so the holder
// exercises the same construction path as a production scorer.
type fakeSeedScorer struct{}

func (fakeSeedScorer) Score(_ context.Context, q string, candidates []string) ([]float64, error) {
	out := make([]float64, len(candidates))
	rot := seedfit.PositiveIndex(q, len(candidates))
	for i, c := range candidates {
		switch {
		case strings.Contains(c, "MAGICROOT"):
			out[i] = 0.9
		case len(candidates) > 1 && i == rot:
			out[i] = 0.9
		default:
			out[i] = 0.1
		}
	}
	return out, nil
}

type fakeScorerProvider struct{}

func (fakeScorerProvider) Scorer(string) (core.Scorer, error) { return fakeSeedScorer{}, nil }

func init() {
	core.RegisterProvider("holdertest-scorer", func(core.ProviderConfig) (any, error) {
		return fakeScorerProvider{}, nil
	})
}

// TestHolder_SelfCalibratesInBackground is the self-service guarantee: a
// substrate booted with a scorer but no fitted calibrator fits one from the
// embedded seed set in the background, persists it, and live-swaps it in —
// zero user action.
func TestHolder_SelfCalibratesInBackground(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{
		Paths: config.Paths{DataDir: dataDir},
		Settings: config.Settings{
			Providers: map[string]config.ProviderConfig{
				"scorer": {Adapter: "holdertest-scorer", Model: "fake-reranker-1"},
			},
		},
	}
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	h := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Boot state: scorer present, no artifact → bootstrap calibrator.
	if h.Current().CalibratorFitted {
		t.Fatal("fresh boot should start on the bootstrap calibrator")
	}

	// The background fit runs, persists, and re-swaps. Poll for the live
	// substrate to carry the fitted calibrator.
	deadline := time.Now().Add(15 * time.Second)
	for !h.Current().CalibratorFitted {
		if time.Now().After(deadline) {
			t.Fatal("self-calibration did not complete: substrate still on bootstrap")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The artifact exists and is bound to this scorer.
	calPath := datadir.CalibratorPath(dataDir)
	art, ok, err := calibrate.Load(calPath, "holdertest-scorer/fake-reranker-1@")
	if err != nil || !ok {
		t.Fatalf("persisted artifact not loadable for scorer: ok=%v err=%v", ok, err)
	}
	cal := art.Calibrator

	// The fit learned the fake scorer's separable distribution: a 0.9-scoring
	// candidate calibrates high, a 0.1-scoring one low.
	if p := cal.Predict(0.9, 0); p < 0.7 {
		t.Fatalf("fitted calibrator should rate sim=0.9 high, got P=%.3f", p)
	}
	if p := cal.Predict(0.1, 0); p > 0.3 {
		t.Fatalf("fitted calibrator should rate sim=0.1 low, got P=%.3f", p)
	}

	// And the LIVE engine is running on it (not the bootstrap): the live
	// config's calibrator matches the persisted fit.
	live := h.Engine().Config().Calibrator
	if live != cal {
		t.Fatalf("live engine not swapped to the fitted calibrator: live=%+v fitted=%+v", live, cal)
	}

	// THE REGRESSION GUARD: the seed set is mass-less, so a naive fit would
	// zero the mass coefficient and silently disable the structural lift —
	// a provenance-connected root at near-zero similarity would stop
	// clearing acceptance the moment auto-calibration ran. The fit must
	// preserve the bootstrap's structural lift instead.
	if live.B <= 0 {
		t.Fatalf("auto-calibration zeroed the mass coefficient (structural lift disabled): %+v", live)
	}
	// And that lift must still function through the fitted A/C: the design's
	// root case (sim=0.15 + full mass — same point the A4 rrc test uses;
	// B/A=0.5 shifts the boundary half a similarity point, so 0.15 clears
	// while sitting far below the ~0.5+ similarity-only boundary) passes,
	// and the same low sim without mass does not.
	if p := live.Predict(0.15, 1.0); p < 0.5 {
		t.Fatalf("fitted calibrator lost the /\\: low-sim+high-mass root no longer clears, P=%.3f", p)
	}
	if p := live.Predict(0.15, 0.0); p >= 0.5 {
		t.Fatalf("fitted calibrator accepts low-sim junk without mass, P=%.3f", p)
	}
}

// TestHolder_RestartLoadsFitDoesNotRefit: a second boot against the same
// data dir loads the persisted fit directly (CalibratorFitted at Bootstrap),
// so calibration is once-per-scorer, not once-per-boot.
func TestHolder_RestartLoadsFitDoesNotRefit(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{
		Paths: config.Paths{DataDir: dataDir},
		Settings: config.Settings{
			Providers: map[string]config.ProviderConfig{
				"scorer": {Adapter: "holdertest-scorer", Model: "fake-reranker-1"},
			},
		},
	}
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// First boot: wait for the background fit.
	h1 := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h1.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap 1: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !h1.Current().CalibratorFitted {
		if time.Now().After(deadline) {
			t.Fatal("first boot never fitted")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Second boot (fresh holder, same data dir): fitted immediately at
	// Bootstrap — no background window on the bootstrap calibrator.
	h2 := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h2.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap 2: %v", err)
	}
	if !h2.Current().CalibratorFitted {
		t.Fatal("restart should load the persisted fit at Bootstrap, not refit")
	}
}

// TestHolder_NoScorerNoCalibration: without a scorer there is nothing to
// calibrate — no artifact appears, no goroutine loops.
func TestHolder_NoScorerNoCalibration(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Paths: config.Paths{DataDir: dataDir}}
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	h := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, ok, _ := calibrate.Load(datadir.CalibratorPath(dataDir), ""); ok {
		t.Fatal("no scorer configured: no calibrator artifact should be produced")
	}
}

// holderTestEstimator: the token estimator is a Holder construction
// dependency (it lands on every built engine's chunk.Config).
type holderTestEstimator struct{}

func (holderTestEstimator) Estimate(s string) int { return len(s)/4 + 1 }

// collapsedScorer returns identical scores for every candidate — a
// scorer whose recipe broke while still returning 200s.
type collapsedScorer struct{}

func (collapsedScorer) Score(_ context.Context, _ string, candidates []string) ([]float64, error) {
	return make([]float64, len(candidates)), nil
}

type collapsedScorerProvider struct{}

func (collapsedScorerProvider) Scorer(string) (core.Scorer, error) { return collapsedScorer{}, nil }

// markerCompleter is the fake judge model: answers YES exactly when the
// judgment prompt contains the marker, NO otherwise — a deterministic
// regenjudge stand-in that still exercises the real judge path.
type markerCompleter struct{}

func (markerCompleter) Complete(_ context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	prompt := ""
	for _, m := range req.Messages {
		prompt += pbtext.TextFromBlocks(m.Content)
	}
	answer := "NO"
	if strings.Contains(prompt, "MAGICROOT") {
		answer = "YES"
	}
	return &llmv1.CompletionResponse{
		Message: &llmv1.LLMMessage{
			Role:    threadv1.Role_ROLE_ASSISTANT,
			Content: pbtext.BlocksFromText(answer),
		},
	}, nil
}

func (markerCompleter) Stream(_ context.Context, _ *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(func(*llmv1.StreamChunk, error) bool) {} // never used by the judge
}

type markerCompleterProvider struct{}

func (markerCompleterProvider) Completer(string) (core.Completer, error) {
	return markerCompleter{}, nil
}

func init() {
	core.RegisterProvider("holdertest-collapsed", func(core.ProviderConfig) (any, error) {
		return collapsedScorerProvider{}, nil
	})
	core.RegisterProvider("holdertest-completer", func(core.ProviderConfig) (any, error) {
		return markerCompleterProvider{}, nil
	})
}

// waitCalibration blocks until the holder's background calibration
// stage (spawned synchronously during buildAndSwap) has completed.
func waitCalibration(t *testing.T, h *Holder) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for h.calibrating.Load() {
		if time.Now().After(deadline) {
			t.Fatal("background calibration stage never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHolder_HealthCheckRefusesPoisonedRefit is the poison-proof loop:
// a healthy persisted artifact + a scorer that has since collapsed. The
// health check detects the broken pairing and attempts a refit, but the
// fit's validity gate refuses a non-discriminating scorer — so the
// persisted artifact survives untouched instead of being overwritten by
// a flat calibrator.
func TestHolder_HealthCheckRefusesPoisonedRefit(t *testing.T) {
	dataDir := t.TempDir()
	calPath := datadir.CalibratorPath(dataDir)
	healthy := calibrate.Artifact{
		Calibrator:    calibrate.Bootstrap(0.5, 5.0, 2.5),
		ScorerModelID: "holdertest-collapsed/fake-collapsed-1@",
		Samples:       155,
		LogLoss:       0.33,
	}
	if err := calibrate.Save(calPath, healthy); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Paths: config.Paths{DataDir: dataDir},
		Settings: config.Settings{
			Providers: map[string]config.ProviderConfig{
				"scorer": {Adapter: "holdertest-collapsed", Model: "fake-collapsed-1"},
			},
		},
	}
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.Current().CalibratorFitted {
		t.Fatal("persisted artifact should load at boot")
	}
	waitCalibration(t, h)

	after, ok, err := calibrate.Load(calPath, "holdertest-collapsed/fake-collapsed-1@")
	if err != nil || !ok {
		t.Fatalf("artifact must survive: ok=%v err=%v", ok, err)
	}
	if after.Calibrator != healthy.Calibrator {
		t.Fatalf("collapsed scorer overwrote the artifact: %+v -> %+v", healthy.Calibrator, after.Calibrator)
	}
}

// TestHolder_HealthyArtifactUntouched: the boot-time health check on a
// healthy scorer/artifact pairing rewrites nothing.
func TestHolder_HealthyArtifactUntouched(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{
		Paths: config.Paths{DataDir: dataDir},
		Settings: config.Settings{
			Providers: map[string]config.ProviderConfig{
				"scorer": {Adapter: "holdertest-scorer", Model: "fake-reranker-1"},
			},
		},
	}
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// First boot fits.
	h1 := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h1.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCalibration(t, h1)
	before, ok, _ := calibrate.Load(datadir.CalibratorPath(dataDir), "holdertest-scorer/fake-reranker-1@")
	if !ok {
		t.Fatal("first boot never persisted a fit")
	}

	// Second boot: loads, health-checks, changes nothing.
	h2 := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h2.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCalibration(t, h2)
	after, ok, _ := calibrate.Load(datadir.CalibratorPath(dataDir), "holdertest-scorer/fake-reranker-1@")
	if !ok || after != before {
		t.Fatalf("healthy pairing must leave the artifact untouched: %+v -> %+v", before, after)
	}
}

// TestHolder_MassRefitFitsBFromCorpus is stage 3 end to end: once a
// fitted artifact exists, the corpus carries enough provenance
// structure, and a completer is configured, the holder replays the
// corpus in the background, labels mass-bearing candidates through the
// real regenjudge path (against the marker completer), union-fits with
// the seed samples, and persists an artifact whose B is empirical —
// with the watermark recorded for the doubling schedule.
func TestHolder_MassRefitFitsBFromCorpus(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{
		Paths: config.Paths{DataDir: dataDir},
		Settings: config.Settings{
			Providers: map[string]config.ProviderConfig{
				"scorer": {Adapter: "holdertest-scorer", Model: "fake-reranker-1"},
				"main":   {Adapter: "holdertest-completer", Model: "fake-judge-1"},
			},
		},
	}
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The massfit fixture shape: root turn, banking turn, trigger turn.
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	mkMsg := func(id, turnID string, role threadv1.Role, text string, at time.Time) *threadv1.Message {
		return &threadv1.Message{
			Id: id, ThreadId: "t1", TurnId: turnID, Role: role,
			Content: []*threadv1.ContentBlock{
				{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}},
			},
			CreatedAt: timestamppb.New(at),
		}
	}
	if err := db.CreateThread(&threadv1.Thread{Id: "t1", Name: "t", CreatedAt: timestamppb.New(base)}); err != nil {
		t.Fatal(err)
	}
	msgs := []*threadv1.Message{
		mkMsg("m0", "turn-0", threadv1.Role_ROLE_USER, "the MAGICROOT fact: overflow wraps at INT_MAX", base),
		mkMsg("m1", "turn-0", threadv1.Role_ROLE_ASSISTANT, "noted, the wraparound detail", base.Add(1*time.Second)),
		mkMsg("m2", "turn-b", threadv1.Role_ROLE_USER, "name three pasta shapes", base.Add(10*time.Second)),
		mkMsg("m3", "turn-b", threadv1.Role_ROLE_ASSISTANT, "penne rigatoni fusilli", base.Add(11*time.Second)),
		mkMsg("m4", "turn-c", threadv1.Role_ROLE_USER, "remind me of that overflow case", base.Add(20*time.Second)),
		mkMsg("m5", "turn-c", threadv1.Role_ROLE_ASSISTANT, "it is the MAGICROOT wraparound at INT_MAX", base.Add(21*time.Second)),
		mkMsg("m6", "turn-d", threadv1.Role_ROLE_USER, "and the MAGICROOT case once more", base.Add(30*time.Second)),
		mkMsg("m7", "turn-d", threadv1.Role_ROLE_ASSISTANT, "still the MAGICROOT wraparound", base.Add(31*time.Second)),
		mkMsg("m8", "turn-e", threadv1.Role_ROLE_USER, "one more time, that overflow thing", base.Add(40*time.Second)),
	}
	for i, m := range msgs {
		m.Position = int64(i)
		if err := db.InsertMessage(m, nil); err != nil {
			t.Fatal(err)
		}
	}
	mkEdge := func(from, to string, w float32, at time.Time) *rrcv1.Edge {
		return &rrcv1.Edge{
			FromMessageId: from, ToMessageId: to, Score: w,
			Source:       rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE,
			DetectedAt:   timestamppb.New(at),
			FromThreadId: "t1", ToThreadId: "t1",
		}
	}
	for _, e := range []*rrcv1.Edge{
		mkEdge("m0", "m1", 1.0, base.Add(2*time.Second)),
		mkEdge("m2", "m3", 1.0, base.Add(12*time.Second)),
		mkEdge("m0", "m3", 0.8, base.Add(12*time.Second)),
		// The root keeps getting selected and banking weight — the
		// fan-in accumulation that separates real roots from one-shot
		// noise like m2.
		mkEdge("m4", "m5", 1.0, base.Add(22*time.Second)),
		mkEdge("m0", "m5", 0.85, base.Add(22*time.Second)),
		mkEdge("m6", "m7", 1.0, base.Add(32*time.Second)),
		mkEdge("m0", "m7", 0.9, base.Add(32*time.Second)),
	} {
		if err := db.InsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	// Filler provenance structure to cross the arming floor. These
	// connect nothing to any cone — arming counts raw structure, the
	// replay decides reachability.
	for i := 0; i < massRefitMinEdges; i++ {
		f := fillerIDs(i)
		if err := db.InsertEdge(mkEdge(f[0], f[1], 1.0, base.Add(-time.Hour))); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHolder(cfg, db, holderTestEstimator{}, nil)
	if err := h.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Stage 1: cold-start seed fit.
	waitCalibration(t, h)
	calPath := datadir.CalibratorPath(dataDir)
	art, ok, _ := calibrate.Load(calPath, "holdertest-scorer/fake-reranker-1@")
	if !ok {
		t.Fatal("seed fit never persisted")
	}
	if art.MassSamples != 0 {
		t.Fatalf("seed fit must not claim mass samples: %+v", art)
	}

	// Next reload: health passes, mass refit arms and runs.
	if err := h.ReloadProviders(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		art, ok, _ = calibrate.Load(calPath, "holdertest-scorer/fake-reranker-1@")
		if ok && art.MassSamples > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mass refit never landed: %+v", art)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if art.ProvenanceEdgesAtFit < massRefitMinEdges {
		t.Fatalf("watermark not recorded: %+v", art)
	}
	if art.Calibrator.A <= 0 {
		t.Fatalf("union fit lost the similarity axis: %+v", art.Calibrator)
	}

	// Idempotence: another reload must NOT re-run the replay — the
	// doubling watermark is not crossed.
	if err := h.ReloadProviders(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCalibration(t, h)
	again, _, _ := calibrate.Load(calPath, "holdertest-scorer/fake-reranker-1@")
	if again != art {
		t.Fatalf("watermark should hold the refit: %+v -> %+v", art, again)
	}
}

// fillerIDs fabricates disconnected edge endpoints for the arming floor.
func fillerIDs(i int) [2]string {
	return [2]string{fmt.Sprintf("filler-a-%d", i), fmt.Sprintf("filler-b-%d", i)}
}
