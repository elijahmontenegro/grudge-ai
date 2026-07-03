package oracle

import (
	"context"
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
	for i := 0; i < 4; i++ {
		id := "foreign-" + string(rune('a'+i))
		insertVectorMessage(t, db, id, "foreign", int64(i), basisVector(0))
	}
	eligibleVector := make([]float32, 1024)
	eligibleVector[0], eligibleVector[1] = 0.8, 0.2
	insertVectorMessage(t, db, "eligible", "current", 0, eligibleVector)

	oracle := NewChunkOracle(db, fixedEmbedder{vector: queryVector}, "model")
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

	oracle := NewChunkOracle(db, fixedEmbedder{vector: basisVector(0)}, "model")
	got, err := oracle.NearestChunks(t.Context(), "query", 1, rrc.PredExcludeMessageIDs{MessageIDs: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != "candidate" {
		t.Fatalf("local exclusion returned %+v", got)
	}
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
