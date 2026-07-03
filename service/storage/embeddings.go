package storage

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Embedding cache — one vector per (chunk, embedder model) triple.
// Storage is the sqlite-vec vec0 virtual table chunk_vectors. The vec0
// engine builds an internal index for sub-linear KNN; auxiliary columns
// (message_id, chunk_index, thread_id, model_id, role) are pushed into
// the MATCH query as filter predicates.
//
// chunk_rowid is sourced from the chunk_vector_rowids mapping table:
// SQLite AUTOINCREMENT gives unique int64 ids with no collision risk,
// and the UNIQUE (message_id, chunk_index, model_id) constraint makes
// "get-or-create rowid" a one-statement INSERT OR IGNORE + SELECT.

// InsertChunkEmbedding stores a vector for a (message, chunk, model)
// triple, denormalizing thread_id and role from the message for
// predicate-side filter speed (no JOIN to messages at query time).
// Idempotent re-backfill: existing rows are replaced via DELETE+INSERT
// at the same rowid (vec0 doesn't accept INSERT OR REPLACE).
func (d *DB) InsertChunkEmbedding(messageID string, chunkIndex int, modelID string, vec []float32) error {
	var threadID string
	var role int
	if err := d.QueryRow(
		`SELECT thread_id, role FROM messages WHERE id = ?`, messageID,
	).Scan(&threadID, &role); err != nil {
		return fmt.Errorf("InsertChunkEmbedding: lookup message %s: %w", messageID, err)
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Get-or-create rowid via the mapping table. UNIQUE constraint
	// catches duplicates at insert; the SELECT then reads back the
	// canonical rowid (whether just-inserted or pre-existing).
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO chunk_vector_rowids (message_id, chunk_index, model_id)
		 VALUES (?, ?, ?)`,
		messageID, chunkIndex, modelID,
	); err != nil {
		return fmt.Errorf("InsertChunkEmbedding: rowid mapping: %w", err)
	}
	var rowID int64
	if err := tx.QueryRow(
		`SELECT chunk_rowid FROM chunk_vector_rowids
		 WHERE message_id = ? AND chunk_index = ? AND model_id = ?`,
		messageID, chunkIndex, modelID,
	).Scan(&rowID); err != nil {
		return fmt.Errorf("InsertChunkEmbedding: lookup rowid: %w", err)
	}

	// vec0 doesn't support INSERT OR REPLACE; emulate via DELETE+INSERT
	// at the same rowid.
	if _, err := tx.Exec(`DELETE FROM chunk_vectors WHERE chunk_rowid = ?`, rowID); err != nil {
		return fmt.Errorf("InsertChunkEmbedding: delete prior: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO chunk_vectors
		 (chunk_rowid, embedding, message_id, chunk_index, thread_id, model_id, role)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rowID, encodeVector(vec), messageID, chunkIndex, threadID, modelID, role,
	); err != nil {
		return fmt.Errorf("InsertChunkEmbedding: insert: %w", err)
	}
	return tx.Commit()
}

// GetChunkEmbedding returns (vec, true) if cached, (nil, false) if
// not. For one-off lookups; use GetChunkEmbeddingsForMessages for
// hot-path batched access.
func (d *DB) GetChunkEmbedding(messageID string, chunkIndex int, modelID string) ([]float32, bool, error) {
	var blob []byte
	err := d.QueryRow(
		`SELECT embedding FROM chunk_vectors WHERE message_id = ? AND chunk_index = ? AND model_id = ?`,
		messageID, chunkIndex, modelID,
	).Scan(&blob)
	if err != nil {
		return nil, false, nil
	}
	return decodeVector(blob), true, nil
}

// ChunkEmbedding is (message_id, chunk_index, vector) for bulk results.
type ChunkEmbedding struct {
	MessageID  string
	ChunkIndex int
	Vector     []float32
}

// GetChunkEmbeddingsForMessages loads every chunk's vector for the
// given message IDs in one query. Returns a map keyed by message_id;
// each value is ordered by chunk_index. Missing messages (no chunks
// or no embeddings under this model) are simply absent from the map.
func (d *DB) GetChunkEmbeddingsForMessages(messageIDs []string, modelID string) (map[string][]ChunkEmbedding, error) {
	if len(messageIDs) == 0 {
		return map[string][]ChunkEmbedding{}, nil
	}
	placeholders := ""
	args := make([]any, 0, len(messageIDs)+1)
	for i, id := range messageIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	args = append(args, modelID)
	query := fmt.Sprintf(`
		SELECT message_id, chunk_index, embedding
		FROM chunk_vectors
		WHERE message_id IN (%s) AND model_id = ?
		ORDER BY message_id, chunk_index
	`, placeholders)
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]ChunkEmbedding, len(messageIDs))
	for rows.Next() {
		var msgID string
		var chunkIdx int
		var blob []byte
		if err := rows.Scan(&msgID, &chunkIdx, &blob); err != nil {
			return nil, err
		}
		out[msgID] = append(out[msgID], ChunkEmbedding{
			MessageID:  msgID,
			ChunkIndex: chunkIdx,
			Vector:     decodeVector(blob),
		})
	}
	return out, rows.Err()
}

// AllChunkEmbeddingsForModel loads every (message_id, chunk_index,
// vector) under one embedder. Used by user-facing search features.
func (d *DB) AllChunkEmbeddingsForModel(modelID string) ([]ChunkEmbedding, error) {
	rows, err := d.Query(
		`SELECT message_id, chunk_index, embedding FROM chunk_vectors WHERE model_id = ?`,
		modelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkEmbedding
	for rows.Next() {
		var ce ChunkEmbedding
		var blob []byte
		if err := rows.Scan(&ce.MessageID, &ce.ChunkIndex, &blob); err != nil {
			return nil, err
		}
		ce.Vector = decodeVector(blob)
		out = append(out, ce)
	}
	return out, rows.Err()
}

// NearestChunkVectors runs sub-linear KNN against the vec0 ANN index.
// queryVec is encoded as the vec0 BLOB format (little-endian float32);
// k bounds the result set; predicateClause is the compiled SQL fragment
// for additional aux-column filters (compiled by the search package's
// predicate compiler) — empty means no extra filter beyond model_id.
//
// model_id is a vec0 partition key so `WHERE
// model_id = ?` is native — the index partitions by it and the KNN
// runs only within the chosen model's vectors. Aux columns
// (+message_id, +chunk_index, +thread_id, +role) can't appear in
// the KNN WHERE; if the caller needs to filter on those, the
// predicate compiler must produce a clause that vec0 accepts (or
// the schema must promote those columns to partition keys too).
//
// Returns chunks ordered by distance ascending (most similar first).
func (d *DB) NearestChunkVectors(queryVec []float32, k int, modelID, predicateClause string, predicateArgs []any) ([]ChunkVectorRow, error) {
	if k <= 0 {
		return nil, nil
	}
	q := `
		SELECT message_id, chunk_index, thread_id, model_id, role, embedding, distance
		FROM chunk_vectors
		WHERE embedding MATCH ? AND k = ?
		  AND model_id = ?`
	args := []any{encodeVector(queryVec), k, modelID}
	if predicateClause != "" {
		q += " AND (" + predicateClause + ")"
		args = append(args, predicateArgs...)
	}
	q += " ORDER BY distance"

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkVectorRow
	for rows.Next() {
		var r ChunkVectorRow
		var blob []byte
		var distance float64
		if err := rows.Scan(&r.MessageID, &r.ChunkIndex, &r.ThreadID, &r.ModelID, &r.Role, &blob, &distance); err != nil {
			return nil, err
		}
		r.Vector = decodeVector(blob)
		r.Distance = distance
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChunkVectorRow is the full row shape — all aux columns plus the
// vector + KNN distance.
type ChunkVectorRow struct {
	MessageID  string
	ChunkIndex int
	ThreadID   string
	ModelID    string
	Role       int
	Vector     []float32
	Distance   float64 // populated by NearestChunkVectors only
}

// ScanChunkVectors returns every chunk_vectors row matching a
// non-MATCH WHERE clause. Used for full-scan diagnostic / fallback
// paths; the production retrieval path uses NearestChunkVectors.
func (d *DB) ScanChunkVectors(whereClause string, args ...any) ([]ChunkVectorRow, error) {
	q := `SELECT message_id, chunk_index, thread_id, model_id, role, embedding
	      FROM chunk_vectors`
	if whereClause != "" {
		q += " WHERE " + whereClause
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkVectorRow
	for rows.Next() {
		var r ChunkVectorRow
		var blob []byte
		if err := rows.Scan(&r.MessageID, &r.ChunkIndex, &r.ThreadID, &r.ModelID, &r.Role, &blob); err != nil {
			return nil, err
		}
		r.Vector = decodeVector(blob)
		out = append(out, r)
	}
	return out, rows.Err()
}

func encodeVector(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func decodeVector(buf []byte) []float32 {
	vec := make([]float32, len(buf)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return vec
}

// EnsureEmbeddingDim reconciles the vector cache to the configured
// embedder's native dimension. Callers pass the probed dim (from a live
// embed of a canary string) and the model id, so storage stays free of any
// core/embedder dependency. embedding_meta is the single source of truth
// for the current dim.
//
//   - First run (no meta row) or dim unchanged: upsert the meta row, done.
//   - Dim changed: drop and recreate chunk_vectors (+ rowids + trigger) at
//     the new width via chunkVectorsDDL, in one transaction, then upsert.
//
// The drop touches only the derived vector cache; chunks/messages/threads/
// scores/edges are untouched (the "vector cache is rebuildable from chunks"
// invariant). After a rebuild the cache is empty, so the caller's normal
// backfill (ChunksMissingEmbeddings + BackfillEmbeddings) re-embeds the
// whole corpus under the new dim. Idempotent on reboot: unchanged dim is a
// no-op and backfill finds nothing missing.
func (d *DB) EnsureEmbeddingDim(dim int, modelID string) error {
	if dim <= 0 {
		return fmt.Errorf("EnsureEmbeddingDim: non-positive dim %d", dim)
	}

	var storedDim int
	err := d.QueryRow(`SELECT dim FROM embedding_meta WHERE id = 1`).Scan(&storedDim)
	if errors.Is(err, sql.ErrNoRows) {
		// First run — no meta row yet. The bootstrap chunk_vectors is at
		// defaultEmbeddingDim; if the probed dim differs we must still
		// rebuild, so fall through to the dim-change path rather than just
		// recording. Treat "no row" as stored == defaultEmbeddingDim.
		storedDim = defaultEmbeddingDim
	} else if err != nil {
		return fmt.Errorf("read embedding_meta: %w", err)
	}

	if storedDim == dim {
		return d.upsertEmbeddingMeta(dim, modelID)
	}

	// Dimension changed: rebuild the vector cache at the new width.
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS trg_cv_cascade_chunks`,
		`DROP TABLE IF EXISTS chunk_vectors`,
		`DROP TABLE IF EXISTS chunk_vector_rowids`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("drop vector cache: %w", err)
		}
	}
	if _, err := tx.Exec(chunkVectorsDDL(dim)); err != nil {
		return fmt.Errorf("recreate vector cache at dim %d: %w", dim, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO embedding_meta (id, dim, model_id, updated_at)
		 VALUES (1, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(id) DO UPDATE SET dim = excluded.dim, model_id = excluded.model_id, updated_at = CURRENT_TIMESTAMP`,
		dim, modelID,
	); err != nil {
		return fmt.Errorf("upsert embedding_meta: %w", err)
	}
	return tx.Commit()
}

func (d *DB) upsertEmbeddingMeta(dim int, modelID string) error {
	_, err := d.Exec(
		`INSERT INTO embedding_meta (id, dim, model_id, updated_at)
		 VALUES (1, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(id) DO UPDATE SET dim = excluded.dim, model_id = excluded.model_id, updated_at = CURRENT_TIMESTAMP`,
		dim, modelID,
	)
	return err
}
