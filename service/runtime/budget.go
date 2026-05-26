package runtime

import (
	"time"

	"github.com/emontenegr/spidey/service/storage"
)

// ResumeMin is the floor on an auto-resumed autonomous budget.
// Computed remaining shorter than this rounds up — fewer than a
// handful of minutes is not a meaningful budget for a multi-round
// reflective loop.
const ResumeMin = 5 * time.Minute

// ComputeRemainingBudget derives how much of the original
// autonomous budget is still owed for a row resumed at boot.
// Returns (remaining, "") on success. On failure returns (0,
// reason) where reason distinguishes:
//
//   - missing duration_limit
//   - missing started_at
//   - unparseable / non-positive duration_limit
//   - expired (elapsed >= total)
//
// Each is a meaningfully different reason the reconcile can't
// resume; collapsing them into one log line loses the signal that
// helps diagnose why a particular run didn't pick back up after
// restart.
//
// A 5-minute floor (ResumeMin) applies: if the computed remaining
// is shorter, round up. This preserves the "why we run autonomous"
// — fewer than a handful of minutes is not a meaningful budget.
func ComputeRemainingBudget(st *storage.AgentState) (time.Duration, string) {
	if st.DurationLimit == "" {
		return 0, "no duration_limit in DB row"
	}
	if st.StartedAt == nil {
		return 0, "no started_at in DB row"
	}
	total, err := time.ParseDuration(st.DurationLimit)
	if err != nil {
		return 0, "unparseable duration_limit=" + st.DurationLimit
	}
	if total <= 0 {
		return 0, "non-positive duration_limit=" + st.DurationLimit
	}
	elapsed := time.Since(*st.StartedAt)
	remaining := total - elapsed
	if remaining <= 0 {
		return 0, "budget expired (started " + elapsed.Round(time.Second).String() + " ago, limit " + total.String() + ")"
	}
	if remaining < ResumeMin {
		remaining = ResumeMin
	}
	return remaining, ""
}
