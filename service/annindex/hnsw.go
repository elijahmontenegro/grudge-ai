package annindex

import (
	"container/heap"
	"math"
	"slices"
	"sort"
)

// hnsw is a minimal Hierarchical Navigable Small World graph over binary
// codes under Hamming distance. Search descends the sparse upper layers
// greedily, then beam-searches the base layer — the number of vectors
// touched grows like log(N), not N, which is the whole point.
//
// It is deliberately small and self-contained: off-the-shelf pure-Go HNSW
// libraries store []float32 and so cannot hold bit-packed codes, which is
// exactly the footprint win this index exists for. Correctness is checked
// empirically by the recall gate in service/rrcbench rather than trusted.
//
// Not safe for concurrent use; the exported Index wraps it in a mutex. The
// Index runs one such graph per thread partition plus a global graph, so a
// node's local id is graph-local; h.vids maps it back to the shared store.
type hnsw struct {
	nodes    []hnswNode
	vids     []int32 // local node id -> external vector id (Index's shared store)
	entry    int32   // node id of the entry point; -1 when empty
	topLayer int
	m        int     // target neighbours per node per layer
	mMax0    int     // max neighbours on the base layer (conventionally 2*m)
	efConstr int     // beam width during construction
	mL       float64 // level-generation normaliser, 1/ln(m)
	// rngState is an 8-byte splitmix64 stream for deterministic layer draws.
	// A rand.Rand would cost ~4.8KB of source state per graph — pathological
	// when THREAD scope spawns a partition graph per (often tiny) thread.
	rngState uint64
}

type hnswNode struct {
	code  []uint64
	conns [][]int32 // conns[layer] = neighbour node ids; len = node's top layer + 1
}

func newHNSW(m, efConstr int, seed int64) *hnsw {
	if m < 2 {
		m = 2
	}
	return &hnsw{
		entry:    -1,
		m:        m,
		mMax0:    2 * m,
		efConstr: efConstr,
		mL:       1 / math.Log(float64(m)),
		rngState: uint64(seed),
	}
}

// nextRandom returns a deterministic uniform in [0,1) from the graph's own
// splitmix64 state, so every partition graph carries an independent,
// reproducible layer stream in 8 bytes. The invariance guard depends on a
// partition's stream being unaffected by inserts into other partitions.
func (h *hnsw) nextRandom() float64 {
	h.rngState += 0x9E3779B97F4A7C15
	z := h.rngState
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	return float64(z>>11) / float64(uint64(1)<<53)
}

// randomLayer draws a node's top layer from the geometric distribution
// that gives HNSW its logarithmic height.
func (h *hnsw) randomLayer() int {
	return int(-math.Log(h.nextRandom()+1e-12) * h.mL)
}

// insert adds code (external id vid) to the graph and returns its local node
// id. Local node ids index h.nodes/h.vids and are stable (the graph is
// append-only); callers map them back to the shared store via h.vids.
func (h *hnsw) insert(vid int32, code []uint64) int32 {
	id := int32(len(h.nodes))
	layer := h.randomLayer()
	h.nodes = append(h.nodes, hnswNode{code: code, conns: make([][]int32, layer+1)})
	h.vids = append(h.vids, vid)

	if h.entry == -1 {
		h.entry = id
		h.topLayer = layer
		return id
	}

	// Construction distance-evals aren't part of the query work signal, so a
	// throwaway counter keeps greedyClosest/searchLayer single-signatured.
	var vis int

	// Greedy descent through the layers above the new node's top layer.
	ep := h.entry
	for l := h.topLayer; l > layer; l-- {
		ep = h.greedyClosest(code, ep, l, &vis)
	}

	// From the new node's top layer down to base: beam-search for candidates,
	// pick neighbours by the heuristic, link bidirectionally, prune.
	for l := min(layer, h.topLayer); l >= 0; l-- {
		found := h.searchLayer(code, ep, h.efConstr, l, &vis)
		mMax := h.m
		if l == 0 {
			mMax = h.mMax0
		}
		ids := make([]int32, len(found))
		for i, f := range found {
			ids[i] = f.id
		}
		neighbours := h.selectNeighbors(code, ids, mMax)
		h.nodes[id].conns[l] = neighbours
		for _, nb := range neighbours {
			h.nodes[nb].conns[l] = append(h.nodes[nb].conns[l], id)
			if len(h.nodes[nb].conns[l]) > mMax {
				h.nodes[nb].conns[l] = h.selectNeighbors(h.nodes[nb].code, h.nodes[nb].conns[l], mMax)
			}
		}
		if len(found) > 0 {
			ep = found[0].id // nearest so far seeds the next layer down
		}
	}

	if layer > h.topLayer {
		h.topLayer = layer
		h.entry = id
	}
	return id
}

