package rrc

import (
	"container/heap"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// selectionEntry is the internal working type used during best-first traversal.
// Mapped to rrcv1.SelectedMessage in the returned SelectionResult.
type selectionEntry struct {
	MessageID      string
	EffectiveScore float64
	HopDepth       int
	ViaEdges       []*rrcv1.Edge
	ThreadID       string
	CrossThread    bool
	// ProvenanceWeight is the RAW dependency evidence along the via-path:
	// the product of CrossEncoderScore over its edges. This — never the
	// calibrated/mass-lifted EffectiveScore and never MMR's rewrite — is
	// what provenance banking records: banking a lifted score feeds the
	// lift back into next turn's mass (measured echo: raw sim 0.36 banked
	// as 0.99, re-lifted every turn). Stamped here because transitive
	// reduction may prune ViaEdges, making the product unrecoverable later.
	ProvenanceWeight float64
}

// Traversal currency — the perishable-inference law: an inference
// (calibrated P, effective score, MMR rewrite) may be consumed only
// within the selection event that produced it; anything consumed later
// must be an OBSERVATION (raw sim, raw evidence weight) interpreted by
// the CURRENT instrument. Stored edge.Score is a past event's verdict:
// its instrument (A,B,C as fitted then) and its circumstance (the mass
// that justified delivery into THAT turn) are both foreign to a later
// walk. Consuming it fossilized the lift into the transitive pull — a
// mass-recalled edge (sim 0.38, accepted at P≈0.83 via mass) read as
// 0.83 of dependency strength when the observed dependency is
// σ(A·0.38+C) ≈ 0.13 — the same laundering class the provenance
// channel's raw-weight banking fix killed.

// seedEdgeScore is the hop-1 currency: the stored detection verdict.
// A seed edge points into the CURRENT anchor — a message that did not
// exist before this turn — so its stored Score is THIS selection
// event's own acceptance confidence, consumed within the event that
// produced it: fresh by construction.
func seedEdgeScore(edge *rrcv1.Edge) float64 {
	return float64(edge.Score)
}

// relationalEdgeScore is the hop≥2 currency: the recorded OBSERVATION
// (CrossEncoderScore, scorer units) interpreted by THIS event's
// instrument — the measured noise floor. An edge whose raw observation
// beats the whole reference sample is a detected dependency and carries
// its detection confidence; one that doesn't is indistinguishable from
// background and contributes nothing. Interpreting at read time is what
// makes history genuinely re-gated per event with no rewrite — and it
// is the perishable-inference law verbatim: observations keep,
// interpreted by the current instrument; verdicts never outlive their
// event. With no measurable floor (standalone Select, cold start) the
// raw observation itself is the value — an honest scorer-units
// fallback gated by the stance downstream.
func relationalEdgeScore(edge *rrcv1.Edge, refFloor []float64) float64 {
	raw := float64(edge.CrossEncoderScore)
	if len(refFloor) >= minReferenceSample {
		p := nullP(raw, refFloor)
		if p > 1.0/float64(len(refFloor)+1) {
			return 0 // background at this event's floor
		}
		return confidenceFromBits(surprisalBits(p))
	}
	return raw
}

// extractSubgraph performs best-first backward traversal from promptID through
// the DAG. Returns selected entries and a map of messages excluded due to score floor.
//
// Hop≥2 edges are re-derived from their recorded observations under the
// current calibrator at walk time (relationalEdgeScore) — stored edges
// whose observed dependency no longer clears calibrated acceptance are
// skipped. This is how calibration takes effect retroactively without a
// separate rebuild path: the fit moves, every historical edge re-gates,
// nothing is rewritten. Hop-1 edges are this selection event's own
// fresh verdicts (seedEdgeScore).
func extractSubgraph(d *dag, promptID string, promptThreadID string, scope threadv1.SelectionScope, cfg EngineConfig, refFloor []float64) ([]selectionEntry, map[string]float64) {
	visited := make(map[string]bool)
	visited[promptID] = true
	belowFloor := make(map[string]float64) // messageID -> score (excluded by floor)

	pq := &priorityQueue{}
	heap.Init(pq)

	// Seed with direct prerequisites of the prompt
	for _, edge := range d.Prerequisites(promptID) {
		if edge.Source == rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE {
			continue // provenance is a recorded structural signal, not a
			// scored prerequisite edge; it is consumed by the traversal
			// recall path (A2) and folded into acceptance (A4), never by
			// this CE-scored subgraph walk.
		}
		if !scopeAllows(edge, promptThreadID, scope) {
			continue
		}
		// Seed edges carry THIS event's calibrated verdict (A4) — accept
		// into the walk at the precision floor (μ=0 at selection; the
		// token-price μ is applied later in the assembly shed loop).
		// Replaces the flat EdgeThreshold.
		score := seedEdgeScore(edge)
		if !accept(score, cfg.LossRatio, 0, 0) {
			continue
		}
		crossThread := edge.FromThreadId != promptThreadID
		heap.Push(pq, &pqItem{
			entry: selectionEntry{
				MessageID:        edge.FromMessageId,
				EffectiveScore:   score,
				HopDepth:         1,
				ViaEdges:         []*rrcv1.Edge{edge},
				ThreadID:         edge.FromThreadId,
				CrossThread:      crossThread,
				ProvenanceWeight: float64(edge.CrossEncoderScore),
			},
			priority: score,
		})
	}

	var selected []selectionEntry
	for pq.Len() > 0 {
		item := heap.Pop(pq).(*pqItem)
		entry := item.entry

		if visited[entry.MessageID] {
			continue
		}
		visited[entry.MessageID] = true

		// Chain-ruled calibrated probability floor (A4, replacing ScoreFloor).
		// EffectiveScore factorizes as (this event's entry verdict at hop 1)
		// × (current-instrument relational strengths beyond): circumstance
		// prices entry into the present turn exactly once; past that, only
		// observed dependency chains. The same precision stance that gates
		// formation gates reach; a deep chain whose product falls below the
		// stance stops here.
		if entry.EffectiveScore < cfg.LossRatio {
			belowFloor[entry.MessageID] = entry.EffectiveScore
			continue
		}

		selected = append(selected, entry)

		// Push prerequisites with multiplicatively decayed (chain-rule) scores
		for _, edge := range d.Prerequisites(entry.MessageID) {
			if edge.Source == rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE {
				continue // see seed loop: provenance is not a CE-scored edge
			}
			if visited[edge.FromMessageId] {
				continue
			}
			if !scopeAllows(edge, promptThreadID, scope) {
				continue
			}
			edgeScore := relationalEdgeScore(edge, refFloor)
			if !accept(edgeScore, cfg.LossRatio, 0, 0) {
				continue
			}
			effectiveScore := edgeScore * entry.EffectiveScore
			crossThread := edge.FromThreadId != promptThreadID
			heap.Push(pq, &pqItem{
				entry: selectionEntry{
					MessageID:      edge.FromMessageId,
					EffectiveScore: effectiveScore,
					HopDepth:       entry.HopDepth + 1,
					ViaEdges:       append(append([]*rrcv1.Edge{}, entry.ViaEdges...), edge),
					ThreadID:       edge.FromThreadId,
					CrossThread:    crossThread,
					// Chain rule over RAW evidence — mirrors the mass
					// walk's own path-product semantics.
					ProvenanceWeight: entry.ProvenanceWeight * float64(edge.CrossEncoderScore),
				},
				priority: effectiveScore,
			})
		}
	}

	return selected, belowFloor
}

// transitiveReduction removes redundant edges from the selected subgraph.
// If A→B→C and A→C both exist, removes A→C. Operates on selection entries.
func transitiveReduction(selected []selectionEntry) []selectionEntry {
	if len(selected) <= 1 {
		return selected
	}

	// Build a reachability set for each node in the subgraph
	ids := make(map[string]bool)
	for _, s := range selected {
		ids[s.MessageID] = true
	}

	// For each selected message, build direct prerequisite set from via_edges
	directPrereqs := make(map[string]map[string]bool)
	for _, s := range selected {
		prereqs := make(map[string]bool)
		for _, e := range s.ViaEdges {
			if ids[e.FromMessageId] {
				prereqs[e.FromMessageId] = true
			}
		}
		directPrereqs[s.MessageID] = prereqs
	}

	// Compute transitive reachability (DFS from each node)
	reachable := make(map[string]map[string]bool)
	for id := range ids {
		visited := make(map[string]bool)
		var dfs func(string)
		dfs = func(node string) {
			for prereq := range directPrereqs[node] {
				if !visited[prereq] {
					visited[prereq] = true
					dfs(prereq)
				}
			}
		}
		dfs(id)
		reachable[id] = visited
	}

	// Remove edges that are transitively reachable
	for i := range selected {
		var filtered []*rrcv1.Edge
		for _, e := range selected[i].ViaEdges {
			isRedundant := false
			// Check if from is reachable from any other direct prereq
			for _, otherEdge := range selected[i].ViaEdges {
				if otherEdge.FromMessageId == e.FromMessageId {
					continue
				}
				if reachable[otherEdge.FromMessageId][e.FromMessageId] {
					isRedundant = true
					break
				}
			}
			if !isRedundant {
				filtered = append(filtered, e)
			}
		}
		selected[i].ViaEdges = filtered
	}

	return selected
}

// scopeAllows returns true if the edge is allowed under the given scope filter.
func scopeAllows(edge *rrcv1.Edge, promptThreadID string, scope threadv1.SelectionScope) bool {
	if scope == threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS {
		return true
	}
	// Thread-scoped: both endpoints must be in the prompt's thread
	return edge.FromThreadId == promptThreadID && edge.ToThreadId == promptThreadID
}

// --- Priority queue for best-first traversal ---

type pqItem struct {
	entry    selectionEntry
	priority float64
	index    int
}

type priorityQueue []*pqItem

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].priority > pq[j].priority } // max-heap
func (pq priorityQueue) Swap(i, j int)      { pq[i], pq[j] = pq[j], pq[i]; pq[i].index = i; pq[j].index = j }
func (pq *priorityQueue) Push(x any) {
	item := x.(*pqItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*pq = old[:n-1]
	return item
}
