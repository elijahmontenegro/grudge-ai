package seedfit

import (
	"context"
	"testing"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
)

// sepScorer scores like a competent reranker on the seed layout: the true
// prerequisite (index 0) high, distractors low.
type sepScorer struct{}

func (sepScorer) Score(_ context.Context, _ string, c []string) ([]float64, error) {
	out := make([]float64, len(c))
	for i := range out {
		if i == 0 {
			out[i] = 0.9
		} else {
			out[i] = 0.1
		}
	}
	return out, nil
}

var _ core.Scorer = sepScorer{}

const miniSeed = `{"categories":{"X":[
  {"id":"1","query":"q1","correct":"a","distractors":["b","c"]},
  {"id":"2","query":"q2","correct":"d","distractors":["e","f"]}
]}}`

// TestFit_PreservesStructuralLiftRatio is the regression guard for the
// mass-less-seed trap: a naive fit returns B=0 (zero gradient) and silently
// disables the /\. The fit must instead carry the prior's boundary-shift
// ratio B/A onto the fitted similarity scale.
func TestFit_PreservesStructuralLiftRatio(t *testing.T) {
	prior := calibrate.Bootstrap(0.60, 12.0, 6.0) // B/A = 0.5

	res, err := Fit(context.Background(), sepScorer{}, []byte(miniSeed), prior)
	if err != nil {
		t.Fatal(err)
	}
	cal := res.Calibrator
	t.Logf("fitted: A=%.3f B=%.3f C=%.3f", cal.A, cal.B, cal.C)

	// The similarity axis was learned from data.
	if cal.A <= 0 {
		t.Fatalf("similarity coefficient not learned: A=%v", cal.A)
	}
	// The boundary-shift ratio survived: B/A ≈ prior's 0.5, so full mass
	// still moves the acceptance boundary by the same similarity distance.
	ratio := cal.B / cal.A
	if ratio < 0.45 || ratio > 0.55 {
		t.Fatalf("structural-lift ratio not preserved: B/A=%.3f (prior 0.5)", ratio)
	}
	// Functionally: the fitted calibrator separates the scorer's own
	// distribution, and mass still lifts a low-sim candidate.
	if p := cal.Predict(0.9, 0); p < 0.7 {
		t.Fatalf("high-sim prerequisite should calibrate high: P=%.3f", p)
	}
	if p := cal.Predict(0.1, 0); p > 0.3 {
		t.Fatalf("low-sim distractor should calibrate low: P=%.3f", p)
	}
	if cal.Predict(0.1, 1.0) <= cal.Predict(0.1, 0) {
		t.Fatal("mass no longer lifts (structural term dead)")
	}

	// The counterfactual the guard exists for: with B zeroed (the naive
	// fit), mass would stop lifting entirely.
	zeroed := cal
	zeroed.B = 0
	if zeroed.Predict(0.1, 1.0) != zeroed.Predict(0.1, 0) {
		t.Fatal("counterfactual broken — test would not catch a zeroed B")
	}
}

// TestFit_ScoringFailureAborts: a scorer error must abort the whole fit
// (fail fast), never thin the training set silently.
func TestFit_ScoringFailureAborts(t *testing.T) {
	if _, err := Fit(context.Background(), failScorer{}, []byte(miniSeed), calibrate.Bootstrap(0.6, 12, 6)); err == nil {
		t.Fatal("scorer failure must abort the fit")
	}
}

type failScorer struct{}

func (failScorer) Score(context.Context, string, []string) ([]float64, error) {
	return nil, context.DeadlineExceeded
}
