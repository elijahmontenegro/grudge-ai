package rrcbench

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/elijahmontenegro/grudge/service/annindex"
)

// TestANNLatencyFlat is the payoff measurement: index Search latency across
// a doubling corpus. Brute-force vec0 was linear (knn_ms ≈ 108 + 0.0106·N,
// ratio ≈ 2.0 per doubling — the leak rrcbench found). A graph index should
// be ~O(log N): the vs_prev ratio should sit near ~1.0-1.1, essentially
// flat, as N doubles. That flatness IS the corpus-invariance fix.
// Informational (wall-clock); gated behind RRC_BENCH.
func TestANNLatencyFlat(t *testing.T) {
	if os.Getenv("RRC_BENCH") == "" {
		t.Skip("perf curve; set RRC_BENCH=1 to run")
	}
	sizes := []int{1000, 2000, 4000, 8000, 16000}
	// Clustered vectors — the navigable small-world structure real embeddings
	// have. Uniform-random vectors are a pathological case for HNSW (nothing
	// to navigate), pessimistic for latency as well as recall.
	centers := make([][]float32, 50)
	for c := range centers {
		centers[c] = unitVector(fmt.Sprintf("lc-%d", c), benchDim)
	}
	qv := noisyUnit(centers[0], "lq", 0.75)

	t.Logf("%-8s %14s %9s   (brute-force ratio was ~2.0/doubling; flat ~1.0 => sub-linear)",
		"N", "search_us", "vs_prev")
	var prev float64
	for _, n := range sizes {
		ix := annindex.New(annindex.Config{Seed: 1})
		for i := range n {
			key := fmt.Sprintf("v%d", i)
			ix.Add(key, noisyUnit(centers[i%len(centers)], key, 0.75))
		}

		const reps = 300
		start := time.Now()
		for range reps {
			_ = ix.Search(qv, 64)
		}
		us := float64(time.Since(start).Microseconds()) / float64(reps)

		ratio := 0.0
		if prev > 0 {
			ratio = us / prev
		}
		t.Logf("%-8d %14.1f %9.2f", n, us, ratio)
		prev = us
	}
}
