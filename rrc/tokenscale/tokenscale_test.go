package tokenscale

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokenscale.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, path
}

// The budget below which a predicted total of `pred` passes the size
// gate: predicted*sizeGateDivisor >= budget.
const gateBudget = 1000 // predictions >= 250 are admitted

func TestObserve_FirstAdmissionSetsScale(t *testing.T) {
	s, _ := open(t)
	b := s.Bound("ollama/qwen3:8b")
	if got := b.Scale(); got != 0 {
		t.Fatalf("ungrounded Scale = %v, want 0", got)
	}
	if err := b.Observe(1000, 1200, gateBudget); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := b.Scale(); got != 1.2 {
		t.Fatalf("Scale = %v, want 1.2", got)
	}
}

func TestObserve_CensoredLowballsNeverLowerScale(t *testing.T) {
	s, _ := open(t)
	b := s.Bound("m")
	if err := b.Observe(1000, 1500, gateBudget); err != nil {
		t.Fatalf("cold observation: %v", err)
	}
	// A KV-cache-hit report: far below truth but inside the band.
	for i := 0; i < 20; i++ {
		if err := b.Observe(1000, 800, gateBudget); err != nil {
			t.Fatalf("censored observation %d: %v", i, err)
		}
	}
	if got := b.Scale(); got != 1.5 {
		t.Fatalf("Scale after censored lowballs = %v, want 1.5 (max must hold)", got)
	}
}

func TestObserve_AnomalyAgesOutOnWindowTurnover(t *testing.T) {
	s, _ := open(t)
	b := s.Bound("m")
	if err := b.Observe(1000, 2900, gateBudget); err != nil {
		t.Fatalf("anomaly: %v", err)
	}
	if got := b.Scale(); got != 2.9 {
		t.Fatalf("Scale = %v, want 2.9", got)
	}
	// window admissions at the true ratio push the anomaly out.
	for i := 0; i < window; i++ {
		if err := b.Observe(1000, 1100, gateBudget); err != nil {
			t.Fatalf("steady observation %d: %v", i, err)
		}
	}
	if got := b.Scale(); got != 1.1 {
		t.Fatalf("Scale after turnover = %v, want 1.1", got)
	}
}

func TestObserve_SizeGateSkipsSmallWires(t *testing.T) {
	s, _ := open(t)
	b := s.Bound("m")
	// predicted*4 < budget → skipped, no state.
	if err := b.Observe(249, 700, gateBudget); err != nil {
		t.Fatalf("below-gate Observe returned error: %v", err)
	}
	if got := b.Scale(); got != 0 {
		t.Fatalf("Scale after below-gate observation = %v, want 0", got)
	}
	// Boundary: predicted*4 == budget → admitted.
	if err := b.Observe(250, 300, gateBudget); err != nil {
		t.Fatalf("at-gate Observe: %v", err)
	}
	if got := b.Scale(); got != 1.2 {
		t.Fatalf("Scale = %v, want 1.2", got)
	}
}

func TestObserve_SanityBandRejectsBothSides(t *testing.T) {
	s, _ := open(t)
	b := s.Bound("m")
	for _, tc := range []struct{ pred, obs int }{
		{1000, 400},  // 0.4 < 0.5
		{1000, 3500}, // 3.5 > 3.0
	} {
		err := b.Observe(tc.pred, tc.obs, gateBudget)
		if !errors.Is(err, ErrRatioOutOfBand) {
			t.Fatalf("Observe(%d, %d) err = %v, want ErrRatioOutOfBand", tc.pred, tc.obs, err)
		}
	}
	if got := b.Scale(); got != 0 {
		t.Fatalf("Scale after rejected observations = %v, want 0", got)
	}
}

func TestObserve_NonPositiveInputsAreNotObservations(t *testing.T) {
	s, _ := open(t)
	b := s.Bound("m")
	for _, tc := range []struct{ pred, obs, budget int }{
		{0, 500, gateBudget},
		{-1, 500, gateBudget},
		{500, 0, gateBudget},
		{500, -1, gateBudget},
		{500, 600, 0},
		{500, 600, -1},
	} {
		if err := b.Observe(tc.pred, tc.obs, tc.budget); err != nil {
			t.Fatalf("Observe(%+v): %v", tc, err)
		}
	}
	if got := b.Scale(); got != 0 {
		t.Fatalf("Scale = %v, want 0", got)
	}
}

func TestPersistence_RoundTrip(t *testing.T) {
	s, path := open(t)
	if err := s.Bound("a/m1").Observe(1000, 1300, gateBudget); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := s.Bound("b/m2").Observe(1000, 900, gateBudget); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	re, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := re.Bound("a/m1").Scale(); got != 1.3 {
		t.Fatalf("a/m1 Scale after reopen = %v, want 1.3", got)
	}
	if got := re.Bound("b/m2").Scale(); got != 0.9 {
		t.Fatalf("b/m2 Scale after reopen = %v, want 0.9", got)
	}
}

func TestOpen_CorruptFileIsRefittableCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokenscale.json")
	if err := os.WriteFile(path, []byte("{torn"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		t.Fatal("Open on corrupt file returned nil error")
	}
	if s == nil {
		t.Fatal("Open on corrupt file returned nil store — must be usable")
	}
	if err := s.Bound("m").Observe(1000, 1200, gateBudget); err != nil {
		t.Fatalf("Observe on recovered store: %v", err)
	}
	re, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after overwrite: %v", err)
	}
	if got := re.Bound("m").Scale(); got != 1.2 {
		t.Fatalf("Scale after recovery = %v, want 1.2", got)
	}
}

func TestOpen_DegenerateEntriesAreRefittable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokenscale.json")
	// Parseable JSON with an out-of-band ratio (hand edit).
	if err := os.WriteFile(path, []byte(`{"models":{"m":{"ratios":[9.5],"samples":1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		t.Fatal("Open on degenerate file returned nil error")
	}
	if s == nil {
		t.Fatal("Open on degenerate file returned nil store")
	}
	if got := s.Bound("m").Scale(); got != 0 {
		t.Fatalf("Scale from degenerate artifact = %v, want 0 (entry refused)", got)
	}
}

func TestBound_EmptyKeyIsNil(t *testing.T) {
	s, _ := open(t)
	if b := s.Bound(""); b != nil {
		t.Fatalf("Bound(\"\") = %v, want nil", b)
	}
}

func TestObserve_ConcurrentAdmissions(t *testing.T) {
	s, path := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			b := s.Bound("m")
			for j := 0; j < 25; j++ {
				if err := b.Observe(1000, 1000+n*10, gateBudget); err != nil {
					t.Errorf("Observe: %v", err)
				}
				_ = b.Scale()
			}
		}(i)
	}
	wg.Wait()
	// The ring holds the last 32 admissions in arrival order, so the
	// surviving max depends on interleaving — assert the invariant,
	// not an exact value: some admitted ratio in [1.00, 1.07].
	if got := s.Bound("m").Scale(); got < 1.0 || got > 1.07 {
		t.Fatalf("Scale = %v, want within [1.00, 1.07]", got)
	}
	re, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := re.Bound("m").Scale(); got == 0 {
		t.Fatal("persisted Scale = 0 after concurrent admissions")
	}
}
