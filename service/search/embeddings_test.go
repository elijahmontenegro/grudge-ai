package search

import (
	"context"
	"testing"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/service/annindex"
	"github.com/elijahmontenegro/grudge/service/chunkkey"
)

// stubEmbedder returns a fixed query vector, so the test controls what the
// index ranks against without a real embedder service.
type stubEmbedder struct{ vec []float32 }

func (s stubEmbedder) Embed(_ context.Context, _ core.EmbedRole, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = s.vec
	}
	return out, nil
}

// TestSearcherGroupsByMessage builds a small index (one message with two
// chunks, one with a single chunk) and checks that Search collapses to message
// granularity keeping the best chunk, ranks by score, and honours the limit.
func TestSearcherGroupsByMessage(t *testing.T) {
	ix := annindex.New(annindex.Config{Seed: 1})
	q := []float32{1, 1, 1, 1}
	ix.Add(chunkkey.Make("m1", 0), "", []float32{-1, -1, -1, -1}) // far from q
	ix.Add(chunkkey.Make("m1", 1), "", []float32{1, 1, 1, 1})     // exact match — m1's best
	ix.Add(chunkkey.Make("m2", 0), "", []float32{1, 1, 1, -1})    // near-ish
	s := NewSearcher(stubEmbedder{vec: q}, "model", nil, ix)

	res, err := s.Search(context.Background(), "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2 distinct messages (grouped)", len(res))
	}
	byMsg := map[string]SearchResult{}
	for _, r := range res {
		byMsg[r.MessageID] = r
	}
	if byMsg["m1"].ChunkIdx != 1 {
		t.Fatalf("m1 best chunk = %d, want 1 (the exact match)", byMsg["m1"].ChunkIdx)
	}
	if res[0].MessageID != "m1" {
		t.Fatalf("top result = %s, want m1 (exact match ranks first)", res[0].MessageID)
	}
	if res[0].Score < res[1].Score {
		t.Fatalf("results not sorted descending by score: %+v", res)
	}

	// Limit is respected.
	res1, err := s.Search(context.Background(), "query", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res1) != 1 || res1[0].MessageID != "m1" {
		t.Fatalf("limit=1 got %+v, want [m1]", res1)
	}
}

// TestSearcherNilIndex: a Searcher with no index returns empty, not a panic
// (search-unavailable is handled at the resolver, but be defensive).
func TestSearcherNilIndex(t *testing.T) {
	s := NewSearcher(stubEmbedder{vec: []float32{1}}, "model", nil, nil)
	res, err := s.Search(context.Background(), "q", 5)
	if err != nil || res != nil {
		t.Fatalf("nil-index Search = (%v, %v), want (nil, nil)", res, err)
	}
}
