package storage

// Score cache — persistent analog of rrc.ScoreCache. Reranker scores
// keyed by (from_message_id, from_chunk_index, to_message_id,
// to_chunk_index, model_id). The chunk-pair key lets different chunks
// of the same two messages carry different relevance scores —
// essential for the bible-as-reference pattern where a specific
// bible chunk matches a specific chapter chunk, even though the bible
// and chapter as wholes have weaker global affinity.
//
// Messages are immutable and chunks are a pure function of message
// content so once scored a pair is stable; cascade FKs from chunks →
// messages mean hard-deleting either cleans the row. Switching the
// reranker model leaves old rows under their old model_id; readers
// filter to the currently configured model.

// InsertChunkScore persists a single chunk-pair score.
func (d *DB) InsertChunkScore(fromID string, fromIdx int, toID string, toIdx int, modelID string, score float64) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO scores
		  (from_message_id, from_chunk_index, to_message_id, to_chunk_index, model_id, score)
		  VALUES (?, ?, ?, ?, ?, ?)`,
		fromID, fromIdx, toID, toIdx, modelID, score,
	)
	return err
}

// ChunkScoreRow is one persisted chunk-pair score.
type ChunkScoreRow struct {
	FromID  string
	FromIdx int
	ToID    string
	ToIdx   int
	ModelID string
	Score   float64
}

// InsertChunkScoresBatch persists many chunk-pair scores in one
// transaction. Used after a rerank call where tens to hundreds of
// pairs scored at once.
func (d *DB) InsertChunkScoresBatch(scores []ChunkScoreRow) error {
	if len(scores) == 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(
		`INSERT OR REPLACE INTO scores
		  (from_message_id, from_chunk_index, to_message_id, to_chunk_index, model_id, score)
		  VALUES (?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, s := range scores {
		if _, err := stmt.Exec(s.FromID, s.FromIdx, s.ToID, s.ToIdx, s.ModelID, s.Score); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ChunkScoreKey is a compact key for the engine's in-memory cache.
type ChunkScoreKey struct {
	FromID  string
	FromIdx int
	ToID    string
	ToIdx   int
}

// MaxScoresFrom returns, for the given query message, a map from
// candidate-message-id to the maximum chunk-pair score recorded
// between any chunk of the query and any chunk of that candidate
// (direction: scores written as from=query, to=candidate by
// engine.OnMessage). Used by the protocol-rectification rule
// pipeline to rank Store-resident candidates for protocol-slot
// fills against the current query.
//
// Empty result is valid and means "no cached scores against this
// query" — callers fall back to whatever secondary signal they use
// (recency, corpus order) or drop.
func (d *DB) MaxScoresFrom(queryID, modelID string) (map[string]float64, error) {
	rows, err := d.Query(
		`SELECT to_message_id, MAX(score)
		 FROM scores
		 WHERE from_message_id = ? AND model_id = ?
		 GROUP BY to_message_id`,
		queryID, modelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]float64)
	for rows.Next() {
		var toID string
		var score float64
		if err := rows.Scan(&toID, &score); err != nil {
			return nil, err
		}
		out[toID] = score
	}
	return out, rows.Err()
}

// ChunkScoresForModel loads every cached chunk-pair score for the
// given model_id. Warm-up on service startup.
func (d *DB) ChunkScoresForModel(modelID string) (map[ChunkScoreKey]float64, error) {
	rows, err := d.Query(
		`SELECT from_message_id, from_chunk_index, to_message_id, to_chunk_index, score
		 FROM scores WHERE model_id = ?`,
		modelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[ChunkScoreKey]float64)
	for rows.Next() {
		var fromID, toID string
		var fromIdx, toIdx int
		var score float64
		if err := rows.Scan(&fromID, &fromIdx, &toID, &toIdx, &score); err != nil {
			return nil, err
		}
		out[ChunkScoreKey{FromID: fromID, FromIdx: fromIdx, ToID: toID, ToIdx: toIdx}] = score
	}
	return out, rows.Err()
}
