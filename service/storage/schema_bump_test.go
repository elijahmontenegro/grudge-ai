package storage

import (
	"testing"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestTurnID_RoundTrips confirms the active-discourse identity column
// survives insert → GetMessage / ListMessages / AllCorpus / LatestMessage.
func TestTurnID_RoundTrips(t *testing.T) {
	db := testDB(t)
	db.CreateThread(&threadv1.Thread{Id: "t1", Name: "T", CreatedAt: timestamppb.Now()})

	msg := &threadv1.Message{
		Id: "m1", ThreadId: "t1", Role: threadv1.Role_ROLE_USER,
		Content:   []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hi"}}}},
		Position:  0,
		CreatedAt: timestamppb.Now(),
		TurnId:    "turn-abc",
	}
	if err := db.InsertMessage(msg, nil); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetMessage("m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TurnId != "turn-abc" {
		t.Fatalf("GetMessage turn_id = %q, want turn-abc", got.TurnId)
	}

	list, err := db.ListMessages("t1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TurnId != "turn-abc" {
		t.Fatalf("ListMessages turn_id not preserved: %+v", list)
	}

	all, err := db.AllCorpus()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].TurnId != "turn-abc" {
		t.Fatalf("AllCorpus turn_id not preserved: %+v", all)
	}

	if latest := db.LatestMessage("t1"); latest == nil || latest.TurnId != "turn-abc" {
		t.Fatalf("LatestMessage turn_id not preserved: %+v", latest)
	}
}

// TestTurnID_DefaultsEmpty confirms a message stored without a turn_id
// reads back as "" (the column default), not an error. Uses content-bearing
// messages because GetMessage on an empty-content row trips a pre-existing
// ncruces empty-BLOB read quirk unrelated to this column (see NOTE in the
// A0 schema-bump work — latent, predates turn_id).
func TestTurnID_DefaultsEmpty(t *testing.T) {
	db := testDB(t)
	db.CreateThread(&threadv1.Thread{Id: "t1", Name: "T", CreatedAt: timestamppb.Now()})
	if err := db.InsertMessage(&threadv1.Message{
		Id: "m1", ThreadId: "t1", Role: threadv1.Role_ROLE_USER, Position: 0,
		Content:   []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "x"}}}},
		CreatedAt: timestamppb.Now(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage("m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TurnId != "" {
		t.Fatalf("turn_id = %q, want empty", got.TurnId)
	}
}

// TestProvenanceEdge_CoexistsWithCrossEncoder confirms the schema PK
// (from, to, source) lets a provenance edge and a cross-encoder edge
// share the same message pair — required for A1, where provenance is
// recorded alongside similarity, not clobbering it.
func TestProvenanceEdge_CoexistsWithCrossEncoder(t *testing.T) {
	db := testDB(t)
	db.CreateThread(&threadv1.Thread{Id: "t1", Name: "T", CreatedAt: timestamppb.Now()})

	ce := &rrcv1.Edge{
		FromMessageId: "a", ToMessageId: "b", Score: 0.7,
		Source: rrcv1.EdgeSource_EDGE_SOURCE_CROSS_ENCODER, CrossEncoderScore: 0.7,
		DetectedAt: timestamppb.Now(), FromThreadId: "t1", ToThreadId: "t1",
	}
	prov := &rrcv1.Edge{
		FromMessageId: "a", ToMessageId: "b", Score: 0.9,
		Source: rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE, CrossEncoderScore: 0,
		DetectedAt: timestamppb.Now(), FromThreadId: "t1", ToThreadId: "t1",
	}
	if err := db.InsertEdge(ce); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertEdge(prov); err != nil {
		t.Fatal(err)
	}

	edges, err := db.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	var sawCE, sawProv bool
	for _, e := range edges {
		if e.FromMessageId != "a" || e.ToMessageId != "b" {
			continue
		}
		switch e.Source {
		case rrcv1.EdgeSource_EDGE_SOURCE_CROSS_ENCODER:
			sawCE = true
		case rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE:
			sawProv = true
		}
	}
	if !sawCE || !sawProv {
		t.Fatalf("expected both CE and provenance edges to coexist; sawCE=%v sawProv=%v (total %d)", sawCE, sawProv, len(edges))
	}
}

