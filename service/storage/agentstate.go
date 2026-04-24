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

// SaveAgentState persists agent state for a thread. Full-row UPSERT —
// any field not set on the struct gets written as its zero value (Mode: 0
// = Normal, RoundCount: 0, StartedAt: nil, DurationLimit: ""). If you
// only need to change one field, use SetAgentStatus or SetAgentMode
// instead so the others aren't clobbered.
func (d *DB) SaveAgentState(s *AgentState) error {
	_, err := d.Exec(
		`INSERT INTO agent_state (thread_id, status, mode, round_count, started_at, duration_limit, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(thread_id) DO UPDATE SET
		   status = excluded.status, mode = excluded.mode,
		   round_count = excluded.round_count, started_at = excluded.started_at,
		   duration_limit = excluded.duration_limit, updated_at = excluded.updated_at`,
		s.ThreadID, s.Status, s.Mode, s.RoundCount, s.StartedAt, s.DurationLimit,
	)
	return err
}

// SetAgentStatus updates only the status column (and updated_at), leaving
// Mode, RoundCount, StartedAt, DurationLimit intact. Returns sql.ErrNoRows
// semantics only via the caller's expectations — SQLite UPDATE with no
// matching row is a silent no-op. Callers that need creation should use
// SaveAgentState instead.
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
