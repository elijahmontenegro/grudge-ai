package storage

import (
	"testing"
	"time"
)

func TestRemainingBudget_MissingDuration(t *testing.T) {
	now := time.Now()
	st := &AgentState{StartedAt: &now}
	r, reason := st.RemainingBudget()
	if r != 0 || reason == "" {
		t.Errorf("expected (0, reason); got (%v, %q)", r, reason)
	}
}

func TestRemainingBudget_MissingStartedAt(t *testing.T) {
	st := &AgentState{DurationLimit: "1h"}
	r, reason := st.RemainingBudget()
	if r != 0 || reason == "" {
		t.Errorf("expected (0, reason); got (%v, %q)", r, reason)
	}
}

func TestRemainingBudget_Unparseable(t *testing.T) {
	now := time.Now()
	st := &AgentState{DurationLimit: "garbage", StartedAt: &now}
	r, reason := st.RemainingBudget()
	if r != 0 || reason == "" {
		t.Errorf("expected (0, reason); got (%v, %q)", r, reason)
	}
}

func TestRemainingBudget_Expired(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	st := &AgentState{DurationLimit: "1h", StartedAt: &past}
	r, reason := st.RemainingBudget()
	if r != 0 || reason == "" {
		t.Errorf("expected expired; got (%v, %q)", r, reason)
	}
}

func TestRemainingBudget_HealthyResume(t *testing.T) {
	// 1h budget, started 30 min ago → 30 min remaining
	started := time.Now().Add(-30 * time.Minute)
	st := &AgentState{DurationLimit: "1h", StartedAt: &started}
	r, reason := st.RemainingBudget()
	if reason != "" {
		t.Fatalf("unexpected reason: %q", reason)
	}
	if r < 25*time.Minute || r > 35*time.Minute {
		t.Errorf("expected ~30 min remaining, got %v", r)
	}
}

func TestRemainingBudget_FloorsToResumeMin(t *testing.T) {
	// 1h budget, started 59 min ago → 1 min remaining, but floor is 5m.
	started := time.Now().Add(-59 * time.Minute)
	st := &AgentState{DurationLimit: "1h", StartedAt: &started}
	r, reason := st.RemainingBudget()
	if reason != "" {
		t.Fatalf("unexpected reason: %q", reason)
	}
	if r != ResumeMin {
		t.Errorf("expected floor=%v, got %v", ResumeMin, r)
	}
}
