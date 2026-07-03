package storage

import (
	"database/sql"
	"fmt"
)

// Chunk is a persisted slice of a message's text. Mirrors chunk.Chunk
// — duplicated here so the storage package can describe its row shape
// without taking a dependency on the splitter.
type Chunk struct {
	MessageID  string
	ChunkIndex int
	Text       string
	ByteStart  int
	ByteEnd    int
	TokenEst   int
}

// InsertChunks writes a message's chunks in a single transaction.
// Idempotent on (message_id, chunk_index) — re-chunking a message
// (e.g., after a model or config swap) replaces the rows in place.
func (d *DB) InsertChunks(messageID string, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear old chunks for this message first — replacing, not merging.
	if _, err := tx.Exec("DELETE FROM chunks WHERE message_id = ?", messageID); err != nil {
		return fmt.Errorf("clear chunks for %s: %w", messageID, err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO chunks (message_id, chunk_index, text, byte_start, byte_end, token_est)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		if _, err := stmt.Exec(messageID, c.ChunkIndex, c.Text, c.ByteStart, c.ByteEnd, c.TokenEst); err != nil {
			return fmt.Errorf("insert chunk %d of %s: %w", c.ChunkIndex, messageID, err)
		}
	}
	return tx.Commit()
}

// GetChunks fetches all chunks for a message, ordered by index.
func (d *DB) GetChunks(messageID string) ([]Chunk, error) {
	rows, err := d.Query(`
		SELECT message_id, chunk_index, text, byte_start, byte_end, token_est
		FROM chunks WHERE message_id = ? ORDER BY chunk_index
	`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.MessageID, &c.ChunkIndex, &c.Text, &c.ByteStart, &c.ByteEnd, &c.TokenEst); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChunksForMessages fetches all chunks for a list of messages in
// one query. Returns a map keyed by message_id, each value ordered
// by chunk_index. Used when scoring many candidates for one query.
func (d *DB) GetChunksForMessages(messageIDs []string) (map[string][]Chunk, error) {
	if len(messageIDs) == 0 {
		return map[string][]Chunk{}, nil
	}
	// Build IN (?, ?, ...) with placeholders
	placeholders := ""
	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT message_id, chunk_index, text, byte_start, byte_end, token_est
		FROM chunks WHERE message_id IN (%s) ORDER BY message_id, chunk_index
	`, placeholders)
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]Chunk, len(messageIDs))
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.MessageID, &c.ChunkIndex, &c.Text, &c.ByteStart, &c.ByteEnd, &c.TokenEst); err != nil {
			return nil, err
		}
		out[c.MessageID] = append(out[c.MessageID], c)
	}
	return out, rows.Err()
}

// ChunkExists returns true if any chunk exists for the message.
func (d *DB) ChunkExists(messageID string) (bool, error) {
	var n int
	err := d.QueryRow("SELECT 1 FROM chunks WHERE message_id = ? LIMIT 1", messageID).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
