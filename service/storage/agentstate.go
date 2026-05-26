package storage

import (
	"database/sql"
	"time"
)

// AgentStatus represents the execution status of an agent.
type AgentStatus int

const (
	AgentStatusIdle    AgentStatus = 0
	AgentStatusRunning AgentStatus = 1
	AgentStatusPaused  AgentStatus = 2
)

// AgentMode represents the operating mode of an agent.
type AgentMode int

const (
	AgentModeNormal     AgentMode = 0
	AgentModeAutonomous AgentMode = 1
	AgentModePlan       AgentMode = 2
)

// AgentState represents the persisted agent execution state.
type AgentState struct {
	ThreadID      string
	Status        AgentStatus
	Mode          AgentMode
	RoundCount    int
	StartedAt     *time.Time
	DurationLimit string
}

// ResumeMin is the floor on an auto-resumed autonomous budget.
// Computed remaining shorter than this rounds up — fewer than a
// handful of minutes is not a meaningful budget for a multi-round
// reflective loop.
const ResumeMin = 5 * time.Minute

// RemainingBudget derives how much of the original autonomous
// budget is still owed for a row resumed at boot. Returns
// (remaining, "") on success. On failure returns (0, reason) where
// reason distinguishes missing duration_limit, missing started_at,
// unparseable / non-positive duration_limit, or expired
// (elapsed >= total). Each is a meaningfully different reason the
// reconcile can't resume; collapsing them into one log line loses
// the signal that helps diagnose why a particular run didn't pick
// back up after restart.
//
// A 5-minute floor (ResumeMin) applies: if the computed remaining
// is shorter, round up. Fewer than a handful of minutes is not a
// meaningful budget.
func (st *AgentState) RemainingBudget() (time.Duration, string) {
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

// StartAutonomousRun is the only legitimate full-row write on
// agent_state — it begins a new autonomous run by recording the
// run's parameters (status=Running, mode=Autonomous, started_at,
// duration_limit) and resetting round_count to 0. Every OTHER state
// transition is a narrow update via SetAgentStatus /
// SetAgentStatusAndMode / SetAgentRoundCount; those preserve the
// metadata this call writes so the run's audit trail survives pause /
// resume / mid-run state flips.
//
// UPSERT (INSERT … ON CONFLICT DO UPDATE) is correct here: a thread
// that previously ran autonomously and is now starting a fresh run
// SHOULD have its old start_time / duration_limit replaced with the
// new run's values. round_count resets to 0 because rounds count
// per-run, not per-thread.
func (d *DB) StartAutonomousRun(threadID string, startedAt time.Time, durationLimit string) error {
	_, err := d.Exec(
		`INSERT INTO agent_state (thread_id, status, mode, round_count, started_at, duration_limit, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(thread_id) DO UPDATE SET
		   status = excluded.status, mode = excluded.mode,
		   round_count = excluded.round_count, started_at = excluded.started_at,
		   duration_limit = excluded.duration_limit, updated_at = excluded.updated_at`,
		threadID, AgentStatusRunning, AgentModeAutonomous, startedAt, durationLimit,
	)
	return err
}

// EnsureAgentStateRow guarantees a row exists for threadID with the
// supplied initial status+mode. INSERT OR IGNORE — if a row already
// exists, this is a no-op (no metadata clobbering). Used by callers
// that want to set Idle/Normal but don't know whether the row exists
// yet (Stop on a thread that never ran has no row to update; without
// this the SetAgentStatusAndMode call below would silently no-op).
func (d *DB) EnsureAgentStateRow(threadID string, status AgentStatus, mode AgentMode) error {
	_, err := d.Exec(
		`INSERT OR IGNORE INTO agent_state (thread_id, status, mode, round_count, updated_at)
		 VALUES (?, ?, ?, 0, CURRENT_TIMESTAMP)`,
		threadID, status, mode,
	)
	return err
}

// SetAgentStatus updates only the status column (and updated_at), leaving
// Mode, RoundCount, StartedAt, DurationLimit intact. SQLite UPDATE with
// no matching row is a silent no-op — for transitions on potentially-
// fresh threads use EnsureAgentStateRow first.
func (d *DB) SetAgentStatus(threadID string, status AgentStatus) error {
	_, err := d.Exec(
		`UPDATE agent_state SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE thread_id = ?`,
		status, threadID,
	)
	return err
}

// SetAgentRoundCount updates only round_count (+ updated_at). Called from
// the autonomous loop on every tick without wiping the start-time or
// duration-limit metadata.
func (d *DB) SetAgentRoundCount(threadID string, round int) error {
	_, err := d.Exec(
		`UPDATE agent_state SET round_count = ?, updated_at = CURRENT_TIMESTAMP WHERE thread_id = ?`,
		round, threadID,
	)
	return err
}

// SetAgentStatusAndMode updates status + mode (+ updated_at), preserving
// round_count / started_at / duration_limit. Useful from EnterPlan/ExitPlan
// tool callbacks which change both axes but shouldn't reset the autonomous
// run's metadata.
func (d *DB) SetAgentStatusAndMode(threadID string, status AgentStatus, mode AgentMode) error {
	_, err := d.Exec(
		`UPDATE agent_state SET status = ?, mode = ?, updated_at = CURRENT_TIMESTAMP WHERE thread_id = ?`,
		status, mode, threadID,
	)
	return err
}

// ListActiveAgentStates returns every row whose status is not Idle.
// Used by boot reconciliation to find threads that were mid-run when
// the service last shut down: anything marked Running or Paused has
// no in-memory runner (goroutines don't survive process exit), so the
// reconcile path either auto-resumes autonomous runs or normalizes
// the row back to Idle for everything else.
func (d *DB) ListActiveAgentStates() ([]*AgentState, error) {
	rows, err := d.Query(
		`SELECT thread_id, status, mode, round_count, started_at, duration_limit
		 FROM agent_state WHERE status != 0`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AgentState
	for rows.Next() {
		s := &AgentState{}
		var startedAt sql.NullTime
		var durLimit sql.NullString
		if err := rows.Scan(&s.ThreadID, &s.Status, &s.Mode, &s.RoundCount, &startedAt, &durLimit); err != nil {
			return nil, err
		}
		if startedAt.Valid {
			s.StartedAt = &startedAt.Time
		}
		if durLimit.Valid {
			s.DurationLimit = durLimit.String
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetAgentState retrieves agent state for a thread.
func (d *DB) GetAgentState(threadID string) (*AgentState, error) {
	s := &AgentState{ThreadID: threadID}
	var startedAt sql.NullTime
	var durLimit sql.NullString

	err := d.QueryRow(
		`SELECT status, mode, round_count, started_at, duration_limit
		 FROM agent_state WHERE thread_id = ?`, threadID,
	).Scan(&s.Status, &s.Mode, &s.RoundCount, &startedAt, &durLimit)
	if err != nil {
		return nil, err
	}

	if startedAt.Valid {
		s.StartedAt = &startedAt.Time
	}
	if durLimit.Valid {
		s.DurationLimit = durLimit.String
	}
	return s, nil
}
