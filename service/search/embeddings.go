package search

import (
	"context"
	"math"
	"sort"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// Searcher runs user-facing semantic search over chunk embeddings.
// Chunks live in the storage layer keyed by (message_id, chunk_index,
// model_id). For search, we embed the query, score all chunks by
// cosine, group by message_id, take the best-scoring chunk per
// message as the message's score. Brute-force across all chunks is
// fine at this project's scale; an ANN index would be the next step
// once the corpus reaches 100k+ chunks.
type Searcher struct {
	embedder core.Embedder
	model    string
	db       *storage.DB
}

// NewSearcher creates a Searcher with the given embedder and the model
// identifier to tag cached rows with (and filter by on read).
func NewSearcher(embedder core.Embedder, model string, db *storage.DB) *Searcher {
	return &Searcher{embedder: embedder, model: model, db: db}
}

// Model returns the embedder model identifier this searcher
// reads/writes under. Exposed so backfill uses the same tag.
func (s *Searcher) Model() string { return s.model }

// EmbedMessageChunks embeds every chunk of a message and writes them
// to the cache under this Searcher's model_id. Called from the
// post-insert pipeline so the corpus stays chunk-vectorized and RRC's
// rerank cosine prefilter hits cache.
func (s *Searcher) EmbedMessageChunks(ctx context.Context, messageID string) error {
	chunks, err := s.db.GetChunks(messageID)
	if err != nil || len(chunks) == 0 {
		return err
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	vecs, err := s.embedder.Embed(ctx, core.RoleDocument, texts)
	if err != nil {
		return err
	}
	for i, c := range chunks {
		if i >= len(vecs) {
			break
		}
		if err := s.db.InsertChunkEmbedding(messageID, c.ChunkIndex, s.model, vecs[i]); err != nil {
			return err
		}
	}
	return nil
}

// SearchResult is a single search hit at message granularity.
type SearchResult struct {
	MessageID string
	ChunkIdx  int // best-matching chunk within the message
	Score     float64
}

// Search embeds the query and ranks every cached chunk by cosine.
// Groups by message_id, taking the best chunk score per message.
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	vecs, err := s.embedder.Embed(ctx, core.RoleQuery, []string{query})
	if err != nil {
		return nil, err
	}
	queryVec := vecs[0]

	embs, err := s.db.AllChunkEmbeddingsForModel(s.model)
	if err != nil {
		return nil, err
	}

	best := make(map[string]SearchResult, len(embs))
	for _, ce := range embs {
		score := cosineSimilarity(queryVec, ce.Vector)
		existing, ok := best[ce.MessageID]
		if !ok || score > existing.Score {
			best[ce.MessageID] = SearchResult{
				MessageID: ce.MessageID,
				ChunkIdx:  ce.ChunkIndex,
				Score:     score,
			}
		}
	}

	results := make([]SearchResult, 0, len(best))
	for _, r := range best {
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
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
