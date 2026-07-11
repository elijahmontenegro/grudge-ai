package calibrate

import (
	"path/filepath"
	"testing"
)

// TestSaveLoad_RoundTrip proves the producer→persist→load contract the
// running system depends on: the Holder's background fit Saves a fitted
// calibrator, substrate.Build Loads it. A calibrator for the same scorer loads and
// predicts identically; one for a different scorer is refused (so a scorer
// swap can't silently mis-gate); an absent file is a normal (false, nil) boot
// path, not an error.
func TestSaveLoad_RoundTrip(t *testing.T) {
	var samples []LabeledSample
	for i := range 100 {
		f := float64(i) / 100.0
		samples = append(samples, LabeledSample{Sim: 0.7 + 0.3*f, Mass: 0, IsPrereq: true})
		samples = append(samples, LabeledSample{Sim: 0.1 * f, Mass: 0, IsPrereq: false})
	}
	cal, err := Fit(samples, FitConfig{L2: 1e-4})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "calibrator.json")
	if err := Save(path, Artifact{
		Calibrator:    *cal,
		ScorerModelID: "zerank-x",
		Samples:       len(samples),
		LogLoss:       cal.LogLoss(samples),
	}); err != nil {
		t.Fatal(err)
	}

	// Same scorer: present + identical prediction, metadata intact.
	got, ok, err := Load(path, "zerank-x")
	if err != nil || !ok {
		t.Fatalf("load for same scorer: ok=%v err=%v", ok, err)
	}
	if got.Calibrator.Predict(0.8, 0) != cal.Predict(0.8, 0) {
		t.Fatal("loaded calibrator predicts differently from the fitted one")
	}
	if got.Samples != len(samples) {
		t.Fatalf("metadata mismatch: %+v", got)
	}

	// Different scorer: refused, no error → caller falls back to bootstrap.
	if _, ok2, err := Load(path, "other-scorer"); err != nil || ok2 {
		t.Fatalf("different scorer must be refused: ok=%v err=%v", ok2, err)
	}

	// Absent file: normal boot path.
	if _, ok3, err := Load(filepath.Join(t.TempDir(), "nope.json"), "zerank-x"); err != nil || ok3 {
		t.Fatalf("absent file must be (false, nil): ok=%v err=%v", ok3, err)
	}
}
