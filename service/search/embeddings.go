package search

import (
	"context"
	"math"
	"sort"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/service/annindex"
	"github.com/elijahmontenegro/grudge/service/chunkkey"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// Searcher runs user-facing semantic search over chunk embeddings. It shares
// the ChunkOracle's in-RAM ANN index (same embedder + model id), so a query
// walks the global graph (~O(log N)) instead of the old whole-corpus
// brute-force cosine scan. Results are grouped to message granularity, taking
// the best-scoring chunk per message. Approximate (the index's int8 rerank) —
// the deliberate cost of making search corpus-invariant; the wide over-fetch
// below keeps recall high for these final user-facing results.
type Searcher struct {
	embedder core.Embedder
	model    string
	db       *storage.DB
	index    *annindex.Index
}

// NewSearcher creates a Searcher over the given embedder, model id (for the
// write side), and the shared ANN index (for the read side).
func NewSearcher(embedder core.Embedder, model string, db *storage.DB, index *annindex.Index) *Searcher {
	return &Searcher{embedder: embedder, model: model, db: db, index: index}
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

// Search shortlist sizing: user-facing hits are final (not RRC candidates), so
// over-fetch wider than the RRC path and widen until `limit` distinct messages
// are covered — the index ranks chunks and one message owns several. Bounded by
// searchWidenCap so the search stays sub-linear.
const (
	searchOverfetch    = 16
	searchMinShortlist = 128
	searchWidenCap     = 4096
)

// Search embeds the query, walks the shared global ANN graph, and returns the
// best-scoring chunk per message, ranked. See the Searcher doc for the
// approximate-but-corpus-invariant tradeoff vs the former brute-force scan.
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if s.index == nil {
		return nil, nil
	}
	vecs, err := s.embedder.Embed(ctx, core.RoleQuery, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, nil
	}
	qVec := vecs[0]
	qNorm := l2norm(qVec)

	// Group by message keeping the best chunk, ranked by the RAW asymmetric
	// score (not the [0,1]-clamped display score, which would collapse ties
	// among strong matches). normalizedScore is applied only to the value the
	// API surfaces.
	type scored struct {
		res SearchResult
		raw float64
	}
	start := max(limit*searchOverfetch, searchMinShortlist)
	best := make(map[string]scored)
	for m := start; ; m *= 2 {
		cands, _ := s.index.Search(qVec, m, "") // "" = global graph, all threads
		best = make(map[string]scored, len(cands))
		for _, c := range cands {
			mid, cidx := chunkkey.Split(c.Key)
			if ex, ok := best[mid]; !ok || c.Score > ex.raw {
				best[mid] = scored{
					res: SearchResult{MessageID: mid, ChunkIdx: cidx, Score: normalizedScore(c.Score, qNorm)},
					raw: c.Score,
				}
			}
		}
		if len(best) >= limit || len(cands) < m || m >= searchWidenCap {
			break
		}
	}

	results := make([]scored, 0, len(best))
	for _, r := range best {
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].raw > results[j].raw })
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = r.res
	}
	return out, nil
}

// l2norm and normalizedScore map the index's raw asymmetric score
// (query·dequant(vec)) to a [0,1] cosine-like value, preserving the score
// semantics the old cosine scan exposed through the GraphQL Search API.
func l2norm(v []float32) float64 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	return math.Sqrt(n)
}

func normalizedScore(raw, qNorm float64) float64 {
	if qNorm == 0 {
		return 0
	}
	s := raw / qNorm
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}
