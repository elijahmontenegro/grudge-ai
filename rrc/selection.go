package rrc

import (
	"container/heap"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// selectionEntry is the internal working type used during best-first traversal.
// Mapped to pb.SelectedMessage in the returned SelectionResult.
type selectionEntry struct {
	MessageID      string
	EffectiveScore float64
	HopDepth       int
	ViaEdges       []*pb.Edge
	ThreadID       string
	CrossThread    bool
}

// edgeScoreUnderConfig returns the traversal score for an edge. Post-A4 this
// is the calibrated P(prereq) stored in Score (edge formation writes the
// Calibrator's output there), so multi-hop traversal chain-rules calibrated
// probabilities — the same currency acceptance uses — rather than raw CE
// scores gated by a flat threshold. Kept as a named function so a future
// per-edge adjustment (age decay, re-calibration) lands here.
func edgeScoreUnderConfig(edge *pb.Edge, _ EngineConfig) float64 {
	return float64(edge.Score)
}

// extractSubgraph performs best-first backward traversal from promptID through
// the DAG. Returns selected entries and a map of messages excluded due to score floor.
//
// Edges are re-projected under current config at walk time — stored
// edges that no longer clear EdgeThreshold are skipped, stored edges
// whose raw components still pass the current weights contribute
// their new fused score. This is how config change takes effect
// retroactively without a separate rebuild path.
func extractSubgraph(d *dag, promptID string, promptThreadID string, scope pb.SelectionScope, cfg EngineConfig) ([]selectionEntry, map[string]float64) {
	visited := make(map[string]bool)
	visited[promptID] = true
	belowFloor := make(map[string]float64) // messageID -> score (excluded by floor)

	pq := &priorityQueue{}
	heap.Init(pq)

	// Seed with direct prerequisites of the prompt
	for _, edge := range d.Prerequisites(promptID) {
		if edge.Source == pb.EdgeSource_EDGE_SOURCE_PROVENANCE {
			continue // provenance is a recorded structural signal, not a
			// scored prerequisite edge; it is consumed by the traversal
			// recall path (A2) and folded into acceptance (A4), never by
			// this CE-scored subgraph walk.
		}
		if !scopeAllows(edge, promptThreadID, scope) {
			continue
		}
		// Edge Score is calibrated P(prereq) (A4). Accept into the walk at
		// the precision floor (μ=0 at selection; the token-price μ is applied
		// later in the assembly shed loop). Replaces the flat EdgeThreshold.
		score := edgeScoreUnderConfig(edge, cfg)
		if !accept(score, cfg.LossRatio, 0, 0) {
			continue
		}
		crossThread := edge.FromThreadId != promptThreadID
		heap.Push(pq, &pqItem{
			entry: selectionEntry{
				MessageID:      edge.FromMessageId,
				EffectiveScore: score,
				HopDepth:       1,
				ViaEdges:       []*pb.Edge{edge},
				ThreadID:       edge.FromThreadId,
				CrossThread:    crossThread,
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
		// EffectiveScore is the product of calibrated edge probabilities along
		// the path — a genuine P(prereq) for the multi-hop chain — so the same
		// precision stance that gates formation gates reach. A deep chain whose
		// product falls below the stance stops here.
		if entry.EffectiveScore < cfg.LossRatio {
			belowFloor[entry.MessageID] = entry.EffectiveScore
			continue
		}

		selected = append(selected, entry)

		// Push prerequisites with multiplicatively decayed (chain-rule) scores
		for _, edge := range d.Prerequisites(entry.MessageID) {
			if edge.Source == pb.EdgeSource_EDGE_SOURCE_PROVENANCE {
				continue // see seed loop: provenance is not a CE-scored edge
			}
			if visited[edge.FromMessageId] {
				continue
			}
			if !scopeAllows(edge, promptThreadID, scope) {
				continue
			}
			edgeScore := edgeScoreUnderConfig(edge, cfg)
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
					ViaEdges:       append(append([]*pb.Edge{}, entry.ViaEdges...), edge),
					ThreadID:       edge.FromThreadId,
					CrossThread:    crossThread,
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
		var filtered []*pb.Edge
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
func scopeAllows(edge *pb.Edge, promptThreadID string, scope pb.SelectionScope) bool {
	if scope == pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS {
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
