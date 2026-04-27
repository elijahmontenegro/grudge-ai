package rrc

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// quotaOracle returns one chunk per message, every chunk's text is
// the message id (so unique), and every chunk has the same vector.
// That ties cosine for every prior chunk; the per-thread quota is
// what determines which chunks reach rerank, not cosine.
type quotaOracle struct{ ids []string }

func (q *quotaOracle) ChunksForMessages(_ context.Context, ids []string) (map[string][]ChunkRef, error) {
	out := make(map[string][]ChunkRef, len(ids))
	for _, id := range ids {
		out[id] = []ChunkRef{{
			MessageID: id, ChunkIndex: 0,
			Text:   id,
			Vector: []float32{1, 0, 0},
		}}
	}
	return out, nil
}
func (q *quotaOracle) EnsureVector(_ context.Context, _ ChunkRef) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

// threadAwareScorer scores chunks by their thread of origin: prior
// chunks whose text starts with "B-" get score 0.9 (relevant); all
// others get 0.0. The query text is irrelevant — what we're
// testing is whether thread-B chunks reach the reranker at all.
type threadAwareScorer struct {
	calls int
	seen  []string // candidate texts seen across all calls
}

func (s *threadAwareScorer) Score(_ context.Context, _ string, candidates []string) ([]float64, error) {
	s.calls++
	out := make([]float64, len(candidates))
	for i, c := range candidates {
		s.seen = append(s.seen, c)
		if len(c) >= 2 && c[:2] == "B-" {
			out[i] = 0.9
		} else {
			out[i] = 0.0
		}
	}
	return out, nil
}

// TestOnMessage_PerThreadQuota_SurfacesSmallThread — corpus
// dominated by a large thread (100 chunks) plus a small thread (10
// chunks). With MinPerThreadInTopK=0 (legacy global top-K), the
// large thread fills the rerank pool; the small thread's chunks
// never get scored. With MinPerThreadInTopK=8, each thread gets at
// least 8 slots; the small thread's chunks reach rerank, score
// high, and form edges.
//
// Pins the volume-bias fix: cross-thread recall depends on small
// threads not getting starved.
func TestOnMessage_PerThreadQuota_SurfacesSmallThread(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.RadiusSize = 0
	cfg.DiversityLambda = 0
	cfg.RerankTopK = 64

	makeMsg := func(id, threadID string, position int64) *pb.Message {
		return &pb.Message{
			Id:        id,
			Role:      pb.Role_ROLE_USER,
			Content:   []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: id}}}},
			Position:  position,
			ThreadId:  threadID,
			CreatedAt: timestamppb.Now(),
		}
	}

	corpus := []*pb.Message{}
	for i := 0; i < 100; i++ {
		corpus = append(corpus, makeMsg(fmt.Sprintf("A-%d", i), "thread-A", int64(i)))
	}
	for i := 0; i < 10; i++ {
		corpus = append(corpus, makeMsg(fmt.Sprintf("B-%d", i), "thread-B", int64(100+i)))
	}
	query := makeMsg("Q", "thread-Q", 200)
	corpus = append(corpus, query)

	ids := make([]string, 0, len(corpus))
	for _, m := range corpus {
		ids = append(ids, m.Id)
	}
	oracle := &quotaOracle{ids: ids}

	// Without quota: large thread starves the small one.
	t.Run("MinPerThreadInTopK=0 starves small thread", func(t *testing.T) {
		c := cfg
		c.MinPerThreadInTopK = 0
		scorer := &threadAwareScorer{}
		e := NewEngine(c, scorer, WithChunkOracle(oracle))
		edges, err := e.OnMessage(context.Background(), query, corpus)
		if err != nil {
			t.Fatalf("OnMessage: %v", err)
		}
		bSeen := 0
		for _, s := range scorer.seen {
			if len(s) >= 2 && s[:2] == "B-" {
				bSeen++
			}
		}
		if bSeen >= 5 {
			t.Errorf("MinPerThreadInTopK=0: expected B-chunks starved by volume, but %d B-chunks reached rerank", bSeen)
		}
		bEdges := 0
		for _, ed := range edges {
			if ed.FromThreadId == "thread-B" {
				bEdges++
			}
		}
		t.Logf("legacy: B-chunks reranked=%d, B-edges=%d (out of 10 B-chunks)", bSeen, bEdges)
	})

	// With quota: small thread gets reserved slots, its relevant
	// chunks reach rerank and form edges.
	t.Run("MinPerThreadInTopK=8 surfaces small thread", func(t *testing.T) {
		c := cfg
		c.MinPerThreadInTopK = 8
		scorer := &threadAwareScorer{}
		e := NewEngine(c, scorer, WithChunkOracle(oracle))
		edges, err := e.OnMessage(context.Background(), query, corpus)
		if err != nil {
			t.Fatalf("OnMessage: %v", err)
		}
		bSeen := 0
		for _, s := range scorer.seen {
			if len(s) >= 2 && s[:2] == "B-" {
				bSeen++
			}
		}
		if bSeen < 8 {
			t.Errorf("MinPerThreadInTopK=8: expected at least 8 B-chunks reranked (the quota), got %d", bSeen)
		}
		bEdges := 0
		for _, ed := range edges {
			if ed.FromThreadId == "thread-B" {
				bEdges++
			}
		}
		if bEdges == 0 {
			t.Errorf("MinPerThreadInTopK=8: expected B-thread to form edges (its chunks score 0.9), got %d", bEdges)
		}
		t.Logf("quota: B-chunks reranked=%d, B-edges=%d (out of 10 B-chunks)", bSeen, bEdges)
	})
}
