package oracle

import (
	"context"
	"fmt"
	"testing"

	"github.com/elijahmontenegro/grudge/core"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/storage"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fixedEmbedder struct {
	vector []float32
}

func (f fixedEmbedder) Embed(context.Context, core.EmbedRole, []string) ([][]float32, error) {
	return [][]float32{f.vector}, nil
}

func TestNearestChunksAdaptivelyFindsEligibleCandidate(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, threadID := range []string{"current", "foreign"} {
		if err := db.CreateThread(&threadv1.Thread{Id: threadID, CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	queryVector := basisVector(0)
	for i := range 4 {
		id := "foreign-" + string(rune('a'+i))
		insertVectorMessage(t, db, id, "foreign", int64(i), basisVector(0))
	}
	eligibleVector := make([]float32, 1024)
	eligibleVector[0], eligibleVector[1] = 0.8, 0.2
	insertVectorMessage(t, db, "eligible", "current", 0, eligibleVector)

	oracle, err := NewChunkOracle(db, fixedEmbedder{vector: queryVector}, "model")
	if err != nil {
		t.Fatal(err)
	}
	got, err := oracle.NearestChunks(t.Context(), "query", 1, rrc.PredThread{ThreadID: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != "eligible" {
		t.Fatalf("adaptive filter returned %+v, want eligible current-thread candidate", got)
	}
}

func TestNearestChunksExcludesLocalMessagesWithoutLosingK(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateThread(&threadv1.Thread{Id: "t1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	insertVectorMessage(t, db, "local", "t1", 0, basisVector(0))
	candidate := make([]float32, 1024)
	candidate[0], candidate[1] = 0.8, 0.2
	insertVectorMessage(t, db, "candidate", "t1", 1, candidate)

	oracle, err := NewChunkOracle(db, fixedEmbedder{vector: basisVector(0)}, "model")
	if err != nil {
		t.Fatal(err)
	}
	got, err := oracle.NearestChunks(t.Context(), "query", 1, rrc.PredExcludeMessageIDs{MessageIDs: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != "candidate" {
		t.Fatalf("local exclusion returned %+v", got)
	}
}

// TestNearestChunksWidensPastExcludedShortlist is the regression guard for the
// under-return a single fetch introduces under THREAD scope: the excluded
// local-context messages are the most similar, so they can fill the entire
// first k*annOverfetch shortlist, leaving a real candidate ranked just past it
// unreachable. Here 10 top-scoring messages (all on the query) are all excluded
// and one lower-scoring candidate sits below them; the first shortlist (k=1 ->
// 8) is entirely excluded, so the widen loop must fetch further — within the
// thread partition — to surface the candidate. A single fetch returns nothing.
func TestNearestChunksWidensPastExcludedShortlist(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateThread(&threadv1.Thread{Id: "t1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	// 10 excluded messages exactly on the query (score 1.0) — more than the
	// k*annOverfetch=8 first shortlist, so they fill it entirely.
	excluded := make([]string, 0, 10)
	for i := range 10 {
		id := fmt.Sprintf("local-%d", i)
		insertVectorMessage(t, db, id, "t1", int64(i), basisVector(0))
		excluded = append(excluded, id)
	}
	// One real candidate in the same thread, ranked strictly below the excluded.
	cand := make([]float32, 1024)
	cand[0], cand[1] = 0.8, 0.2
	insertVectorMessage(t, db, "candidate", "t1", 10, cand)

	oracle, err := NewChunkOracle(db, fixedEmbedder{vector: basisVector(0)}, "model")
	if err != nil {
		t.Fatal(err)
	}
	// Production predicate shape: thread scope + local-context exclusion.
	predicate := rrc.PredAnd{Children: []rrc.Predicate{
		rrc.PredScope{CurrentThread: "t1", Scope: rrc.ScopeThread},
		rrc.PredExcludeMessageIDs{MessageIDs: excluded},
	}}
	got, err := oracle.NearestChunks(t.Context(), "query", 1, predicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != "candidate" {
		t.Fatalf("widen-past-excluded returned %+v, want [candidate] — a single fetch would return nothing", got)
	}
}

// TestNearestChunksWidensPastExcludedShortlist_AllScope is the same under-return
// but under ALL_THREADS scope (the interactive-chat default): a large current
// turn fills the first shortlist in the global graph too. If ALL scope refuses
// to widen, it returns nothing.
func TestNearestChunksWidensPastExcludedShortlist_AllScope(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateThread(&threadv1.Thread{Id: "t1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	excluded := make([]string, 0, 10)
	for i := range 10 {
		id := fmt.Sprintf("local-%d", i)
		insertVectorMessage(t, db, id, "t1", int64(i), basisVector(0))
		excluded = append(excluded, id)
	}
	cand := make([]float32, 1024)
	cand[0], cand[1] = 0.8, 0.2
	insertVectorMessage(t, db, "candidate", "t1", 10, cand)

	oracle, err := NewChunkOracle(db, fixedEmbedder{vector: basisVector(0)}, "model")
	if err != nil {
		t.Fatal(err)
	}
	predicate := rrc.PredAnd{Children: []rrc.Predicate{
		rrc.PredScope{CurrentThread: "t1", Scope: rrc.ScopeAll},
		rrc.PredExcludeMessageIDs{MessageIDs: excluded},
	}}
	got, err := oracle.NearestChunks(t.Context(), "query", 1, predicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != "candidate" {
		t.Fatalf("all-scope widen-past-excluded returned %+v, want [candidate]", got)
	}
}

// TestIndexAddIgnoresOtherModels: the index is per-model, and the embedding
// observer fires for every model's inserts, so IndexAdd must drop any embedding
// written under a different model (mixing models/widths would corrupt distances).
func TestIndexAddIgnoresOtherModels(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oracle, err := NewChunkOracle(db, fixedEmbedder{vector: basisVector(0)}, "model")
	if err != nil {
		t.Fatal(err)
	}
	oracle.IndexAdd("mine", 0, "model", "t1", basisVector(0))
	oracle.IndexAdd("theirs", 0, "other-model", "t1", basisVector(0))
	if !oracle.index.Has(chunkKeyStr("mine", 0)) {
		t.Fatal("same-model chunk was not indexed")
	}
	if oracle.index.Has(chunkKeyStr("theirs", 0)) {
		t.Fatal("other-model chunk was indexed — the observer is not model-scoped")
	}
}

func (o *ChunkOracle) resetExamined()      { o.examined.Store(0) }
func (o *ChunkOracle) examinedCount() int64 { return o.examined.Load() }

// TestNearestChunksWorkInvariant is the deterministic guard the partition-graph
// guard could not provide: it drives the FULL NearestChunks path (routing +
// widen + filter), not Search directly, and asserts the number of candidates it
// examines for a fixed thread is byte-identical as OTHER threads accumulate. A
// regression that routed THREAD scope to the global graph (or widened it) would
// make this grow with filler — which is exactly the O(N) leak this whole effort
// closed, and which no default guard previously covered.
func TestNearestChunksWorkInvariant(t *testing.T) {
	const target = "target"
	const targetSize = 30
	var want int64
	for fi, filler := range []int{0, 500, 2000} {
		db, err := storage.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateThread(&threadv1.Thread{Id: target, CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
		excluded := make([]string, 0, 5)
		for i := range targetSize {
			id := fmt.Sprintf("target-%d", i)
			insertVectorMessage(t, db, id, target, int64(i), basisVector(0))
			if i < 5 {
				excluded = append(excluded, id)
			}
		}
		if filler > 0 {
			if err := db.CreateThread(&threadv1.Thread{Id: "filler", CreatedAt: timestamppb.Now()}); err != nil {
				t.Fatal(err)
			}
			for i := range filler {
				insertVectorMessage(t, db, fmt.Sprintf("f-%d", i), "filler", int64(targetSize+i), basisVector(0))
			}
		}
		oracle, err := NewChunkOracle(db, fixedEmbedder{vector: basisVector(0)}, "model")
		if err != nil {
			t.Fatal(err)
		}
		predicate := rrc.PredAnd{Children: []rrc.Predicate{
			rrc.PredScope{CurrentThread: target, Scope: rrc.ScopeThread},
			rrc.PredExcludeMessageIDs{MessageIDs: excluded},
		}}
		oracle.resetExamined()
		if _, err := oracle.NearestChunks(t.Context(), "query", 8, predicate); err != nil {
			t.Fatal(err)
		}
		got := oracle.examinedCount()
		db.Close()
		if fi == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("filler=%d: NearestChunks examined %d candidates, want %d — THREAD-scope work grew with other-thread filler (O(N) leak)",
				filler, got, want)
		}
	}
	if want == 0 {
		t.Fatal("examined 0 candidates — misconfigured")
	}
	t.Logf("THREAD-scope NearestChunks examined %d candidates, invariant across filler [0 500 2000]", want)
}

func insertVectorMessage(t *testing.T, db *storage.DB, id, thread string, position int64, vector []float32) {
	t.Helper()
	message := &threadv1.Message{
		Id: id, ThreadId: thread, Position: position, Role: threadv1.Role_ROLE_USER,
		Content:   []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: id}}}},
		CreatedAt: timestamppb.Now(),
	}
	if err := db.InsertMessage(message, []storage.Chunk{{
		ChunkIndex: 0, Text: id, ByteEnd: len(id), TokenEst: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertChunkEmbedding(id, 0, "model", vector); err != nil {
		t.Fatal(err)
	}
}

func basisVector(index int) []float32 {
	vector := make([]float32, 1024)
	vector[index] = 1
	return vector
}
