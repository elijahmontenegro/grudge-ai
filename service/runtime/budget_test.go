package runtime

import (
	"testing"
	"time"

	"github.com/emontenegr/spidey/service/storage"
)

func TestComputeRemainingBudget_MissingDuration(t *testing.T) {
	now := time.Now()
	st := &storage.AgentState{StartedAt: &now}
	r, reason := ComputeRemainingBudget(st)
	if r != 0 || reason == "" {
		t.Errorf("expected (0, reason); got (%v, %q)", r, reason)
	}
}

func TestComputeRemainingBudget_MissingStartedAt(t *testing.T) {
	st := &storage.AgentState{DurationLimit: "1h"}
	r, reason := ComputeRemainingBudget(st)
	if r != 0 || reason == "" {
		t.Errorf("expected (0, reason); got (%v, %q)", r, reason)
	}
}

func TestComputeRemainingBudget_Unparseable(t *testing.T) {
	now := time.Now()
	st := &storage.AgentState{DurationLimit: "garbage", StartedAt: &now}
	r, reason := ComputeRemainingBudget(st)
	if r != 0 || reason == "" {
		t.Errorf("expected (0, reason); got (%v, %q)", r, reason)
	}
}

func TestComputeRemainingBudget_Expired(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	st := &storage.AgentState{DurationLimit: "1h", StartedAt: &past}
	r, reason := ComputeRemainingBudget(st)
	if r != 0 || reason == "" {
		t.Errorf("expected expired; got (%v, %q)", r, reason)
	}
}

func TestComputeRemainingBudget_HealthyResume(t *testing.T) {
	// 1h budget, started 30 min ago → 30 min remaining
	started := time.Now().Add(-30 * time.Minute)
	st := &storage.AgentState{DurationLimit: "1h", StartedAt: &started}
	r, reason := ComputeRemainingBudget(st)
	if reason != "" {
		t.Fatalf("unexpected reason: %q", reason)
	}
	if r < 25*time.Minute || r > 35*time.Minute {
		t.Errorf("expected ~30 min remaining, got %v", r)
	}
}

func TestComputeRemainingBudget_FloorsToResumeMin(t *testing.T) {
	// 1h budget, started 59 min ago → 1 min remaining, but floor is 5m.
	started := time.Now().Add(-59 * time.Minute)
	st := &storage.AgentState{DurationLimit: "1h", StartedAt: &started}
	r, reason := ComputeRemainingBudget(st)
	if reason != "" {
		t.Fatalf("unexpected reason: %q", reason)
	}
	if r != ResumeMin {
		t.Errorf("expected floor=%v, got %v", ResumeMin, r)
	}
}
