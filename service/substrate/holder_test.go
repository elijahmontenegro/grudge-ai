package substrate

import (
	"context"
	"testing"
	"time"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"github.com/elijahmontenegro/grudge/service/config"
	"github.com/elijahmontenegro/grudge/service/datadir"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// fakeSeedScorer scores the seed set the way a competent reranker would:
// seedfit always places the true prerequisite at candidate index 0, so
// returning a high score there and low elsewhere yields a separable fit.
// Registered through the real provider registry so the holder exercises the
// same construction path as a production scorer.
type fakeSeedScorer struct{}

func (fakeSeedScorer) Score(_ context.Context, _ string, candidates []string) ([]float64, error) {
	out := make([]float64, len(candidates))
	for i := range out {
		if i == 0 {
			out[i] = 0.9
		} else {
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
	cal, ok, err := calibrate.Load(calPath, "fake-reranker-1")
	if err != nil || !ok {
		t.Fatalf("persisted artifact not loadable for scorer: ok=%v err=%v", ok, err)
	}

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
