package rrc

import (
	"container/heap"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
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

// edgeScoreUnderConfig computes an edge's fused score from its stored
// raw components (CrossEncoderScore, TemporalProximity) using the
// CURRENT engine config. Stored edges' Score field was set to whatever
// config was in effect at creation time — using it directly would
// freeze walks against historical config. By recomputing from raw
// components on every walk, the DAG becomes a derived view of
// (stored_edges, current_config): change config and the graph
// reprojects immediately, no rebuild or invalidation step needed.
func edgeScoreUnderConfig(edge *pb.Edge, cfg EngineConfig) float64 {
	return FuseScore(cfg, float64(edge.CrossEncoderScore), float64(edge.TemporalProximity))
}

// extractSubgraph performs best-first backward traversal from promptID through
// the DAG. Returns selected entries and a map of messages excluded due to score floor.
//
// Edges are re-projected under current config at walk time — stored
// edges that no longer clear EdgeThreshold are skipped, stored edges
// whose raw components still pass the current weights contribute
// their new fused score. This is how config change takes effect
// retroactively without a separate rebuild path.
func extractSubgraph(dag *DAG, promptID string, promptThreadID string, scope pb.SelectionScope, cfg EngineConfig) ([]selectionEntry, map[string]float64) {
	visited := make(map[string]bool)
	visited[promptID] = true
	belowFloor := make(map[string]float64) // messageID -> score (excluded by floor)

	pq := &priorityQueue{}
	heap.Init(pq)

	// Seed with direct prerequisites of the prompt
	for _, edge := range dag.Prerequisites(promptID) {
		if !scopeAllows(edge, promptThreadID, scope) {
			continue
		}
		score := edgeScoreUnderConfig(edge, cfg)
		if score < cfg.EdgeThreshold {
			continue // edge doesn't qualify under current config
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

		if entry.EffectiveScore < cfg.ScoreFloor {
			belowFloor[entry.MessageID] = entry.EffectiveScore
			continue
		}

		selected = append(selected, entry)

		// Push prerequisites with multiplicatively decayed scores
		for _, edge := range dag.Prerequisites(entry.MessageID) {
			if visited[edge.FromMessageId] {
				continue
			}
			if !scopeAllows(edge, promptThreadID, scope) {
				continue
			}
			edgeScore := edgeScoreUnderConfig(edge, cfg)
			if edgeScore < cfg.EdgeThreshold {
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

func (pq priorityQueue) Len() int            { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool  { return pq[i].priority > pq[j].priority } // max-heap
func (pq priorityQueue) Swap(i, j int)       { pq[i], pq[j] = pq[j], pq[i]; pq[i].index = i; pq[j].index = j }
func (pq *priorityQueue) Push(x any)         { item := x.(*pqItem); item.index = len(*pq); *pq = append(*pq, item) }
func (pq *priorityQueue) Pop() any           { old := *pq; n := len(old); item := old[n-1]; old[n-1] = nil; item.index = -1; *pq = old[:n-1]; return item }
