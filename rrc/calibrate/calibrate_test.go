package calibrate

import (
	"context"
	"strconv"
	"testing"
)

// TestFit_SeparatesAndCalibrates confirms the joint model learns to separate
// true prerequisites from non-prerequisites and drives log-loss down.
func TestFit_SeparatesAndCalibrates(t *testing.T) {
	// Synthetic ground truth: prereq iff sim+mass is high. Includes the
	// decisive ROOT case — low sim, high mass, TRUE — so the fit must learn
	// a real mass coefficient, not just lean on similarity.
	var samples []LabeledSample
	for i := range 200 {
		f := float64(i) / 200.0
		// High-sim prereqs.
		samples = append(samples, LabeledSample{Sim: 0.7 + 0.3*f, Mass: 0.1, IsPrereq: true})
		// Low-sim, high-mass prereqs (roots).
		samples = append(samples, LabeledSample{Sim: 0.05, Mass: 0.7 + 0.3*f, IsPrereq: true})
		// Low-sim, low-mass non-prereqs (spent junk).
		samples = append(samples, LabeledSample{Sim: 0.1 * f, Mass: 0.1 * f, IsPrereq: false})
	}

	cal, err := Fit(samples, FitConfig{L2: 1e-4})
	if err != nil {
		t.Fatal(err)
	}

	// Log-loss should be low (well-separated data).
	if ll := cal.LogLoss(samples); ll > 0.3 {
		t.Fatalf("log-loss too high after fit: %v", ll)
	}

	// The mass coefficient must be meaningfully positive — the fit learned
	// that structure carries prerequisite-ness, not just similarity.
	if cal.B <= 0 {
		t.Fatalf("mass coefficient not positive (structure not learned): b=%v", cal.B)
	}

	// The root case: low similarity, high mass → high P(prereq). This is the
	// behavior the whole structural lift exists for.
	pRoot := cal.Predict(0.05, 0.95)
	if pRoot < 0.7 {
		t.Fatalf("root (low sim, high mass) should calibrate high; got P=%v", pRoot)
	}
	// Spent junk: low sim, low mass → low P.
	pJunk := cal.Predict(0.05, 0.05)
	if pJunk > 0.3 {
		t.Fatalf("junk (low sim, low mass) should calibrate low; got P=%v", pJunk)
	}
	// Obvious prereq: high sim → high P.
	if p := cal.Predict(0.95, 0.1); p < 0.7 {
		t.Fatalf("high-sim prereq should calibrate high; got P=%v", p)
	}
}

func TestFit_Empty(t *testing.T) {
	if _, err := Fit(nil, FitConfig{}); err == nil {
		t.Fatal("expected error on empty samples")
	}
}

func TestPredict_RangeAndMonotonicity(t *testing.T) {
	c := Calibrator{A: 2, B: 3, C: -1}
	for _, tc := range []struct{ sim, mass float64 }{{0, 0}, {1, 1}, {0.5, 0.5}} {
		p := c.Predict(tc.sim, tc.mass)
		if p <= 0 || p >= 1 {
			t.Fatalf("P out of (0,1): %v", p)
		}
	}
	// Monotonic in mass (b>0).
	if c.Predict(0.5, 0.2) >= c.Predict(0.5, 0.8) {
		t.Fatal("P should increase with mass when b>0")
	}
}

// fakeJudge is label-independent by construction: it decides purely from the
// candidate id (a fixed truth set), never reading Sim/Mass — exactly the
// property the real regenerative judge must have.
type fakeJudge struct {
	truePrereqs map[string]bool
	calls       int
}

func (f *fakeJudge) IsPrerequisite(_ context.Context, _ /*turnID*/ string, candidateID string) (bool, error) {
	f.calls++
	return f.truePrereqs[candidateID], nil
}

// TestLabel_PairsVerdictsAndAccountsCoverage confirms the harness pairs judge
// verdicts with the raw signals and tracks included vs excluded coverage (the
// coverage-bias-breaking excluded samples).
func TestLabel_PairsVerdictsAndAccountsCoverage(t *testing.T) {
	judge := &fakeJudge{truePrereqs: map[string]bool{"root": true, "obvious": true}}
	candidates := []CandidateSignals{
		{TurnID: "t1", CandidateID: "obvious", Sim: 0.9, Mass: 0.1, WasIncluded: true},
		{TurnID: "t1", CandidateID: "root", Sim: 0.05, Mass: 0.9, WasIncluded: false}, // explored
		{TurnID: "t1", CandidateID: "junk", Sim: 0.05, Mass: 0.05, WasIncluded: false},
	}

	res, err := Label(context.Background(), judge, candidates, LabelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 3 || res.Considered != 3 {
		t.Fatalf("expected 3 samples/considered, got %d/%d", len(res.Samples), res.Considered)
	}
	if res.IncludedLabeled != 1 || res.ExcludedLabeled != 2 {
		t.Fatalf("coverage accounting wrong: included=%d excluded=%d", res.IncludedLabeled, res.ExcludedLabeled)
	}
	// The root sample must carry its true label AND its low-sim/high-mass
	// signals paired correctly.
	var sawRoot bool
	for _, s := range res.Samples {
		if s.Mass == 0.9 {
			sawRoot = true
			if !s.IsPrereq {
				t.Fatal("root should be labeled true prerequisite")
			}
			if s.Sim != 0.05 {
				t.Fatalf("root sim mispaired: %v", s.Sim)
			}
		}
	}
	if !sawRoot {
		t.Fatal("root sample missing from labeled set")
	}
}

func TestLabel_MaxSamplesTruncates(t *testing.T) {
	judge := &fakeJudge{truePrereqs: map[string]bool{}}
	candidates := []CandidateSignals{
		{TurnID: "t", CandidateID: "a"}, {TurnID: "t", CandidateID: "b"}, {TurnID: "t", CandidateID: "c"},
	}
	res, err := Label(context.Background(), judge, candidates, LabelConfig{MaxSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 2 || !res.Truncated {
		t.Fatalf("expected truncation at 2; got %d truncated=%v", len(res.Samples), res.Truncated)
	}
}

func TestLabel_NilJudge(t *testing.T) {
	if _, err := Label(context.Background(), nil, nil, LabelConfig{}); err == nil {
		t.Fatal("expected error on nil judge")
	}
}

// TestFitFromLabels_EndToEnd exercises the sweep→fit convenience and confirms
// the resulting calibrator ranks the root above junk.
func TestFitFromLabels_EndToEnd(t *testing.T) {
	judge := &fakeJudge{truePrereqs: map[string]bool{}}
	var candidates []CandidateSignals
	for i := range 100 {
		s := strconv.Itoa(i)
		judge.truePrereqs["root"+s] = true
		judge.truePrereqs["obvious"+s] = true
		candidates = append(candidates,
			CandidateSignals{CandidateID: "obvious" + s, Sim: 0.9, Mass: 0.1, WasIncluded: true},
			CandidateSignals{CandidateID: "root" + s, Sim: 0.05, Mass: 0.9, WasIncluded: false},
			CandidateSignals{CandidateID: "junk" + s, Sim: 0.05, Mass: 0.05, WasIncluded: false},
		)
	}
	cal, res, err := FitFromLabels(context.Background(), judge, candidates, LabelConfig{}, FitConfig{L2: 1e-4})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExcludedLabeled == 0 {
		t.Fatal("expected excluded (explored) samples in the training set")
	}
	if cal.Predict(0.05, 0.9) <= cal.Predict(0.05, 0.05) {
		t.Fatal("calibrator must rank root above junk")
	}
}
