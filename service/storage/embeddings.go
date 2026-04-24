package storage

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Embedding cache — one vector per (chunk, embedder model) triple.
// Keys at chunk granularity because message-level embedding silently
// truncated at the model's context limit under the old schema; the
// character bible (24KB) only had its first 2KB embedded. Chunking
// means each paragraph-sized slice gets its own vector and the model's
// attention is spent on a coherent unit rather than averaged across
// the whole document.
//
// Encoding: little-endian float32 blobs. Decoded into []float32 on
// read. No compression — bge-m3 dense vectors are 1024-dim ≈ 4 KB;
// 10k chunks ≈ 40 MB. Disk is cheap.

// InsertChunkEmbedding stores a vector for a (message, chunk, model)
// triple. INSERT OR REPLACE so re-backfills don't collide and chunk
// updates supersede cleanly.
func (d *DB) InsertChunkEmbedding(messageID string, chunkIndex int, modelID string, vec []float32) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO embeddings (message_id, chunk_index, model_id, vector) VALUES (?, ?, ?, ?)`,
		messageID, chunkIndex, modelID, encodeVector(vec),
	)
	return err
}

// GetChunkEmbedding returns (vec, true) if cached, (nil, false) if
// not. For one-off lookups; use GetChunkEmbeddingsForMessages for
// hot-path batched access.
func (d *DB) GetChunkEmbedding(messageID string, chunkIndex int, modelID string) ([]float32, bool, error) {
	var blob []byte
	err := d.QueryRow(
		`SELECT vector FROM embeddings WHERE message_id = ? AND chunk_index = ? AND model_id = ?`,
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
// Primary hot-path loader for OnMessage's cosine prefilter.
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
		SELECT message_id, chunk_index, vector
		FROM embeddings
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

// ChunksMissingEmbeddings returns the chunks that have no vector
// cached under the given model. Drives startup backfill and catch-up
// after model swaps. Resumable — running twice in a row returns the
// shrinking residual.
type ChunkMissing struct {
	MessageID  string
	ChunkIndex int
	Text       string
}

func (d *DB) ChunksMissingEmbeddings(modelID string) ([]ChunkMissing, error) {
	rows, err := d.Query(`
		SELECT c.message_id, c.chunk_index, c.text
		FROM chunks c
		LEFT JOIN embeddings e
		  ON e.message_id = c.message_id
		 AND e.chunk_index = c.chunk_index
		 AND e.model_id = ?
		WHERE e.message_id IS NULL
		ORDER BY c.message_id, c.chunk_index
	`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkMissing
	for rows.Next() {
		var cm ChunkMissing
		if err := rows.Scan(&cm.MessageID, &cm.ChunkIndex, &cm.Text); err != nil {
			return nil, err
		}
		if cm.Text == "" {
			continue
		}
		out = append(out, cm)
	}
	return out, rows.Err()
}

// AllChunkEmbeddingsForModel loads every (message_id, chunk_index,
// vector) under one embedder. Used by search features; brute-force
// cosine is fine at current scale.
func (d *DB) AllChunkEmbeddingsForModel(modelID string) ([]ChunkEmbedding, error) {
	rows, err := d.Query(
		`SELECT message_id, chunk_index, vector FROM embeddings WHERE model_id = ?`,
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
