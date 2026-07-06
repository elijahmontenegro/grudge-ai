package annindex

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// seedVec deterministically maps a seed string to a dim-dimensional vector with
// mixed signs (Gaussian components), so Binarize produces distinguishing codes.
func seedVec(dim int, seed string) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	r := rand.New(rand.NewSource(int64(h.Sum64())))
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return v
}

// TestPartitionInvariance is the regression guard the single-thread benchmark
// structurally could NOT provide: it proves THREAD-scoped search work is
// invariant under corpus growth in OTHER threads. A target thread of fixed
// size is searched while filler vectors accumulate in a different thread; the
// distance-evaluation count (Search's second return) must be BYTE-IDENTICAL
// across filler sizes, because the target partition's graph — its nodes and
// its splitmix layer stream (seeded by thread id) — is untouched by filler
// that lands in other partitions. If a future change reverts to a global graph
// + post-filter, this count balloons with filler and the test fails.
func TestPartitionInvariance(t *testing.T) {
	const (
		dim          = 128
		targetThread = "target"
		targetSize   = 20 // smaller than a realistic k; the old widen loop scanned all N here
	)
	query := seedVec(dim, "partition-invariance-query")
	fillerSizes := []int{0, 1000, 5000}

	var want int
	for fi, filler := range fillerSizes {
		ix := New(Config{Seed: 1})
		// Target thread added first so its partition graph is identical every run.
		for i := range targetSize {
			key := fmt.Sprintf("t-%d", i)
			ix.Add(key, targetThread, seedVec(dim, key))
		}
		for i := range filler {
			key := fmt.Sprintf("f-%d", i)
			ix.Add(key, "filler", seedVec(dim, key))
		}
		got, visited := ix.Search(query, 64*8, targetThread)
		if len(got) != targetSize {
			t.Fatalf("filler=%d: target search returned %d, want all %d target vectors", filler, len(got), targetSize)
		}
		if fi == 0 {
			want = visited
			continue
		}
		if visited != want {
			t.Fatalf("filler=%d: target-thread search did %d distance evals, want %d — THREAD scope is NOT partition-bounded (O(N) leak)",
				filler, visited, want)
		}
	}
	if want == 0 {
		t.Fatal("target-thread search did zero work — misconfigured")
	}
	t.Logf("THREAD-scoped search: %d distance evals, invariant across filler %v", want, fillerSizes)
}

func TestBinarizeHamming(t *testing.T) {
	// bit i set iff vec[i] >= 0: [1,-1,1,-1] -> bits 0 and 2 -> 0b0101.
	a := Binarize([]float32{1, -1, 1, -1})
	if a[0] != 0b0101 {
		t.Fatalf("Binarize = %04b, want 0101", a[0]&0xf)
	}
	b := Binarize([]float32{1, 1, 1, -1}) // flips bit 1 on -> 0b0111
	if got := Hamming(a, b); got != 1 {
		t.Fatalf("Hamming(differ by one sign) = %d, want 1", got)
	}
	if got := Hamming(a, a); got != 0 {
		t.Fatalf("Hamming(self) = %d, want 0", got)
	}
}

