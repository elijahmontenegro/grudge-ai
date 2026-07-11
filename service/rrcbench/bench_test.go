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

	// Corpus-independent ceiling, per the actual mechanism. Local Context is
	// chunked PER MESSAGE (each semantic message is its own query unit), so
	// localWindow messages carry localWindow chunks. Each chunk scores at most
	// RerankTopK top-K-cosine candidates, and the provenance mass walk
	// surfaces at most provenanceReachCap messages per turn, each scored
	// against every chunk (same max-merge footing as cosine). Both components
	// are bounded by constants — the load-bearing invariance.
	//
	// The reach component GROWS BELOW its ceiling as N grows: walk-reached
	// ids already surfaced by cosine are skipped, and that overlap shrinks
	// with corpus size, so the scored remainder climbs toward the cap and
	// saturates. That is overlap decay under a hard ceiling, not an O(N)
	// leak — so flatness is asserted component-wise where it is exact
	// (cosine), and by ceiling where saturation applies (reach).
	const rerankTopK = 64
	const provenanceReachCap = 64
	// The cosine path scores candidates (≤ chunks·topK) plus the
	// detection law's noise reference (≤ chunks·R, R=16) — both
	// per-event constants; the reference is what keeps acceptance
	// corpus-invariant WITHOUT a fitted calibrator.
	cosineBound := int64(localWindow * (rerankTopK + rrc.ReferenceSampleSize))
	reachBound := int64(localWindow * provenanceReachCap)

	type split struct{ total, cosine, reach int64 }
	counts := make([]split, len(sizes))
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
		res, err := h.assembleOnce(t.Context())
		if err != nil {
			db.Close()
			t.Fatalf("N=%d: assemble: %v", n, err)
		}
		total := loadInt64(h.scorerPairs)
		reach := int64(res.Telemetry.PrerequisiteSelection.ProvenanceReached)
		counts[idx] = split{total: total, cosine: total - reach, reach: reach}
		db.Close()

		if counts[idx].cosine > cosineBound {
			t.Errorf("N=%d: cosine path scored %d pairs, exceeds chunks*(RerankTopK+R)=%d — an O(N) scoring leak",
				n, counts[idx].cosine, cosineBound)
		}
		if counts[idx].reach > reachBound {
			t.Errorf("N=%d: provenance-reach path scored %d pairs, exceeds chunks*reachCap=%d — the walk cap is not holding",
				n, counts[idx].reach, reachBound)
		}
	}

	// The cosine component must be flat-to-within-the-reference once
	// N >> RerankTopK: candidate work saturates at chunks·topK exactly,
	// and the only lawful variation is the noise reference (≤ chunks·R)
	// — a reference chunk that coincides with a top-K candidate reuses
	// its cache entry, and that overlap shrinks as the corpus grows. A
	// spread beyond chunks·R means candidate work is tracking N.
	refSlack := int64(localWindow * rrc.ReferenceSampleSize)
	lo, hi := counts[0].cosine, counts[0].cosine
	for _, c := range counts[1:] {
		if c.cosine < lo {
			lo = c.cosine
		}
		if c.cosine > hi {
			hi = c.cosine
		}
	}
	if hi-lo > refSlack {
		t.Errorf("cosine pairs vary beyond the reference slack (%d): %v — top-K work is not corpus-invariant",
			refSlack, counts)
	}
	t.Logf("scorer pairs across N=%v: %+v (ceilings: cosine %d, reach %d)",
		sizes, counts, cosineBound, reachBound)
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

	t.Logf("%-7s %10s %9s %12s %8s %7s",
		"N", "lctx_ms", "knn_ms", "assemble_ms", "scored", "edges")
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

		// The bridge's per-tick Local Context fetch, post corpus-passing removal:
		// a bounded recency window + the in-flight turn, both indexed. This
		// replaces the O(N) ThreadCorpus load + full-corpus protocol index,
		// which are no longer on the per-step path at all.
		lctxMs := timeIt(5, func() {
			_, _ = h.db.RecentMessages("bench", localWindow)
			_, _ = h.db.TurnMessages("bench", h.anchor.TurnId)
		})
		// One ANN KNN query (index-only, no vec0 scan).
		knnMs := timeIt(5, func() {
			_, _ = h.oracle.NearestChunks(ctx, benchText(n/2), 64, rrc.PredThread{ThreadID: "bench"})
		})
		// End-to-end Assemble via the CorpusStore (bounded selected-content and
		// protocol-counterpart fetches, not a corpus scan).
		h.resetCounters()
		var res rrc.AssembleResult
		asmMs := timeIt(1, func() { res, _ = h.assembleOnce(ctx) })

		t.Logf("%-7d %10.3f %9.3f %12.3f %8d %7d",
			n, lctxMs, knnMs, asmMs, loadInt64(h.scorerPairs), len(res.Edges))
		db.Close()
	}
}
