package storage

import (
	"encoding/json"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// InsertQUD persists a QUD entry.
func (d *DB) InsertQUD(threadID string, qud *pb.QUD) error {
	addressedBy, _ := json.Marshal(qud.AddressedBy)
	_, err := d.Exec(
		`INSERT OR REPLACE INTO quds (id, thread_id, question, established_by, parent_qud_id, status, addressed_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		qud.Id, threadID, qud.Question, qud.EstablishedBy,
		qud.ParentQudId, int(qud.Status), string(addressedBy),
	)
	return err
}

// QUDGraphForThread loads the QUD graph for a thread.
func (d *DB) QUDGraphForThread(threadID string) (*pb.QUDGraph, error) {
	rows, err := d.Query(
		`SELECT id, question, established_by, parent_qud_id, status, addressed_by
		 FROM quds WHERE thread_id = ?`, threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	graph := &pb.QUDGraph{}
	for rows.Next() {
		qud := &pb.QUD{}
		var statusInt int
		var addressedByJSON string
		if err := rows.Scan(
			&qud.Id, &qud.Question, &qud.EstablishedBy,
			&qud.ParentQudId, &statusInt, &addressedByJSON,
		); err != nil {
			return nil, err
		}
		qud.Status = pb.QUDStatus(statusInt)
		json.Unmarshal([]byte(addressedByJSON), &qud.AddressedBy)
		graph.Quds = append(graph.Quds, qud)

		if qud.Status == pb.QUDStatus_QUD_STATUS_OPEN {
			graph.ActiveStack = append(graph.ActiveStack, qud.Id)
		}
	}
	return graph, rows.Err()
}

// AllThreadsWithQUDs returns thread IDs that have QUD data.
func (d *DB) AllThreadsWithQUDs() ([]string, error) {
	rows, err := d.Query(`SELECT DISTINCT thread_id FROM quds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