// TestIndexRecall validates the whole index — Hamming-navigated graph plus
// int8 asymmetric rerank — against the real goal: true cosine nearest
// neighbours, on CLUSTERED vectors. Real embeddings have neighbour structure
// where Hamming and int8-asymmetric both track cosine, so the shortlist
// contains the reranked winners; uniform-random vectors have no such
// structure and only measure the two metrics diverging on noise.
func TestIndexRecall(t *testing.T) {
	const (
		dim      = 128
		clusters = 30
		perClust = 50
		queries  = 60
		k        = 10
	)
	rng := rand.New(rand.NewSource(7))
	onSphere := func(build func(i int) float32) []float32 {
		v := make([]float32, dim)
		var n float64
		for i := range v {
			v[i] = build(i)
			n += float64(v[i]) * float64(v[i])
		}
		inv := float32(1 / math.Sqrt(n))
		for i := range v {
			v[i] *= inv
		}
		return v
	}
	unit := func() []float32 { return onSphere(func(int) float32 { return float32(rng.NormFloat64()) }) }
	// A cluster member is center + 0.75*(unit noise): the noise is a UNIT
	// vector (magnitude ~0.75), not per-dim gaussian (magnitude ~0.75*sqrt(dim),
	// which would swamp the center and wash the clusters out). Lands members at
	// cosine ~0.8 to their center — the neighbour structure real embeddings have.
	near := func(c []float32) []float32 {
		noise := unit()
		return onSphere(func(i int) float32 { return c[i] + 0.75*noise[i] })
	}
	dot := func(a, b []float32) float64 {
		var s float64
		for i := range a {
			s += float64(a[i]) * float64(b[i])
		}
		return s
	}

	centers := make([][]float32, clusters)
	for i := range centers {
		centers[i] = unit()
	}
	keys := make([]string, 0, clusters*perClust)
	vecs := make(map[string][]float32)
	ix := New(Config{Seed: 1})
	for c := range clusters {
		for j := range perClust {
			key := fmt.Sprintf("c%d-%d", c, j)
			v := near(centers[c])
			keys = append(keys, key)
			vecs[key] = v
			ix.Add(key, "", v)
		}
	}

	var hit, total int
	for q := range queries {
		qv := near(centers[q%clusters])
		type ks struct {
			key string
			s   float64
		}
		arr := make([]ks, len(keys))
		for i, key := range keys {
			arr[i] = ks{key, dot(qv, vecs[key])}
		}
		sort.Slice(arr, func(a, b int) bool { return arr[a].s > arr[b].s })
		truth := make(map[string]bool, k)
		for i := range k {
			truth[arr[i].key] = true
		}

		got, _ := ix.Search(qv, k*8, "") // over-fetch, then take the reranked top-k
		for i := 0; i < k && i < len(got); i++ {
			if truth[got[i].Key] {
				hit++
			}
		}
		total += k
	}

	recall := float64(hit) / float64(total)
	if recall < 0.90 {
		t.Fatalf("index recall@%d = %.3f on clustered vectors, want >= 0.90", k, recall)
	}
	t.Logf("index recall@%d (graph + int8 asymmetric) = %.3f", k, recall)
}

// TestAddIdempotent confirms re-adding a key is a no-op and keeps the
// key<->node lockstep intact (Search still resolves keys correctly).
func TestAddIdempotent(t *testing.T) {
	// Distinguishing sign patterns: sign-bit binarization collapses vectors
	// that share signs, so the keys must differ in sign (not just magnitude).
	ix := New(Config{Seed: 1})
	ix.Add("a", "", []float32{1, -1, 1, -1}) // code 0101
	ix.Add("b", "", []float32{-1, 1, -1, 1}) // code 1010
	ix.Add("a", "", []float32{1, -1, 1, -1}) // duplicate
	if ix.Len() != 2 {
		t.Fatalf("Len after duplicate Add = %d, want 2", ix.Len())
	}
	if !ix.Has("a") || !ix.Has("b") {
		t.Fatal("Has lost a key")
	}
	got, _ := ix.Search([]float32{1, -1, 1, -1}, 1, "")
	if len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("Search = %+v, want key a", got)
	}
}

// TestPartitionRouting confirms the thread partitions: a vector added under
// thread T is found by a T-scoped search and by the global ("") search, but
// NOT by a search scoped to a different thread. This is the core of the
// thread-invariance fix — THREAD scope touches only its own partition.
func TestPartitionRouting(t *testing.T) {
	ix := New(Config{Seed: 1})
	ix.Add("a", "t1", []float32{1, -1, 1, -1})
	ix.Add("b", "t2", []float32{-1, 1, -1, 1})

	q := []float32{1, -1, 1, -1}
	// Thread t1 sees only a.
	if got, _ := ix.Search(q, 5, "t1"); len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("Search(t1) = %+v, want [a]", got)
	}
	// Thread t2 sees only b.
	if got, _ := ix.Search(q, 5, "t2"); len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("Search(t2) = %+v, want [b]", got)
	}
	// Global sees both.
	if got, _ := ix.Search(q, 5, ""); len(got) != 2 {
		t.Fatalf("Search(global) returned %d, want 2", len(got))
	}
	// An unknown thread has no partition -> empty, no panic.
	if got, _ := ix.Search(q, 5, "nope"); len(got) != 0 {
		t.Fatalf("Search(unknown thread) = %+v, want empty", got)
	}
}
