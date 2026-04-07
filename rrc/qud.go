package rrc

import pb "github.com/emontenegr/spidey/gen/go/spidey/v1"

// QUDGraph is the in-memory representation of a thread's QUD state, extended
// with derived edges fed back into the DAG. Per-thread — one QUDGraph per thread.
type QUDGraph struct {
	graph        *pb.QUDGraph
	derivedEdges []*pb.Edge // QUD-sourced edges fed back into the DAG
}

func newQUDGraph() *QUDGraph {
	return &QUDGraph{
		graph: &pb.QUDGraph{},
	}
}

// AddQUD adds a new QUD entry to the graph.
func (q *QUDGraph) AddQUD(qud *pb.QUD) {
	q.graph.Quds = append(q.graph.Quds, qud)
	if qud.Status == pb.QUDStatus_QUD_STATUS_OPEN {
		q.graph.ActiveStack = append(q.graph.ActiveStack, qud.Id)
	}
}

// UpdateStatus updates a QUD's status and records addressing messages.
// Enrichment is additive only — status only moves toward resolution.
func (q *QUDGraph) UpdateStatus(qudID string, status pb.QUDStatus, addressedBy string) {
	for _, qud := range q.graph.Quds {
		if qud.Id != qudID {
			continue
		}
		// Only allow forward transitions: OPEN → PARTIALLY_ADDRESSED → RESOLVED
		if status <= qud.Status {
			return
		}
		qud.Status = status
		if addressedBy != "" {
			qud.AddressedBy = append(qud.AddressedBy, addressedBy)
		}
		// Remove from active stack if resolved
		if status == pb.QUDStatus_QUD_STATUS_RESOLVED {
			q.removeFromActiveStack(qudID)
		}
		return
	}
}

// AddDerivedEdge records a QUD-sourced edge. The engine feeds these into the DAG.
func (q *QUDGraph) AddDerivedEdge(edge *pb.Edge) {
	q.derivedEdges = append(q.derivedEdges, edge)
}

// DerivedEdges returns all QUD-sourced edges for DAG integration.
func (q *QUDGraph) DerivedEdges() []*pb.Edge {
	return q.derivedEdges
}

// Proto returns the underlying proto representation for persistence.
func (q *QUDGraph) Proto() *pb.QUDGraph {
	return q.graph
}

// FindByEstablisher returns QUDs established by the given message ID.
func (q *QUDGraph) FindByEstablisher(messageID string) []*pb.QUD {
	var result []*pb.QUD
	for _, qud := range q.graph.Quds {
		if qud.EstablishedBy == messageID {
			result = append(result, qud)
		}
	}
	return result
}

// ActiveQUDs returns the current active QUD IDs (most recent first).
func (q *QUDGraph) ActiveQUDs() []string {
	return q.graph.ActiveStack
}

func (q *QUDGraph) removeFromActiveStack(qudID string) {
	stack := q.graph.ActiveStack
	for i, id := range stack {
		if id == qudID {
			q.graph.ActiveStack = append(stack[:i], stack[i+1:]...)
			return
		}
	}
}
