package storage

// InsertLocalContextScore persists one scorer result between a transient
// serialized Local Context chunk and a stored candidate chunk.
func (d *DB) InsertLocalContextScore(localContextFingerprint string, localContextChunkIndex int, candidateID string, candidateChunkIndex int, modelID string, score float64) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO scores
		  (local_context_fingerprint, local_context_chunk_index, candidate_message_id, candidate_chunk_index, model_id, score)
		  VALUES (?, ?, ?, ?, ?, ?)`,
		localContextFingerprint, localContextChunkIndex, candidateID, candidateChunkIndex, modelID, score,
	)
	return err
}

type LocalContextScoreKey struct {
	LocalContextFingerprint string
	LocalContextChunkIndex  int
	CandidateID             string
	CandidateChunkIdx       int
}

func (d *DB) LocalContextScoresForModel(modelID string) (map[LocalContextScoreKey]float64, error) {
	rows, err := d.Query(
		`SELECT local_context_fingerprint, local_context_chunk_index, candidate_message_id, candidate_chunk_index, score
		 FROM scores WHERE model_id = ?`,
		modelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[LocalContextScoreKey]float64)
	for rows.Next() {
		var key LocalContextScoreKey
		var score float64
		if err := rows.Scan(
			&key.LocalContextFingerprint, &key.LocalContextChunkIndex,
			&key.CandidateID, &key.CandidateChunkIdx, &score,
		); err != nil {
			return nil, err
		}
		out[key] = score
	}
	return out, rows.Err()
}
