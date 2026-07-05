package rrcbench

import (
	"os"
	"testing"
	"time"

	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// guardDim is the embedding width the CI count-invariance guard uses. The
// scored-pair count is independent of vector width, so shrinking the vec0
// table keeps the brute-force KNN cheap enough to run on every pass while
// asserting the exact same invariant. The wall-clock curves keep the real
// benchDim (1024) so their timings are faithful.
const guardDim = 64

// timeIt runs fn reps times and returns the mean wall-clock in
// milliseconds. Averaging smooths the per-call noise that makes a single
// timing meaningless; it does not remove it, which is why the curve is
// informational and never asserted.
func timeIt(reps int, fn func()) float64 {
	start := time.Now()
	for range reps {
		fn()
	}
	return float64(time.Since(start).Microseconds()) / float64(reps) / 1000.0
}

// TestInvariance_ScorerWorkStaysFlat is the regression guard for the
// load-bearing claim. The scorer is the expensive per-step operation; the
// number of candidate pairs it scores in one Assemble must be bounded by a
// corpus-INDEPENDENT constant — RerankTopK per Local Context chunk, plus the
// provenanceReachCap — no matter how large the corpus grows. If a future
// change routes O(N) candidates to the scorer, this test fails.
//
// This asserts the invariant that actually matters (bounded model work) and
// is deterministic, so it is safe in CI. The wall-clock leaks live in
// TestInvariance_StageLatencyCurve, which is informational only.
func TestInvariance_ScorerWorkStaysFlat(t *testing.T) {
	const localWindow = 6
	sizes := []int{250, 500, 1000, 2000}

	// Corpus-independent ceiling: at most RerankTopK candidates surface per
	// Local Context chunk (top-K cosine), plus at most provenanceReachCap
	// messages via the backward mass walk. localWindow messages carry one
	// chunk each, so <= localWindow local chunks. RerankTopK and the cap are
	// DefaultConfig(64) and rrc's provenanceReachCap(64) respectively.
	const rerankTopK = 64
	const provenanceReachCap = 64
	bound := int64(localWindow*rerankTopK + provenanceReachCap)

	counts := make([]int64, len(sizes))
	for idx, n := range sizes {
		db, err := storage.Open(t.TempDir())
		if err != nil {
			t.Fatalf("N=%d: open db: %v", n, err)
		}
		if err := db.EnsureEmbeddingDim(guardDim, benchModel); err != nil {
			db.Close()
			t.Fatalf("N=%d: shrink embedding dim: %v", n, err)
		}
		h, err := buildCorpus(db, "bench", n, localWindow, guardDim)
		if err != nil {
			db.Close()
			t.Fatalf("N=%d: build corpus: %v", n, err)
		}
		h.resetCounters()
		if _, err := h.assembleOnce(t.Context()); err != nil {
			db.Close()
			t.Fatalf("N=%d: assemble: %v", n, err)
		}
		counts[idx] = loadInt64(h.scorerPairs)
		db.Close()

		if counts[idx] > bound {
			t.Errorf("N=%d: scorer saw %d candidate pairs, exceeds corpus-independent bound %d — an O(N) scoring leak",
				n, counts[idx], bound)
		}
	}

	// Flatness: once N greatly exceeds RerankTopK, the scored-pair count
	// should be materially constant (top-K saturates, the reach walk caps).
	// A growth trend from the smallest to the largest corpus would mean the
	// scorer work tracks corpus size even while staying under the ceiling.
	lo, hi := counts[0], counts[len(counts)-1]
	if lo > 0 && hi > lo*3/2 {
		t.Errorf("scorer work grew with corpus: N=%d scored %d pairs, N=%d scored %d (>1.5x) — not invariant",
			sizes[0], lo, sizes[len(sizes)-1], hi)
	}
	t.Logf("scorer candidate-pairs across N=%v: %v (corpus-independent bound %d)", sizes, counts, bound)
}

// TestInvariance_KNNScaling settles the vec0 complexity question in
// isolation: it times the raw NearestChunkVectors scan (a fixed query
// vector, no embedding, no oracle wrapper, no predicate) across a doubling
// corpus. The vs_prev column is the tell — each step doubles N, so a linear
// brute-force scan trends toward ~2.0 per step while a genuine sub-linear
// ANN index would stay near ~1.0-1.2. Informational (wall-clock); skipped
// under -short.
func TestInvariance_KNNScaling(t *testing.T) {
	if os.Getenv("RRC_BENCH") == "" {
		t.Skip("perf curve; set RRC_BENCH=1 to run (keeps the WASM-KNN cost out of the default test path)")
	}
	sizes := []int{1000, 2000, 4000, 8000, 16000, 32000}
	qVec := unitVector("fixed-knn-query", benchDim)

	t.Logf("%-8s %10s %9s   (each step doubles N: ~2.0 => linear scan, ~1.0 => sub-linear)",
		"N", "knn_ms", "vs_prev")
	var prev float64
	for _, n := range sizes {
		db, err := storage.Open(t.TempDir())
		if err != nil {
			t.Fatalf("N=%d: open db: %v", n, err)
		}
		if err := insertVectorCorpus(db, "bench", n, benchDim); err != nil {
			db.Close()
			t.Fatalf("N=%d: insert vectors: %v", n, err)
		}
		ms := timeIt(15, func() { _, _ = db.NearestChunkVectors(qVec, 64, benchModel, "", nil) })
		ratio := 0.0
		if prev > 0 {
			ratio = ms / prev
		}
		t.Logf("%-8d %10.3f %9.2f", n, ms, ratio)
		prev = ms
		db.Close()
	}
}

// TestInvariance_StageLatencyCurve quantifies the four suspected O(N)
// substrate leaks by timing each stage across a growing corpus, and settles
// whether vec0 KNN is sub-linear (as the storage comments claim) or a linear
// brute-force scan. It asserts nothing — wall-clock is machine-dependent and
// noisy — so it is skipped under -short and read by eye / captured in the
// findings write-up.
func TestInvariance_StageLatencyCurve(t *testing.T) {
	if os.Getenv("RRC_BENCH") == "" {
		t.Skip("perf curve; set RRC_BENCH=1 to run (keeps the WASM-KNN cost out of the default test path)")
	}
	const localWindow = 6
	sizes := []int{500, 1000, 2000, 4000, 8000}
	ctx := t.Context()

	t.Logf("%-7s %11s %13s %9s %12s %8s %7s",
		"N", "load_ms", "protoIdx_ms", "knn_ms", "assemble_ms", "scored", "edges")
	for _, n := range sizes {
		db, err := storage.Open(t.TempDir())
		if err != nil {
			t.Fatalf("N=%d: open db: %v", n, err)
		}
		h, err := buildCorpus(db, "bench", n, localWindow, benchDim)
		if err != nil {
			db.Close()
			t.Fatalf("N=%d: build corpus: %v", n, err)
		}

		// Leak 1: full-history load + per-message proto deserialize.
		loadMs := timeIt(5, func() { _, _ = h.db.ThreadCorpus("bench") })
		// Leak 2: protocol index construction over the whole corpus.
		protoMs := timeIt(5, func() { _ = rrc.NewProtocolIndex(h.corpus) })
		// Leak 4: one vec0 KNN query. If this grows ~linearly with N, the
		// "sub-linear ANN" comment is false and the search is brute-force.
		knnMs := timeIt(5, func() {
			_, _ = h.oracle.NearestChunks(ctx, benchText(n/2), 64, rrc.PredThread{ThreadID: "bench"})
		})
		// End-to-end (includes leak 3, the eligibility scan, plus detection,
		// Select, MMR, shed). Cold cache each rep: a fresh anchor is not
		// re-run, so this is a single cold Assemble timed once.
		h.resetCounters()
		var res rrc.AssembleResult
		asmMs := timeIt(1, func() { res, _ = h.assembleOnce(ctx) })

		t.Logf("%-7d %11.3f %13.3f %9.3f %12.3f %8d %7d",
			n, loadMs, protoMs, knnMs, asmMs, loadInt64(h.scorerPairs), len(res.Edges))
		db.Close()
	}
}
