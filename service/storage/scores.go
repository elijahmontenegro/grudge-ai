package storage

// InsertScore persists a cross-encoder pairwise score.
func (d *DB) InsertScore(fromID, toID string, score float64) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO scores (from_message_id, to_message_id, score) VALUES (?, ?, ?)`,
		fromID, toID, score,
	)
	return err
}

// AllScores loads the full score cache for engine startup.
func (d *DB) AllScores() (map[[2]string]float64, error) {
	rows, err := d.Query(`SELECT from_message_id, to_message_id, score FROM scores`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := make(map[[2]string]float64)
	for rows.Next() {
		var from, to string
		var score float64
		if err := rows.Scan(&from, &to, &score); err != nil {
			return nil, err
		}
		scores[[2]string{from, to}] = score
	}
	return scores, rows.Err()
}