// search returns up to k nearest node ids to code (ascending by distance) and
// the number of distance evaluations performed — a deterministic work signal
// the invariance guard asserts stays flat for a partition as OTHER partitions
// grow. ef is the base-layer beam width (>= k for useful recall).
func (h *hnsw) search(code []uint64, k, ef int) ([]distNode, int) {
	if h.entry == -1 {
		return nil, 0
	}
	visited := 0
	ep := h.entry
	for l := h.topLayer; l > 0; l-- {
		ep = h.greedyClosest(code, ep, l, &visited)
	}
	if ef < k {
		ef = k
	}
	res := h.searchLayer(code, ep, ef, 0, &visited)
	if len(res) > k {
		res = res[:k]
	}
	return res, visited
}

// greedyClosest walks toward code from ep along one layer, hopping to a
// strictly-closer neighbour until none exists (the ef=1 descent step). Each
// Hamming evaluation bumps *visited.
func (h *hnsw) greedyClosest(code []uint64, ep int32, layer int, visited *int) int32 {
	best := ep
	bestDist := Hamming(code, h.nodes[ep].code)
	*visited++
	for {
		improved := false
		for _, nb := range h.nodes[best].conns[layer] {
			d := Hamming(code, h.nodes[nb].code)
			*visited++
			if d < bestDist {
				best, bestDist, improved = nb, d, true
			}
		}
		if !improved {
			return best
		}
	}
}

// searchLayer is the HNSW beam search: expand the closest unexpanded
// candidate, keep the ef best seen, stop when the nearest candidate is
// farther than the current worst result. Returns the kept set ascending.
// Each Hamming evaluation bumps *visited.
func (h *hnsw) searchLayer(code []uint64, ep int32, ef, layer int, visited *int) []distNode {
	epDist := Hamming(code, h.nodes[ep].code)
	*visited++
	seen := map[int32]bool{ep: true}
	cand := &minHeap{{ep, epDist}} // frontier: nearest-first
	res := &maxHeap{{ep, epDist}}  // results: farthest-first, capped at ef
	heap.Init(cand)
	heap.Init(res)

	for cand.Len() > 0 {
		c := heap.Pop(cand).(distNode)
		if c.dist > (*res)[0].dist && res.Len() >= ef {
			break
		}
		for _, nb := range h.nodes[c.id].conns[layer] {
			if seen[nb] {
				continue
			}
			seen[nb] = true
			d := Hamming(code, h.nodes[nb].code)
			*visited++
			if res.Len() < ef || d < (*res)[0].dist {
				heap.Push(cand, distNode{nb, d})
				heap.Push(res, distNode{nb, d})
				if res.Len() > ef {
					heap.Pop(res)
				}
			}
		}
	}

	out := make([]distNode, res.Len())
	for i := len(out) - 1; i >= 0; i-- { // drain max-heap into ascending order
		out[i] = heap.Pop(res).(distNode)
	}
	return out
}

// selectNeighbors picks up to m neighbours for base from cands using the
// HNSW heuristic: prefer a candidate only when it is closer to base than
// to any already-selected neighbour (spreads links across directions
// instead of clustering them), then top up with the closest remaining if
// the heuristic left fewer than m.
func (h *hnsw) selectNeighbors(base []uint64, cands []int32, m int) []int32 {
	type dc struct {
		id   int32
		dist int
	}
	arr := make([]dc, len(cands))
	for i, id := range cands {
		arr[i] = dc{id, Hamming(base, h.nodes[id].code)}
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].dist < arr[j].dist })

	out := make([]int32, 0, m)
	for _, c := range arr {
		if len(out) >= m {
			break
		}
		keep := true
		for _, s := range out {
			if Hamming(h.nodes[c.id].code, h.nodes[s].code) < c.dist {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, c.id)
		}
	}
	if len(out) < m {
		for _, c := range arr {
			if len(out) >= m {
				break
			}
			if !slices.Contains(out, c.id) {
				out = append(out, c.id)
			}
		}
	}
	return out
}

// distNode is a (node id, distance) pair used by the search heaps.
type distNode struct {
	id   int32
	dist int
}

// minHeap orders nearest-first (the search frontier).
type minHeap []distNode

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(distNode)) }
func (h *minHeap) Pop() any          { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

// maxHeap orders farthest-first (the capped result set).
type maxHeap []distNode

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i].dist > h[j].dist }
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(distNode)) }
func (h *maxHeap) Pop() any          { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }
