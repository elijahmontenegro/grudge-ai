package rrc

import (
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
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
func (e *Engine) RecordProvenance(anchor *pb.Message, contributors []Contributor) []*pb.Edge {
	if anchor == nil || len(contributors) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	seen := make(map[string]bool, len(contributors))
	var edges []*pb.Edge
	for _, c := range contributors {
		if c.MessageID == "" || c.MessageID == anchor.Id || seen[c.MessageID] {
			continue
		}
		seen[c.MessageID] = true
		edge := &pb.Edge{
			FromMessageId: c.MessageID,
			ToMessageId:   anchor.Id,
			Score:         float32(c.Weight),
			Source:        pb.EdgeSource_EDGE_SOURCE_PROVENANCE,
			DetectedAt:    timestamppb.Now(),
			FromThreadId:  c.ThreadID,
			ToThreadId:    anchor.ThreadId,
		}
		e.dag.AddEdge(edge)
		edges = append(edges, edge)
	}
	return edges
}

// provenanceReach walks provenance edges backward from the active-discourse
// cone (the Local Context message IDs) and returns candidate message IDs
// ranked by accumulated descendant *mass* — the recall path that surfaces
// required-but-low-similarity messages the top-K cosine prefilter amputates
// before they can ever be scored.
//
// Mass, not count: each provenance edge carries a contribution weight (its
// Score), and a node's mass is the sum over the cone of (weight along the
// path). A single thin spine to an abandoned root scores far below the broad
// fan-in to a live root, so the ever-present recent-tail spine does not flood
// recall. Multi-hop reach uses the chain rule (product of edge weights along
// the path) — the same shape A4 will calibrate; here it is the raw banked
// weight, uncalibrated.
//
// Excluded from the result: the cone itself (already Local Context) and
// anything failing scope. Bounded by provenanceReachCap; the boolean return
// reports whether the cap truncated the walk (for telemetry — no silent cap).
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
func (e *Engine) provenanceReach(coneIDs []string, coneThreadID string, scope pb.SelectionScope) (map[string]float64, bool) {
	cone := make(map[string]bool, len(coneIDs))
	for _, id := range coneIDs {
		cone[id] = true
	}

	mass := make(map[string]float64)
	// Best-first by accumulated mass. Seed: the cone's direct provenance
	// prerequisites (messages the cone's turns were generated from).
	type reachItem struct {
		id   string
		mass float64
	}
	var frontier []reachItem
	for _, id := range coneIDs {
		for _, edge := range e.dag.Prerequisites(id) {
			if edge.Source != pb.EdgeSource_EDGE_SOURCE_PROVENANCE {
				continue
			}
			if !scopeAllows(edge, coneThreadID, scope) {
				continue
			}
			from := edge.FromMessageId
			if cone[from] {
				continue
			}
			mass[from] += float64(edge.Score)
			frontier = append(frontier, reachItem{id: from, mass: float64(edge.Score)})
		}
	}

	// Walk backward, chain-ruling the mass, until the frontier drains or the
	// cap is hit. visited guards against provenance cycles (shouldn't occur
	// — provenance is generation-ordered — but defensive).
	visited := make(map[string]bool, len(cone))
	for id := range cone {
		visited[id] = true
	}
	truncated := false
	for len(frontier) > 0 {
		item := frontier[0]
		frontier = frontier[1:]
		if visited[item.id] {
			continue
		}
		visited[item.id] = true
		if len(visited)-len(cone) > provenanceReachCap {
			truncated = true
			break
		}
		for _, edge := range e.dag.Prerequisites(item.id) {
			if edge.Source != pb.EdgeSource_EDGE_SOURCE_PROVENANCE {
				continue
			}
			if !scopeAllows(edge, coneThreadID, scope) {
				continue
			}
			from := edge.FromMessageId
			if cone[from] {
				continue
			}
			// Chain rule: contribution decays multiplicatively along the path.
			contributed := item.mass * float64(edge.Score)
			mass[from] += contributed
			if !visited[from] {
				frontier = append(frontier, reachItem{id: from, mass: contributed})
			}
		}
	}
	return mass, truncated
}
