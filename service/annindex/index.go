package annindex

import (
	"sort"
	"sync"
)

// Candidate is one search hit: the caller's key and its asymmetric score
// against the query (higher is nearer). The score already reranks the
// Hamming shortlist, so the caller uses this order directly — no float
// re-fetch, no second rerank.
type Candidate struct {
	Key   string
	Score float64
}

// Index is a concurrency-safe ANN index over binary-quantized vectors. It
// binarizes float32 inputs at the boundary; the graph stores only packed
// codes. Search is ~O(log N) and returns an over-fetched shortlist for the
// caller to rerank. Reads (Search) and writes (Add) are guarded by an
// RWMutex — Search is fast and reads dominate, so contention is low.
type Index struct {
	mu    sync.RWMutex
	graph *hnsw
	keys  []string         // node id -> key (append-locked to graph.nodes)
	i8    [][]int8         // node id -> int8 rerank code
	scale []float64        // node id -> that code's dequant scale
	byKey map[string]int32 // key -> node id (presence / dedup)
	ef    int              // default base-layer search beam width
}

// Config tunes the index. Zero fields take documented defaults.
type Config struct {
	M        int   // graph degree (neighbours per node per layer); default 16
	EfConstr int   // construction beam width; default 200
	EfSearch int   // default query beam width; default 128
	Seed     int64 // deterministic layer assignment
}

// New constructs an empty index.
func New(cfg Config) *Index {
	m := cfg.M
	if m == 0 {
		m = 16
	}
	efC := cfg.EfConstr
	if efC == 0 {
		efC = 200
	}
	efS := cfg.EfSearch
	if efS == 0 {
		efS = 128
	}
	return &Index{
		graph: newHNSW(m, efC, cfg.Seed),
		byKey: make(map[string]int32),
		ef:    efS,
	}
}

// Add indexes vec under key. A key already present is ignored (idempotent
// re-adds keep keys and graph nodes in lockstep).
func (ix *Index) Add(key string, vec []float32) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, ok := ix.byKey[key]; ok {
		return
	}
	id := ix.graph.insert(Binarize(vec)) // binary code: fast Hamming traversal
	code, sc := QuantizeInt8(vec)        // int8 code: asymmetric rerank
	ix.byKey[key] = id
	ix.keys = append(ix.keys, key) // insert returns id == pre-append len
	ix.i8 = append(ix.i8, code)
	ix.scale = append(ix.scale, sc)
}

// Search returns up to n candidates for vec, sorted by asymmetric score
// (nearest first). The graph navigates by Hamming to gather an n-sized
// shortlist, which is then reranked by asymmetric distance — the float
// query against each candidate's binary code — entirely in RAM. n is the
// caller's over-fetch count; it takes the top-k it wants after applying its
// own predicate to this ordering.
func (ix *Index) Search(vec []float32, n int) []Candidate {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	res := ix.graph.search(Binarize(vec), n, ix.ef)
	out := make([]Candidate, len(res))
	for i, r := range res {
		out[i] = Candidate{
			Key:   ix.keys[r.id],
			Score: AsymmetricInt8Score(vec, ix.i8[r.id], ix.scale[r.id]),
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out
}

// Vector dequantizes the stored int8 code for key and returns it as a float
// vector — the in-RAM source for callers that need the vector back (e.g. MMR
// diversity), so they never re-read it from disk. Approximate (int8 rounding),
// which is fine for a diversity penalty. Returns false if key is not indexed.
func (ix *Index) Vector(key string) ([]float32, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	id, ok := ix.byKey[key]
	if !ok {
		return nil, false
	}
	code, s := ix.i8[id], ix.scale[id]
	v := make([]float32, len(code))
	for i, c := range code {
		v[i] = float32(float64(c) * s)
	}
	return v, true
}

// Has reports whether key is already indexed — lets the boot build and the
// live backfill skip re-adding vectors.
func (ix *Index) Has(key string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	_, ok := ix.byKey[key]
	return ok
}

// Len is the number of indexed vectors.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.keys)
}
