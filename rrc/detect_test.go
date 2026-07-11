package rrc

import (
	"math"
	"testing"
)

// TestNullP_ExactRankStatistics pins the exactness of the rank-based
// null probability: with R references, beating all of them is
// 1/(R+1); losing to all is (R+1)/(R+1) = 1; ties count against the
// candidate.
func TestNullP_ExactRankStatistics(t *testing.T) {
	refs := make([]float64, 16)
	for i := range refs {
		refs[i] = float64(i+1) / 100.0 // 0.01 .. 0.16
	}
	if p := nullP(0.5, refs); math.Abs(p-1.0/17.0) > 1e-12 {
		t.Fatalf("beat-all p = %v, want 1/17", p)
	}
	if p := nullP(0.001, refs); math.Abs(p-1.0) > 1e-12 {
		t.Fatalf("lose-all p = %v, want 1", p)
	}
	// Tie with the max: the tied reference counts as >= the candidate.
	if p := nullP(0.16, refs); math.Abs(p-2.0/17.0) > 1e-12 {
		t.Fatalf("tie-with-max p = %v, want 2/17", p)
	}
	// Mid-rank: 8 refs at or above.
	if p := nullP(0.085, refs); math.Abs(p-9.0/17.0) > 1e-12 {
		t.Fatalf("mid-rank p = %v, want 9/17", p)
	}
}

func TestSurprisalAndConfidenceRoundTrip(t *testing.T) {
	for _, p := range []float64{1.0 / 17.0, 0.25, 0.5, 0.9} {
		bits := surprisalBits(p)
		conf := confidenceFromBits(bits)
		if math.Abs(conf-(1-p)) > 1e-12 {
			t.Fatalf("confidence(surprisal(%v)) = %v, want %v", p, conf, 1-p)
		}
		if back := bitsFromConfidence(conf); math.Abs(back-bits) > 1e-9 {
			t.Fatalf("bits round-trip: %v -> %v", bits, back)
		}
	}
	if got := bitsFromConfidence(0); got != 0 {
		t.Fatalf("zero confidence must be zero bits, got %v", got)
	}
	if got := confidenceFromBits(math.Inf(1)); got != 1 {
		t.Fatalf("infinite bits must be confidence 1, got %v", got)
	}
}

// TestStanceBits pins the stance mapping: the one hand-set value
// judgment in bits. LossRatio 0.5 = one bit of evidence.
func TestStanceBits(t *testing.T) {
	cases := []struct{ lr, want float64 }{
		{0.5, 1}, {0.75, 2}, {0.875, 3}, {0, 0},
	}
	for _, c := range cases {
		if got := stanceBits(c.lr); math.Abs(got-c.want) > 1e-12 {
			t.Fatalf("stanceBits(%v) = %v, want %v", c.lr, got, c.want)
		}
	}
	if !math.IsInf(stanceBits(1), 1) {
		t.Fatal("stanceBits(1) must be +Inf (harm-only stance accepts nothing)")
	}
}

// TestFlatReference: zero spread across an adequate sample = dead
// instrument; small samples never trip it (cold start is ungated, not
// an error).
func TestFlatReference(t *testing.T) {
	if !flatReference([]float64{0.5, 0.5, 0.5, 0.5}) {
		t.Fatal("constant sample must read as flat")
	}
	if flatReference([]float64{0.5, 0.5, 0.5, 0.51}) {
		t.Fatal("spread sample must not read as flat")
	}
	if flatReference([]float64{0.5, 0.5}) {
		t.Fatal("below minReferenceSample must not trip the guard")
	}
}

// TestReferenceSeed_Deterministic: same fingerprint, same seed;
// different fingerprints, different seeds (determinism of the draw
// without bias across events).
func TestReferenceSeed_Deterministic(t *testing.T) {
	a1, a2 := referenceSeed("fp-alpha"), referenceSeed("fp-alpha")
	if a1 != a2 {
		t.Fatal("seed must be deterministic per fingerprint")
	}
	if referenceSeed("fp-alpha") == referenceSeed("fp-beta") {
		t.Fatal("distinct fingerprints should not collide (FNV-64a)")
	}
}
