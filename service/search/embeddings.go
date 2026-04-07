package search

import (
	"context"
	"encoding/binary"
	"math"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/service/storage"
)

// Searcher provides semantic search across all threads.
type Searcher struct {
	embedder core.Embedder
	model    string
	db       *storage.DB
}

// NewSearcher creates a Searcher with the given embedder.
func NewSearcher(embedder core.Embedder, model string, db *storage.DB) *Searcher {
	return &Searcher{embedder: embedder, model: model, db: db}
}

// EmbedMessage embeds a message text and stores the vector.
func (s *Searcher) EmbedMessage(ctx context.Context, messageID, text string) error {
	vec, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return err
	}

	blob := encodeVector(vec)
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO embeddings (message_id, vector, model) VALUES (?, ?, ?)`,
		messageID, blob, s.model,
	)
	return err
}

// SearchResult is a single search hit.
type SearchResult struct {
	MessageID string
	Score     float64
}

// Search performs cosine similarity search across all stored embeddings.
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	queryVec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT message_id, vector FROM embeddings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var msgID string
		var blob []byte
		if err := rows.Scan(&msgID, &blob); err != nil {
			return nil, err
		}

		vec := decodeVector(blob)
		score := cosineSimilarity(queryVec, vec)
		results = append(results, SearchResult{MessageID: msgID, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by score descending
	sortResults(results)

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

func sortResults(results []SearchResult) {
	// Simple insertion sort — result sets are small
	for i := 1; i < len(results); i++ {
		key := results[i]
		j := i - 1
		for j >= 0 && results[j].Score < key.Score {
			results[j+1] = results[j]
			j--
		}
		results[j+1] = key
	}
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
