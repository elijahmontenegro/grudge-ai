package search

import (
	"fmt"
	"strings"

	"github.com/elijahmontenegro/grudge/rrc"
)

// compilePredicate lowers an rrc.Predicate to a SQL WHERE clause and
// argument slice for the chunk_vectors table. Empty clause means "no
// filter" (the caller still applies its model_id filter on top).
//
// The compiler is closed over the concrete predicate types declared in
// rrc/predicate.go. A new Pred* type added there must add a case here
// (or the compiler returns ErrUnsupportedPredicate so the caller can
// decide between failing closed and falling back to full scan).
func compilePredicate(p rrc.Predicate) (string, []any, error) {
	if p == nil {
		return "", nil, nil
	}
	switch v := p.(type) {
	case rrc.PredAll:
		return "", nil, nil
	case rrc.PredThread:
		return "thread_id = ?", []any{v.ThreadID}, nil
	case rrc.PredScope:
		if v.Scope == rrc.ScopeThread {
			return "thread_id = ?", []any{v.CurrentThread}, nil
		}
		// ScopeAll — no thread filter; the oracle's model_id filter
		// still applies.
		return "", nil, nil
	case rrc.PredAnd:
		return joinPredicates(v.Children, "AND")
	case rrc.PredOr:
		return joinPredicates(v.Children, "OR")
	case rrc.PredNot:
		inner, args, err := compilePredicate(v.Inner)
		if err != nil {
			return "", nil, err
		}
		if inner == "" {
			// NOT(no-filter) matches nothing.
			return "0 = 1", nil, nil
		}
		return "NOT (" + inner + ")", args, nil
	case rrc.PredHasMetadata:
		// chunk_vectors has a fixed columnar shape (thread_id,
		// model_id, role); arbitrary metadata isn't indexed today.
		// Surface as unsupported so the caller can decide instead of
		// silently returning empty results.
		return "", nil, fmt.Errorf("PredHasMetadata not supported by chunk_vectors backend (key=%q)", v.Key)
	case rrc.PredExcludeMessageIDs:
		return "", nil, fmt.Errorf("PredExcludeMessageIDs is evaluated after adaptive KNN overfetch")
	default:
		return "", nil, fmt.Errorf("unknown predicate type %T", p)
	}
}

func joinPredicates(children []rrc.Predicate, op string) (string, []any, error) {
	if len(children) == 0 {
		// Empty And matches everything (vacuously true);
		// empty Or matches nothing (vacuously false).
		if op == "AND" {
			return "", nil, nil
		}
		return "0 = 1", nil, nil
	}
	clauses := make([]string, 0, len(children))
	args := make([]any, 0)
	for _, c := range children {
		clause, a, err := compilePredicate(c)
		if err != nil {
			return "", nil, err
		}
		if clause == "" {
			// "no filter" child — for AND it's a no-op; for OR it
			// makes the whole disjunction unconditional.
			if op == "OR" {
				return "", nil, nil
			}
			continue
		}
		clauses = append(clauses, "("+clause+")")
		args = append(args, a...)
	}
	if len(clauses) == 0 {
		return "", nil, nil
	}
	return strings.Join(clauses, " "+op+" "), args, nil
}
