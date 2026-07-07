package annindex

import (
	"fmt"
	"hash/fnv"
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

// Index is a concurrency-safe ANN index over binary-quantized vectors,
// partitioned by thread so both retrieval scopes stay sub-linear:
//   - a global graph over every vector serves ALL_THREADS scope (O(log N)).
//   - one graph per thread serves THREAD scope (O(log |thread|)), invariant
//     under growth of other threads.
//
// Each vector lives in the global graph and its thread's partition, so graph
// structure roughly doubles; the dominant per-vector cost, the int8 rerank
// code, is stored once in a shared store keyed by vid (the index into
// keys/i8/scale), with a graph's local node id mapped back via hnsw.vids. One
// RWMutex guards reads (Search) and writes (Add).
type Index struct {
	mu     sync.RWMutex
	global *hnsw            // all vids — ALL scope
	parts  map[string]*hnsw // threadID -> that thread's vids — THREAD scope
	keys   []string         // vid -> key (shared store)
	i8     [][]int8         // vid -> int8 rerank code (shared; the 1KB term)
	scale  []float64        // vid -> that code's dequant scale
	byKey  map[string]int32 // key -> vid (presence / dedup / Vector lookup)

	m        int   // graph degree
	efConstr int   // construction beam width
	ef       int   // default query beam width
	seed     int64 // base seed; partitions derive seed ^ fnv(threadID)
	dim      int   // vector width, fixed by the first Add; 0 until then
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
		global:   newHNSW(m, efC, cfg.Seed),
		parts:    make(map[string]*hnsw),
		byKey:    make(map[string]int32),
		m:        m,
		efConstr: efC,
		ef:       efS,
		seed:     cfg.Seed,
	}
}

// Add indexes vec under key: into the global graph, and — unless threadID is
// "" — into that thread's partition ("" is the global-only sentinel; thread
// ids are non-empty in this system). A key already present is ignored
// (idempotent; keys, codes, and graphs stay in lockstep via vid).
func (ix *Index) Add(key, threadID string, vec []float32) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, ok := ix.byKey[key]; ok {
		return
	}
	// Fail fast on a dimension mismatch. The distance kernels (Hamming,
	// AsymmetricInt8Score) index one code by the other's length, so a mismatch
	// is a panic in one direction and silent wrong distances in the other —
	// never tolerate it (vec0 enforced this at the DB layer; the in-RAM index
	// must enforce it itself).
	if ix.dim == 0 {
		ix.dim = len(vec)
	} else if len(vec) != ix.dim {
		panic(fmt.Sprintf("annindex.Add: vector dim %d != index dim %d", len(vec), ix.dim))
	}
	vid := int32(len(ix.keys))
	code := Binarize(vec)        // binary code: fast Hamming traversal (shared, immutable)
	i8c, sc := QuantizeInt8(vec) // int8 code: asymmetric rerank (stored once)
	ix.keys = append(ix.keys, key)
	ix.i8 = append(ix.i8, i8c)
	ix.scale = append(ix.scale, sc)
	ix.byKey[key] = vid

	ix.global.insert(vid, code)
	if threadID != "" {
		p := ix.parts[threadID]
		if p == nil {
			p = newHNSW(ix.m, ix.efConstr, ix.seed^fnvHash(threadID))
			ix.parts[threadID] = p
		}
		p.insert(vid, code) // same immutable code slice — no duplication
	}
}

// Search returns up to n candidates for vec, sorted by asymmetric score
// (nearest first), plus the number of distance evaluations the graph made
// (a deterministic work signal for the invariance guard). threadID selects
// the graph: "" searches the global graph (ALL scope); a non-empty thread
// searches only that partition (THREAD scope) — an absent partition yields
// nothing. n is the caller's over-fetch count; it takes the top-k it wants
// after applying its own predicate to this ordering.
func (ix *Index) Search(vec []float32, n int, threadID string) ([]Candidate, int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.dim != 0 && len(vec) != ix.dim {
		panic(fmt.Sprintf("annindex.Search: query dim %d != index dim %d", len(vec), ix.dim))
	}
	g := ix.global
	if threadID != "" {
		g = ix.parts[threadID]
		if g == nil {
			return nil, 0
		}
	}
	res, visited := g.search(Binarize(vec), n, ix.ef)
	out := make([]Candidate, len(res))
	for i, r := range res {
		vid := g.vids[r.id]
		out[i] = Candidate{
			Key:   ix.keys[vid],
			Score: AsymmetricInt8Score(vec, ix.i8[vid], ix.scale[vid]),
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out, visited
}

// Vector dequantizes the stored int8 code for key and returns it as a float
// vector — the in-RAM source for callers that need the vector back (e.g. MMR
// diversity), so they never re-read it from disk. Approximate (int8 rounding),
// which is fine for a diversity penalty. Returns false if key is not indexed.
func (ix *Index) Vector(key string) ([]float32, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	vid, ok := ix.byKey[key]
	if !ok {
		return nil, false
	}
	code, s := ix.i8[vid], ix.scale[vid]
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

// Len is the number of indexed vectors (global count).
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.keys)
}

// fnvHash derives a partition's layer-stream seed from its thread id, so
// partitions get independent deterministic graphs.
func fnvHash(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64())
}
