package chunkkey

import "testing"

func TestMakeSplitRoundTrip(t *testing.T) {
	cases := []struct {
		mid string
		idx int
	}{
		{"m0000001", 0},
		{"msg-with-dashes", 7},
		{"01H8XYZ", 123},
		{"", 0}, // degenerate but must round-trip
	}
	for _, c := range cases {
		key := Make(c.mid, c.idx)
		gotMid, gotIdx := Split(key)
		if gotMid != c.mid || gotIdx != c.idx {
			t.Errorf("Make(%q,%d)->Split = (%q,%d), want (%q,%d)", c.mid, c.idx, gotMid, gotIdx, c.mid, c.idx)
		}
	}
}

// TestSplitMalformed: a key without the separator yields the whole string and
// index 0 rather than panicking (defensive).
func TestSplitMalformed(t *testing.T) {
	mid, idx := Split("no-separator-here")
	if mid != "no-separator-here" || idx != 0 {
		t.Fatalf("Split(malformed) = (%q,%d), want (whole,0)", mid, idx)
	}
}

// Distinct chunks of one message produce distinct keys (so the index does not
// collapse them) that all split back to the same message id.
func TestMakeDistinctChunks(t *testing.T) {
	seen := map[string]bool{}
	for i := range 5 {
		key := Make("m1", i)
		if seen[key] {
			t.Fatalf("duplicate key for chunk %d", i)
		}
		seen[key] = true
		if mid, _ := Split(key); mid != "m1" {
			t.Fatalf("chunk %d split to message %q, want m1", i, mid)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("got %d distinct keys, want 5", len(seen))
	}
}