// TestEnsureEmbeddingDim_FirstRunAndNoOp confirms the meta row is recorded
// on first probe and an unchanged dim is a no-op that leaves data intact.
func TestEnsureEmbeddingDim_FirstRunAndNoOp(t *testing.T) {
	db := testDB(t)

	// First run at the bootstrap dim: records meta, no rebuild.
	if err := db.EnsureEmbeddingDim(defaultEmbeddingDim, "model-a"); err != nil {
		t.Fatalf("first EnsureEmbeddingDim: %v", err)
	}
	var dim int
	var model string
	if err := db.QueryRow(`SELECT dim, model_id FROM embedding_meta WHERE id = 1`).Scan(&dim, &model); err != nil {
		t.Fatalf("read embedding_meta: %v", err)
	}
	if dim != defaultEmbeddingDim || model != "model-a" {
		t.Fatalf("meta = (%d, %q), want (%d, model-a)", dim, model, defaultEmbeddingDim)
	}

	// Same dim again: no-op, just refreshes model_id.
	if err := db.EnsureEmbeddingDim(defaultEmbeddingDim, "model-b"); err != nil {
		t.Fatalf("no-op EnsureEmbeddingDim: %v", err)
	}
	if err := db.QueryRow(`SELECT model_id FROM embedding_meta WHERE id = 1`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "model-b" {
		t.Fatalf("model_id = %q, want model-b", model)
	}
}

// TestEnsureEmbeddingDim_RebuildsOnChange confirms a dimension change drops
// and recreates chunk_vectors at the new width while leaving the source of
// truth (chunks, messages) intact — the "vector cache is rebuildable" invariant.
func TestEnsureEmbeddingDim_RebuildsOnChange(t *testing.T) {
	db := testDB(t)
	db.CreateThread(&threadv1.Thread{Id: "t1", Name: "T", CreatedAt: timestamppb.Now()})
	if err := db.InsertMessage(&threadv1.Message{
		Id: "m1", ThreadId: "t1", Role: threadv1.Role_ROLE_USER, Position: 0,
		Content:   []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hello"}}}},
		CreatedAt: timestamppb.Now(),
	}, []Chunk{{MessageID: "m1", ChunkIndex: 0, Text: "hello", ByteStart: 0, ByteEnd: 5, TokenEst: 1}}); err != nil {
		t.Fatal(err)
	}
	// Seed a bootstrap-width vector so we can prove the rebuild clears it.
	if err := db.InsertChunkEmbedding("m1", 0, "model-a", unitVec1024(0)); err != nil {
		t.Fatal(err)
	}

	// Change to a new dimension.
	const newDim = 3072
	if err := db.EnsureEmbeddingDim(newDim, "gemini-embedding-001"); err != nil {
		t.Fatalf("rebuild EnsureEmbeddingDim: %v", err)
	}

	// Meta reflects the new dim.
	var dim int
	if err := db.QueryRow(`SELECT dim FROM embedding_meta WHERE id = 1`).Scan(&dim); err != nil {
		t.Fatal(err)
	}
	if dim != newDim {
		t.Fatalf("meta dim = %d, want %d", dim, newDim)
	}

	// chunk_vectors is empty (cache dropped) but a new-width insert works.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunk_vectors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("chunk_vectors should be empty after rebuild, got %d rows", count)
	}
	newVec := make([]float32, newDim)
	newVec[0] = 1
	if err := db.InsertChunkEmbedding("m1", 0, "gemini-embedding-001", newVec); err != nil {
		t.Fatalf("insert at new dim: %v", err)
	}

	// Source of truth survived the rebuild.
	if got, err := db.GetMessage("m1"); err != nil || got == nil {
		t.Fatalf("message lost across rebuild: %v", err)
	}
	chunks, err := db.GetChunks("m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Text != "hello" {
		t.Fatalf("chunks lost across rebuild: %+v", chunks)
	}
}
