package storage

import (
	"math"
	"testing"

	pb "github.com/emontenegr/spidey/proto/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestVec0Cosine_RoundTrip pins migrationV5's distance_metric=cosine
// declaration. Without it sqlite-vec defaults to L2, and the
// engine's Layer-1-score consumer (rrccache.NearestChunks) computes
// similarity as `1.0 - row.Distance`. That conversion is correct
// only when row.Distance IS cosine distance — under L2 with
// unit-normalized vectors the *ordering* matches cosine but the
// returned distance value is squared Euclidean, so `1 - distance`
// is wrong.
//
// This test inserts a known pair (query vec, two candidates with
// analytically computable cosine distances), runs NearestChunkVectors,
// and asserts `row.Distance` matches the analytical cosine distance
// to floating-point precision. If migrationV5 regresses (someone
// drops the distance_metric clause), this fails immediately.
//
// Vectors must be 1024-dim to match the schema (chunk_vectors
// declares FLOAT[1024]); we use one-hot vectors with two basis
// dimensions to keep cosine analytical: dot products are 0 or 1,
// norms are exactly 1, so cosine_distance = 1 - dot.
func TestVec0Cosine_RoundTrip(t *testing.T) {
	db := testDB(t)

	// Schema requires a thread + messages to exist before
	// InsertChunkEmbedding can populate chunk_vectors (FK lookup
	// against messages for thread_id / role).
	if err := db.CreateThread(&pb.Thread{
		Id: "tCos", Name: "cosine probe", CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for _, id := range []string{"q", "near", "far"} {
		msg := &pb.Message{
			Id: id, ThreadId: "tCos", Role: pb.Role_ROLE_USER,
			Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{
				Text: &pb.TextContent{Text: id},
			}}},
			Position: 0, CreatedAt: timestamppb.Now(),
		}
		if err := db.InsertMessage(msg, nil); err != nil {
			t.Fatalf("InsertMessage(%s): %v", id, err)
		}
	}

	// Build three 1024-dim vectors:
	//   query = e0       (basis dim 0 = 1, rest 0)
	//   near  = e0       (identical → cosine_distance = 0)
	//   far   = e1       (orthogonal → cosine_distance = 1)
	query := unitVec1024(0)
	near := unitVec1024(0)
	far := unitVec1024(1)

	if err := db.InsertChunkEmbedding("near", 0, "test-model", near); err != nil {
		t.Fatalf("InsertChunkEmbedding near: %v", err)
	}
	if err := db.InsertChunkEmbedding("far", 0, "test-model", far); err != nil {
		t.Fatalf("InsertChunkEmbedding far: %v", err)
	}

	// Direct vec0 KNN query — bypasses NearestChunkVectors so the
	// test isolates the distance metric (not the production helper's
	// aux-column WHERE handling). The schema's distance_metric=cosine
	// declaration is what's under test; any working KNN query
	// against this vec0 instance returns cosine distance.
	queryRows, err := db.Query(
		`SELECT message_id, distance FROM chunk_vectors
		 WHERE embedding MATCH ? AND k = 2
		 ORDER BY distance`,
		encodeVector(query),
	)
	if err != nil {
		t.Fatalf("vec0 KNN query: %v", err)
	}
	defer queryRows.Close()

	got := map[string]float64{}
	for queryRows.Next() {
		var msgID string
		var dist float64
		if err := queryRows.Scan(&msgID, &dist); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got[msgID] = dist
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}

	// Cosine distance: near=e0 vs query=e0 → 1 - 1 = 0; far=e1 vs
	// query=e0 → 1 - 0 = 1. If sqlite-vec is computing L2² instead,
	// near would still be 0 but far would be 2 (||e0 - e1||² = 2),
	// which `1 - distance = -1` clamps to 0 in the consumer — a
	// silent recall collapse the engine has no way to detect.
	if math.Abs(got["near"]-0.0) > 1e-5 {
		t.Errorf("near cosine_distance: want 0.0, got %v (vec0 likely on L2 — distance_metric=cosine not declared?)", got["near"])
	}
	if math.Abs(got["far"]-1.0) > 1e-5 {
		t.Errorf("far cosine_distance: want 1.0, got %v (vec0 likely on L2 — distance_metric=cosine not declared?)", got["far"])
	}
}

// unitVec1024 returns a 1024-dim unit vector with a single 1.0 at
// the given basis index, the rest zero. Used by the cosine probe to
// keep the analytical computation trivial.
func unitVec1024(basis int) []float32 {
	v := make([]float32, 1024)
	v[basis] = 1.0
	return v
}
