package rrc

import (
	"fmt"
	"testing"
)

// TestCompilePredicateMatchesEval is the load-bearing guarantee: the compiled
// evaluator must return exactly what EvalPredicate returns for every predicate
// shape and every candidate, or the O(1) exclusion optimisation silently changes
// retrieval semantics.
func TestCompilePredicateMatchesEval(t *testing.T) {
	preds := []Predicate{
		nil,
		PredAll{},
		PredThread{ThreadID: "t1"},
		PredThread{ThreadID: ""},
		PredScope{CurrentThread: "t1", Scope: ScopeThread},
		PredScope{CurrentThread: "t1", Scope: ScopeAll},
		PredScope{CurrentThread: "", Scope: ScopeThread},
		PredExcludeMessageIDs{MessageIDs: []string{"m1", "m3"}},
		PredExcludeMessageIDs{MessageIDs: nil},
		PredHasMetadata{Key: "model_id", Value: "x"},
		PredHasMetadata{Key: "thread_id", Value: "t1"},
		PredNot{Inner: PredThread{ThreadID: "t1"}},
		PredOr{Children: []Predicate{PredThread{ThreadID: "t1"}, PredThread{ThreadID: "t2"}}},
		PredAnd{Children: []Predicate{
			PredScope{CurrentThread: "t1", Scope: ScopeThread},
			PredExcludeMessageIDs{MessageIDs: []string{"m2"}},
		}},
		PredAnd{Children: nil}, // empty AND == PredAll
		PredAnd{Children: []Predicate{
			PredOr{Children: []Predicate{PredThread{ThreadID: "t1"}, PredHasMetadata{Key: "model_id", Value: "x"}}},
			PredNot{Inner: PredExcludeMessageIDs{MessageIDs: []string{"m1"}}},
		}},
	}
	attrs := []CandidateAttrs{
		{MessageID: "m1", ThreadID: "t1", Metadata: map[string]string{"model_id": "x"}},
		{MessageID: "m2", ThreadID: "t1", Metadata: map[string]string{"model_id": "y"}},
		{MessageID: "m3", ThreadID: "t2", Metadata: map[string]string{"model_id": "x"}},
		{MessageID: "m4", ThreadID: "", Metadata: nil},
		{MessageID: "", ThreadID: "t1", Metadata: map[string]string{}},
	}
	for pi, p := range preds {
		match := CompilePredicate(p)
		for ai, a := range attrs {
			want := EvalPredicate(p, a)
			got := match(a)
			if got != want {
				t.Errorf("pred[%d] %s attr[%d] %+v: compiled=%v eval=%v", pi, fmt.Sprintf("%T%+v", p, p), ai, a, got, want)
			}
		}
	}
}
