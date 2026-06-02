package search

import (
	"strings"
	"testing"

	"github.com/emontenegr/grudge/rrc"
)

func TestCompilePredicate(t *testing.T) {
	cases := []struct {
		name      string
		pred      rrc.Predicate
		wantSQL   string
		wantArgs  []any
		wantError bool
	}{
		{
			name:    "PredAll empty filter",
			pred:    rrc.PredAll{},
			wantSQL: "",
		},
		{
			name:     "PredThread",
			pred:     rrc.PredThread{ThreadID: "t-1"},
			wantSQL:  "thread_id = ?",
			wantArgs: []any{"t-1"},
		},
		{
			name:     "PredScope thread-only",
			pred:     rrc.PredScope{CurrentThread: "t-7", Scope: rrc.ScopeThread},
			wantSQL:  "thread_id = ?",
			wantArgs: []any{"t-7"},
		},
		{
			name:    "PredScope all is no-op",
			pred:    rrc.PredScope{CurrentThread: "t-7", Scope: rrc.ScopeAll},
			wantSQL: "",
		},
		{
			name: "PredAnd composes",
			pred: rrc.PredAnd{Children: []rrc.Predicate{
				rrc.PredThread{ThreadID: "a"},
				rrc.PredThread{ThreadID: "b"},
			}},
			wantSQL:  "(thread_id = ?) AND (thread_id = ?)",
			wantArgs: []any{"a", "b"},
		},
		{
			name: "PredOr composes",
			pred: rrc.PredOr{Children: []rrc.Predicate{
				rrc.PredThread{ThreadID: "a"},
				rrc.PredThread{ThreadID: "b"},
			}},
			wantSQL:  "(thread_id = ?) OR (thread_id = ?)",
			wantArgs: []any{"a", "b"},
		},
		{
			name:     "PredNot wraps",
			pred:     rrc.PredNot{Inner: rrc.PredThread{ThreadID: "t-1"}},
			wantSQL:  "NOT (thread_id = ?)",
			wantArgs: []any{"t-1"},
		},
		{
			name:    "PredNot of all matches nothing",
			pred:    rrc.PredNot{Inner: rrc.PredAll{}},
			wantSQL: "0 = 1",
		},
		{
			name: "PredAnd with all child is no-op for that branch",
			pred: rrc.PredAnd{Children: []rrc.Predicate{
				rrc.PredThread{ThreadID: "t-1"},
				rrc.PredAll{},
			}},
			wantSQL:  "(thread_id = ?)",
			wantArgs: []any{"t-1"},
		},
		{
			name: "PredOr with all child is unconditional",
			pred: rrc.PredOr{Children: []rrc.Predicate{
				rrc.PredThread{ThreadID: "t-1"},
				rrc.PredAll{},
			}},
			wantSQL: "",
		},
		{
			name:    "Empty PredAnd matches everything",
			pred:    rrc.PredAnd{},
			wantSQL: "",
		},
		{
			name:    "Empty PredOr matches nothing",
			pred:    rrc.PredOr{},
			wantSQL: "0 = 1",
		},
		{
			name: "Nested PredAnd of PredOr",
			pred: rrc.PredAnd{Children: []rrc.Predicate{
				rrc.PredThread{ThreadID: "t-1"},
				rrc.PredOr{Children: []rrc.Predicate{
					rrc.PredThread{ThreadID: "t-2"},
					rrc.PredThread{ThreadID: "t-3"},
				}},
			}},
			wantSQL:  "(thread_id = ?) AND ((thread_id = ?) OR (thread_id = ?))",
			wantArgs: []any{"t-1", "t-2", "t-3"},
		},
		{
			name:      "PredHasMetadata is unsupported by chunk_vectors backend",
			pred:      rrc.PredHasMetadata{Key: "role", Value: "user"},
			wantError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := compilePredicate(tc.pred)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error, got SQL=%q args=%v", sql, args)
				}
				return
			}
			if err != nil {
				t.Fatalf("compilePredicate: %v", err)
			}
			if sql != tc.wantSQL {
				t.Errorf("SQL mismatch: got %q, want %q", sql, tc.wantSQL)
			}
			if !argsEqual(args, tc.wantArgs) {
				t.Errorf("args mismatch: got %v, want %v", args, tc.wantArgs)
			}
		})
	}
}

func TestCompilePredicateNilArg(t *testing.T) {
	sql, args, err := compilePredicate(nil)
	if err != nil {
		t.Fatalf("nil predicate should not error: %v", err)
	}
	if sql != "" || len(args) != 0 {
		t.Errorf("nil predicate should be no-op, got SQL=%q args=%v", sql, args)
	}
}

func TestCompilePredicateUnknownType(t *testing.T) {
	type customPred struct{ rrc.Predicate }
	_, _, err := compilePredicate(customPred{})
	if err == nil {
		t.Error("expected error for unknown predicate type")
	}
	if err != nil && !strings.Contains(err.Error(), "unknown predicate type") {
		t.Errorf("error should mention unknown type: %v", err)
	}
}

func argsEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
