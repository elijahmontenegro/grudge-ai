package storage

import (
	"testing"
)

func TestTickTrace_RoundTrip(t *testing.T) {
	db := testDB(t)

	trace := &TickTrace{
		ThreadID:                   "t-trace",
		Round:                      7,
		RRCPrerequisiteSelectionMs: 1234,
		SelectMs:                   45,
		AssembleMs:                 12,
		CompleteMs:                 8900,
		StreamMs:                   9500,
		PersistMs:                  67,
		TotalMs:                    10800,
		CompleterModel:             "minimax-m2.7",
		CorpusSize:                 1968,
		SelectedCount:              8,
		AssembledTokensEst:         142000,
		UsagePredictedTokens:       141000,
		UsagePromptTokens:          138500,
		UsageCompletionTokens:      910,
		Errored:                    false,
	}
	if err := db.InsertTickTrace(trace); err != nil {
		t.Fatalf("InsertTickTrace: %v", err)
	}
	if trace.ID == 0 {
		t.Errorf("ID not populated after insert")
	}

	got, err := db.ListTickTraces("t-trace", 10)
	if err != nil {
		t.Fatalf("ListTickTraces: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(got))
	}
	r := got[0]
	if r.Round != 7 || r.RRCPrerequisiteSelectionMs != 1234 || r.SelectMs != 45 ||
		r.AssembleMs != 12 || r.CompleteMs != 8900 || r.StreamMs != 9500 ||
		r.PersistMs != 67 || r.TotalMs != 10800 {
		t.Errorf("timing fields didn't round-trip: %+v", r)
	}
	if r.CompleterModel != "minimax-m2.7" {
		t.Errorf("CompleterModel: got %q, want %q", r.CompleterModel, "minimax-m2.7")
	}
	if r.CorpusSize != 1968 || r.SelectedCount != 8 || r.AssembledTokensEst != 142000 {
		t.Errorf("counters didn't round-trip: corpus=%d selected=%d tokens=%d",
			r.CorpusSize, r.SelectedCount, r.AssembledTokensEst)
	}
	if r.UsagePredictedTokens != 141000 || r.UsagePromptTokens != 138500 || r.UsageCompletionTokens != 910 {
		t.Errorf("usage triple didn't round-trip: pred=%d prompt=%d completion=%d",
			r.UsagePredictedTokens, r.UsagePromptTokens, r.UsageCompletionTokens)
	}
	if r.Errored {
		t.Errorf("Errored should be false, got true")
	}
	if r.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should have been filled by DEFAULT CURRENT_TIMESTAMP")
	}
}

func TestTickTrace_ErroredPathPersistsErrorMsg(t *testing.T) {
	db := testDB(t)

	trace := &TickTrace{
		ThreadID: "t-err",
		Round:    3,
		TotalMs:  450,
		Errored:  true,
		ErrorMsg: "completer: context deadline exceeded",
	}
	if err := db.InsertTickTrace(trace); err != nil {
		t.Fatalf("InsertTickTrace: %v", err)
	}

	got, err := db.ListTickTraces("t-err", 1)
	if err != nil {
		t.Fatalf("ListTickTraces: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row")
	}
	if !got[0].Errored {
		t.Errorf("Errored should be true")
	}
	if got[0].ErrorMsg != "completer: context deadline exceeded" {
		t.Errorf("ErrorMsg: got %q", got[0].ErrorMsg)
	}
}

func TestTickTrace_ListOrdersNewestFirst(t *testing.T) {
	db := testDB(t)

	for i := 1; i <= 5; i++ {
		if err := db.InsertTickTrace(&TickTrace{
			ThreadID: "t-order", Round: i, TotalMs: int64(i * 100),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	got, err := db.ListTickTraces("t-order", 3)
	if err != nil {
		t.Fatalf("ListTickTraces: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("limit=3 should return 3 rows, got %d", len(got))
	}
	// Newest first → rounds 5, 4, 3.
	for i, want := range []int{5, 4, 3} {
		if got[i].Round != want {
			t.Errorf("got[%d].Round = %d, want %d", i, got[i].Round, want)
		}
	}
}
