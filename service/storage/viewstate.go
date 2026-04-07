package storage

// SaveViewState persists JSON-encoded UI state for a thread.
func (d *DB) SaveViewState(threadID string, state []byte) error {
	_, err := d.Exec(
		`INSERT INTO view_state (thread_id, state, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(thread_id) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		threadID, state,
	)
	return err
}

// GetViewState retrieves persisted UI state for a thread.
func (d *DB) GetViewState(threadID string) ([]byte, error) {
	var state []byte
	err := d.QueryRow(`SELECT state FROM view_state WHERE thread_id = ?`, threadID).Scan(&state)
	return state, err
}
