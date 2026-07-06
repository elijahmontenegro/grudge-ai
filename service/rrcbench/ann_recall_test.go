package rrcbench

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/elijahmontenegro/grudge/service/annindex"
)

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// noisyUnit returns normalize(center + alpha*noise) — a point near center
// on the unit sphere. alpha controls how far: alpha≈0.75 lands members at
// cosine≈0.8 to their center, the kind of margin real semantic neighbours
// have (unlike uniform-random vectors, which sit at cosine≈0 to everything
// and have no neighbour structure to retrieve).
func noisyUnit(center []float32, seed string, alpha float64) []float32 {
	noise := unitVector(seed, len(center))
	out := make([]float32, len(center))
	var norm float64
	for i := range out {
		out[i] = center[i] + float32(alpha)*noise[i]
		norm += float64(out[i]) * float64(out[i])
	}
	inv := float32(1.0 / math.Sqrt(norm))
	for i := range out {
		out[i] *= inv
	}
	return out
}

// TestANNRecallVsBruteForce measures the recall the oracle will see: the
// binary HNSW shortlist, reranked by exact cosine, against the true cosine
// top-k from a brute-force scan — on CLUSTERED vectors that actually have
// neighbour structure (the shape real embeddings have). Uniform-random
// vectors have no retrievable neighbours and only measure quantization
// noise; this measures whether the index recovers genuine neighbours.
func TestANNRecallVsBruteForce(t *testing.T) {
	const (
		dim       = benchDim
		clusters  = 40
		perClust  = 60 // n = clusters*perClust = 2400
		queries   = 120
		k         = 10
		overfetch = 8
		alpha     = 0.75
	)
	centers := make([][]float32, clusters)
	for c := range centers {
		centers[c] = unitVector(fmt.Sprintf("center-%d", c), dim)
	}

	keys := make([]string, 0, clusters*perClust)
	vecs := make(map[string][]float32, clusters*perClust)
	ix := annindex.New(annindex.Config{Seed: 1})
	for c := range clusters {
		for j := range perClust {
			key := fmt.Sprintf("c%d-m%d", c, j)
			v := noisyUnit(centers[c], key, alpha)
			keys = append(keys, key)
			vecs[key] = v
			ix.Add(key, v)
		}
	}

	type ks struct {
		key string
		s   float64
	}
	var hit, total int
	for q := range queries {
		qv := noisyUnit(centers[q%clusters], fmt.Sprintf("q%d", q), alpha)

		truthArr := make([]ks, len(keys))
		for i, key := range keys {
			truthArr[i] = ks{key, dot(qv, vecs[key])}
		}
		sort.Slice(truthArr, func(a, b int) bool { return truthArr[a].s > truthArr[b].s })
		truth := make(map[string]bool, k)
		for i := range k {
			truth[truthArr[i].key] = true
		}

		// Search reranks the Hamming shortlist by asymmetric score; take its
		// top-k directly.
		short := ix.Search(qv, k*overfetch)
		lim := min(k, len(short))
		for i := range lim {
			if truth[short[i].Key] {
				hit++
			}
		}
		total += k
	}

	recall := float64(hit) / float64(total)
	t.Logf("ANN end-to-end recall@%d (binary shortlist x%d + cosine rerank) = %.3f over %d clustered vectors",
		k, overfetch, recall, len(keys))
	if recall < 0.90 {
		t.Errorf("recall %.3f below 0.90 on clustered vectors — binary shortlist is losing true neighbours; escalate quantization per plan", recall)
	}
}
