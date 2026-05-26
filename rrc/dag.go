package rrc

import pb "github.com/emontenegr/spidey/gen/go/spidey/v1"

// dag is the prerequisite DAG. Dual adjacency list — two indexes over the same
// edge set. Backward: message → its prerequisites. Forward: message → its dependents.
// Global across all threads.
type dag struct {
	backward map[string][]*pb.Edge // message ID -> its prerequisites
	forward  map[string][]*pb.Edge // message ID -> its dependents
}

func newDAG() *dag {
	return &dag{
		backward: make(map[string][]*pb.Edge),
		forward:  make(map[string][]*pb.Edge),
	}
}

// AddEdge inserts an edge into both adjacency lists. The edge goes from
// (earlier message) to (later message): "to depends on from."
func (d *dag) AddEdge(edge *pb.Edge) {
	to := edge.ToMessageId
	from := edge.FromMessageId
	d.backward[to] = append(d.backward[to], edge)
	d.forward[from] = append(d.forward[from], edge)
}

// Prerequisites returns all edges pointing into messageID (its prerequisites).
func (d *dag) Prerequisites(messageID string) []*pb.Edge {
	return d.backward[messageID]
}

// Dependents returns all edges pointing out of messageID (messages that depend on it).
func (d *dag) Dependents(messageID string) []*pb.Edge {
	return d.forward[messageID]
}

// HasMessage returns true if the message has any edges (either direction).
func (d *dag) HasMessage(messageID string) bool {
	_, b := d.backward[messageID]
	_, f := d.forward[messageID]
	return b || f
}

// AllEdges returns every edge in the DAG (deduplicated via backward index).
func (d *dag) AllEdges() []*pb.Edge {
	var edges []*pb.Edge
	for _, es := range d.backward {
		edges = append(edges, es...)
	}
	return edges
}

// ThreadEdges returns edges where both endpoints belong to the given thread.
func (d *dag) ThreadEdges(threadID string) []*pb.Edge {
	var edges []*pb.Edge
	for _, es := range d.backward {
		for _, e := range es {
			if e.FromThreadId == threadID && e.ToThreadId == threadID {
				edges = append(edges, e)
			}
		}
	}
	return edges
}
