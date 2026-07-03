package gcpranking

import "testing"

// TestAlignScores_OutOfOrder confirms records returned in arbitrary order map
// back to their input index — the core.Scorer order-preservation contract.
func TestAlignScores_OutOfOrder(t *testing.T) {
	out := make([]float64, 3)
	// API returns records sorted by score desc (id order 2,0,1).
	records := []rankRecord{
		{ID: "2", Score: 0.9},
		{ID: "0", Score: 0.1},
		{ID: "1", Score: 0.5},
	}
	alignScores(records, out)
	want := []float64{0.1, 0.5, 0.9}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out[%d]=%v, want %v (full: %v)", i, out[i], want[i], out)
		}
	}
}

// TestAlignScores_SkipsBadIDs confirms unparseable/out-of-range ids are
// ignored rather than panicking or corrupting other slots.
func TestAlignScores_SkipsBadIDs(t *testing.T) {
	out := make([]float64, 2)
	alignScores([]rankRecord{
		{ID: "0", Score: 0.7},
		{ID: "99", Score: 0.9}, // out of range
		{ID: "x", Score: 0.5},  // unparseable
	}, out)
	if out[0] != 0.7 || out[1] != 0 {
		t.Fatalf("bad-id handling wrong: %v", out)
	}
}

// TestScore_EmptyShortCircuits confirms the no-network fast paths.
func TestScore_EmptyShortCircuits(t *testing.T) {
	s := &scorer{model: defaultModel}
	got, err := s.Score(t.Context(), "q", nil)
	if err != nil || got != nil {
		t.Fatalf("empty candidates should return (nil,nil), got (%v,%v)", got, err)
	}
	// Empty query → zero-filled, no network call (client is nil, so a
	// network attempt would panic — proving the short-circuit).
	got, err = s.Score(t.Context(), "", []string{"a", "b"})
	if err != nil || len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Fatalf("empty query should zero-fill without a call, got (%v,%v)", got, err)
	}
}
