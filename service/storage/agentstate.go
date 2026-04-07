package storage

import (
	"database/sql"
	"time"
)

// AgentState represents the persisted agent execution state.
type AgentState struct {
	ThreadID      string
	Status        int // 0=idle, 1=running, 2=paused
	Mode          int // 0=normal, 1=autonomous, 2=plan
	RoundCount    int
	StartedAt     *time.Time
	DurationLimit string
}

// SaveAgentState persists agent state for a thread.
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
