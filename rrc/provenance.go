package rrc

import (
	"container/heap"
	"sort"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// provenanceReachCap bounds how many provenance-reached candidate messages
// the walk surfaces per turn. This is the traversal's compute-invariance
// backstop: without it a deeply-provenanced corpus could fan out without
// limit and break RRC's per-step invariance promise. Kept generous
// relative to RerankTopK so it adds reach (the low-similarity roots the
// cosine prefilter amputates) without dominating the pool. Surfaced in
// telemetry so a silent cap is never mistaken for "nothing more to reach."
const provenanceReachCap = 64

// Contributor is one message that fed a generated turn, paired with the
// weight of its contribution. The weight is P(prereq)-shaped in [0,1]:
//   - Local Context messages contributed by being the active discourse the
//     turn was interpreted from — definitionally load-bearing, weight 1.0.
//   - Selected prerequisites contributed at their selection score (the
//     engine's current best estimate that they were required).
//
// The weight is what makes descendant *mass* (not mere count) computable
// later: a phatic opener present-but-never-load-bearing banks ~0; a real
// framing root banks near 1. See the structural-lift design.
type Contributor struct {
	MessageID string
	ThreadID  string
	Weight    float64
}

// RecordProvenance records, at generation time, that the turn ending at
// anchor was generated from the given contributors. It writes one
// EDGE_SOURCE_PROVENANCE edge per contributor (from contributor → anchor)
// into the DAG and returns them for persistence.
//
// Provenance is recorded fact, not similarity score: these edges are legible
// even where cosine similarity is near zero, which is exactly the band where
// roots live. They are deliberately NOT consumed by extractSubgraph (the
// CE-scored prerequisite walk skips EDGE_SOURCE_PROVENANCE); they exist for
// the traversal recall path (A2) and the calibrated acceptance fusion (A4).
//
// The contribution weight rides in the edge's Score field (the schema and
// the (from,to,source) primary key let a provenance edge coexist with a
// cross-encoder edge for the same pair). Self-edges (contributor == anchor)
// and empty ids are skipped. Callers hold no lock; RecordProvenance takes
// the engine mutex like the other DAG mutators.
func (e *Engine) RecordProvenance(anchor *threadv1.Message, contributors []Contributor) []*rrcv1.Edge {
	if anchor == nil || len(contributors) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	seen := make(map[string]bool, len(contributors))
	var edges []*rrcv1.Edge
	for _, c := range contributors {
		if c.MessageID == "" || c.MessageID == anchor.Id || seen[c.MessageID] {
			continue
		}
		seen[c.MessageID] = true
		edge := &rrcv1.Edge{
			FromMessageId: c.MessageID,
			ToMessageId:   anchor.Id,
			Score:         float32(c.Weight),
			Source:        rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE,
			DetectedAt:    timestamppb.Now(),
			FromThreadId:  c.ThreadID,
			ToThreadId:    anchor.ThreadId,
			// Selected-contributor weights are raw-CE via-path products —
			// instrument-relative like every observation.
			ScorerModel: e.cfg.ScorerModelID,
		}
		if !e.admitEdge(edge) {
			continue
		}
		edges = append(edges, edge)
	}
	return edges
}

// AddEdges admits externally constructed edges into the live DAG under
// the engine mutex — the runtime counterpart of the construction-time
// WithLoadedEdges. Each edge passes through the edge filter (when
// installed); the returned slice holds the edges actually admitted.
func (e *Engine) AddEdges(edges []*rrcv1.Edge) []*rrcv1.Edge {
	if len(edges) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	admitted := make([]*rrcv1.Edge, 0, len(edges))
	for _, edge := range edges {
		if edge == nil || !e.admitEdge(edge) {
			continue
		}
		admitted = append(admitted, edge)
	}
	return admitted
}

// provenanceReach walks provenance edges backward from the anchor set —
// the Local Context MEMBERSHIP, i.e. the delivered window (the
// immediately preceding turn ∪ the current turn). The window-tail is the
// graph's entry point for a fresh turn, whose own messages have no
// incoming edges yet. Returns candidate message IDs ranked by
// accumulated descendant *mass*: the recall path that surfaces
// required-but-low-similarity messages the top-K cosine prefilter
// amputates before they can ever be scored.
//
// Mass, not count: each provenance edge carries a contribution weight (its
// Score), and a node's mass is the sum over the anchor set of (weight along
// the path). A single thin chain to an abandoned root scores far below the
// broad fan-in to a live root, so the ever-present window-tail does not
// flood recall. Multi-hop reach uses the chain rule (product of edge
// weights along the path) — the same shape A4 will calibrate; here it is
// the raw banked weight, uncalibrated.
//
// Excluded from the result: the anchor set itself (it is DELIVERED —
// nothing on the wire is ever re-retrieved; what surfaces is its
// undelivered ancestry) and anything failing scope. Bounded by
// provenanceReachCap; the boolean return reports whether the cap truncated
// the walk (for telemetry — no silent cap).
//
// NOTE (μ-availability seam, per plan): the ideal stop is "walk until path
// value < μ (the budget shadow price)", but μ is not available at recall
// time (it prices the pool this walk produces). Until A4 resolves that
// ordering (warm-start μ / fixpoint), the walk uses a fixed mass floor and
// the cap. This is the known open sub-part of the recall change; it does not
// affect the acceptance-side mechanism.
// Concurrency: like the other DAG reads in SelectPrerequisites (which is its
// only caller), provenanceReach does NOT take e.mu — it runs under the lock
// Assemble already holds around SelectPrerequisites. Taking e.mu here would
// self-deadlock against that outer lock.
func (e *Engine) provenanceReach(anchorIDs []string, coneThreadID string, scope threadv1.SelectionScope) (map[string]float64, map[string]string, bool) {
	return provenanceMassWalk(e.dag, anchorIDs, coneThreadID, scope)
}

// ProvenanceMass computes the chain-ruled provenance mass of every
// message reachable from the anchor set (the delivered Local Context
// window's membership) through the given edge set — the same walk the
// engine's recall path runs, exposed over an arbitrary edge set so
// offline replay (mass calibration over corpus history, filtered to
// edges as-of a turn) computes mass with the engine's exact semantics
// instead of a drifting reimplementation. Non-provenance edges are
// ignored by the walk itself.
func ProvenanceMass(edges []*rrcv1.Edge, anchorIDs []string, coneThreadID string, scope threadv1.SelectionScope) (map[string]float64, bool) {
	d := newDAG()
	for _, edge := range edges {
		if edge != nil {
			d.AddEdge(edge)
		}
	}
	mass, _, truncated := provenanceMassWalk(d, anchorIDs, coneThreadID, scope)
	return mass, truncated
}

// provenanceMassWalk is the shared walk body. See provenanceReach for
// the mass semantics and the cap contract. Anchor members — the delivered
// window — seed reachability and receive mass at contribution 1.0, and
// are excluded from the output: delivered never surfaces; the walk's
// yield is the window's undelivered ancestry. It also returns each
// reached message's thread (read off the provenance edges it walks), so
// the caller can build edges for reached messages without a full-corpus
// lookup.
// provIn is one incoming provenance relation: the contributor and its
// recorded contribution weight.
type provIn struct {
	from   string
	weight float64
}

// boundEntry / boundHeap: max-heap over best-path-product bounds with a
// deterministic id tie-break. Lazy deletion — improved bounds push
// duplicates; stale entries are skipped at pop against best[].
type boundEntry struct {
	id    string
	bound float64
}

type boundHeap []boundEntry

func (h boundHeap) Len() int { return len(h) }
func (h boundHeap) Less(i, j int) bool {
	if h[i].bound != h[j].bound {
		return h[i].bound > h[j].bound
	}
	return h[i].id < h[j].id
}
func (h boundHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *boundHeap) Push(x any)   { *h = append(*h, x.(boundEntry)) }
func (h *boundHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func provenanceMassWalk(d *dag, anchorIDs []string, coneThreadID string, scope threadv1.SelectionScope) (map[string]float64, map[string]string, bool) {
	anchors := make(map[string]bool, len(anchorIDs))
	for _, id := range anchorIDs {
		anchors[id] = true
	}

	// Phase 1 — capped reachability, BEST-FIRST by path-product bound.
	// The cap is a compute-resource rule (it bounds scorer pairs per
	// step — permissible); WHICH nodes it keeps is a truth question, so
	// truncation keeps the top-K by the law's own currency: the
	// strongest recorded path into the anchor set. Weights ≤ 1 make
	// expansion monotone (Dijkstra on the reversed graph; max-product ≡
	// min-cost under -log), so nodes settle in non-increasing bound
	// order and the settled set provably holds the highest-bound
	// reachable nodes. The bound is the BEST single path — a lower bound
	// on final path-SUM mass — so ordering keeps the dominant term. (The
	// old layered BFS kept the hop-NEAREST K instead: an arbitrary WHICH
	// leaking into truth whenever the cap bit.) Ties settle in id order
	// for determinism; weights clamp to [0,1] for ORDERING only —
	// phase-2 mass math is untouched.
	inReach := make(map[string]bool)
	threadByID := make(map[string]string)
	settled := make(map[string]bool, len(anchorIDs))
	for _, id := range anchorIDs {
		settled[id] = true
	}
	contributorsOf := func(id string) []provIn {
		var out []provIn
		for _, edge := range d.Prerequisites(id) {
			if edge.Source != rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE {
				continue
			}
			if !scopeAllows(edge, coneThreadID, scope) {
				continue
			}
			threadByID[edge.FromMessageId] = edge.FromThreadId
			out = append(out, provIn{from: edge.FromMessageId, weight: float64(edge.Score)})
		}
		return out
	}
	clamp01 := func(w float64) float64 {
		if w < 0 {
			return 0
		}
		if w > 1 {
			return 1
		}
		return w
	}
	best := make(map[string]float64)
	h := &boundHeap{}
	push := func(id string, bound float64) {
		if settled[id] {
			return
		}
		if cur, ok := best[id]; !ok || bound > cur {
			best[id] = bound
			heap.Push(h, boundEntry{id: id, bound: bound})
		}
	}
	for _, a := range anchorIDs {
		for _, in := range contributorsOf(a) {
			push(in.from, clamp01(in.weight))
		}
	}
	truncated := false
	for h.Len() > 0 {
		top := heap.Pop(h).(boundEntry)
		if settled[top.id] || top.bound < best[top.id] {
			continue // already settled, or a stale lazy-deletion duplicate
		}
		if len(inReach) >= provenanceReachCap {
			truncated = true // a valid unsettled candidate remains beyond the cap
			break
		}
		settled[top.id] = true
		inReach[top.id] = true
		for _, in := range contributorsOf(top.id) {
			push(in.from, top.bound*clamp01(in.weight))
		}
	}

	// Phase 2 — exact path-sum mass over the capped subgraph. A node's
	// mass is the sum, over its provenance edges into reach ∪ anchors, of
	// edge weight × the dependent's contribution (1.0 for anchor members,
	// the dependent's own FINAL mass otherwise) — the chain rule summed
	// over every path into the anchor set. A node finalizes only after every
	// in-reach dependent has finalized, so multi-path (diamond) mass
	// accumulates fully before it propagates and the result is
	// independent of edge order. Provenance is generation-ordered
	// (contributor → newer anchor), so the subgraph is a DAG; a defensive
	// cycle leftover finalizes with its partial sum, in sorted order.
	type out struct {
		to     string
		weight float64
	}
	outgoing := make(map[string][]out, len(inReach))
	pending := make(map[string]int, len(inReach))
	members := make([]string, 0, len(inReach))
	for id := range inReach {
		members = append(members, id)
	}
	sort.Strings(members)
	targets := append(append([]string(nil), anchorIDs...), members...)
	sort.Strings(targets)
	for _, u := range targets {
		for _, edge := range d.Prerequisites(u) {
			if edge.Source != rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE {
				continue
			}
			if !scopeAllows(edge, coneThreadID, scope) {
				continue
			}
			from := edge.FromMessageId
			if anchors[from] || !inReach[from] {
				continue
			}
			outgoing[from] = append(outgoing[from], out{to: u, weight: float64(edge.Score)})
			if !anchors[u] && inReach[u] {
				pending[from]++
			}
		}
	}

	mass := make(map[string]float64, len(inReach))
	finalized := make(map[string]bool, len(inReach))
	finalize := func(v string) []string {
		var m float64
		for _, e := range outgoing[v] {
			switch {
			case anchors[e.to]:
				m += e.weight
			case inReach[e.to]:
				m += e.weight * mass[e.to]
			}
		}
		mass[v] = m
		finalized[v] = true
		var freed []string
		for _, in := range contributorsOf(v) {
			if !inReach[in.from] || finalized[in.from] {
				continue
			}
			pending[in.from]--
			if pending[in.from] == 0 {
				freed = append(freed, in.from)
			}
		}
		sort.Strings(freed)
		return freed
	}
	var queue []string
	for _, v := range members {
		if pending[v] == 0 {
			queue = append(queue, v)
		}
	}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if finalized[v] {
			continue
		}
		queue = append(queue, finalize(v)...)
	}
	for _, v := range members { // defensive cycle leftovers
		if !finalized[v] {
			finalize(v)
		}
	}

	return mass, threadByID, truncated
}
