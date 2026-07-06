package rrcbench

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/oracle"
	"github.com/elijahmontenegro/grudge/service/storage"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// insertOneVectorMessage plants a single embedded chunk-message in threadID.
func insertOneVectorMessage(db *storage.DB, threadID, id string, pos int, dim int) error {
	text := benchText(pos)
	msg := &threadv1.Message{
		Id: id, ThreadId: threadID, Role: threadv1.Role_ROLE_USER,
		Content:   []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}}},
		Position:  int64(pos),
		CreatedAt: timestamppb.New(benchBaseTime.Add(time.Duration(pos) * time.Second)),
	}
	if err := db.InsertMessage(msg, []storage.Chunk{{MessageID: id, ChunkIndex: 0, Text: text, ByteEnd: len(text)}}); err != nil {
		return err
	}
	return db.InsertChunkEmbedding(id, 0, benchModel, unitVector(id, dim))
}

// TestInvariance_ThreadScopedNearestChunks probes the case the StageLatencyCurve
// never exercises: THREAD-scoped retrieval where the active thread is a small
// fraction of a large multi-thread corpus. The target thread holds a FIXED 20
// chunks (< RerankTopK=64), so the predicate can never fill the shortlist and
// NearestChunks' widening loop must widen until it exhausts the whole index.
// If per-query latency grows with total N while the target thread stays fixed,
// thread-scoped retrieval is O(N) — the invariance fix does not hold here.
func TestInvariance_ThreadScopedNearestChunks(t *testing.T) {
	if os.Getenv("RRC_BENCH") == "" {
		t.Skip("perf probe; set RRC_BENCH=1 to run")
	}
	const targetThread = "target"
	const targetSize = 20 // < RerankTopK, so eligible can never reach k
	ctx := context.Background()

	fillerSizes := []int{1000, 4000, 16000}
	t.Logf("%-8s %12s %9s   (target thread fixed at %d chunks; flat => invariant, grows => O(N))",
		"fillerN", "nearest_ms", "vs_prev", targetSize)
	var prev float64
	for _, fillerN := range fillerSizes {
		db, err := storage.Open(t.TempDir())
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		if err := db.CreateThread(&threadv1.Thread{Id: targetThread, CreatedAt: timestamppb.New(benchBaseTime)}); err != nil {
			t.Fatalf("create target thread: %v", err)
		}
		for i := range targetSize {
			if err := insertOneVectorMessage(db, targetThread, fmt.Sprintf("target-%04d", i), i, benchDim); err != nil {
				t.Fatalf("insert target msg: %v", err)
			}
		}
		// Filler spread across many threads, none of them the target.
		if err := db.CreateThread(&threadv1.Thread{Id: "filler", CreatedAt: timestamppb.New(benchBaseTime)}); err != nil {
			t.Fatalf("create filler thread: %v", err)
		}
		for i := range fillerN {
			if err := insertOneVectorMessage(db, "filler", fmt.Sprintf("filler-%07d", i), targetSize+i, benchDim); err != nil {
				t.Fatalf("insert filler msg: %v", err)
			}
		}

		o, err := oracle.NewChunkOracle(db, hashEmbedder{dim: benchDim, calls: new(int64)}, benchModel)
		if err != nil {
			t.Fatalf("build oracle: %v", err)
		}
		pred := rrc.PredThread{ThreadID: targetThread}
		ms := timeIt(10, func() {
			_, _ = o.NearestChunks(ctx, "some query about topic 5", 64, pred)
		})
		ratio := 0.0
		if prev > 0 {
			ratio = ms / prev
		}
		t.Logf("%-8d %12.3f %9.2f", fillerN, ms, ratio)
		prev = ms
		db.Close()
	}
}
