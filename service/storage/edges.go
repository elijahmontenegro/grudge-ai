package storage

import (
	"time"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// InsertEdge persists a DAG edge.
func (d *DB) InsertEdge(e *pb.Edge) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO edges
		 (from_message_id, to_message_id, score, source, cross_encoder_score,
		  qud_weight, temporal_proximity, detected_at, from_thread_id, to_thread_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.FromMessageId, e.ToMessageId, e.Score, int(e.Source),
		e.CrossEncoderScore, e.QudWeight, e.TemporalProximity,
		e.DetectedAt.AsTime(), e.FromThreadId, e.ToThreadId,
	)
	return err
}

// AllEdges loads all DAG edges for engine startup.
func (d *DB) AllEdges() ([]*pb.Edge, error) {
	rows, err := d.Query(
		`SELECT from_message_id, to_message_id, score, source, cross_encoder_score,
		        qud_weight, temporal_proximity, detected_at, from_thread_id, to_thread_id
		 FROM edges`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []*pb.Edge
	for rows.Next() {
		e := &pb.Edge{}
		var sourceInt int
		var detectedAt time.Time
		if err := rows.Scan(
			&e.FromMessageId, &e.ToMessageId, &e.Score, &sourceInt,
			&e.CrossEncoderScore, &e.QudWeight, &e.TemporalProximity,
			&detectedAt, &e.FromThreadId, &e.ToThreadId,
		); err != nil {
			return nil, err
		}
		e.Source = pb.EdgeSource(sourceInt)
		e.DetectedAt = timestamppb.New(detectedAt)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// DeleteEdgesForThread removes all edges involving a thread.
func (d *DB) DeleteEdgesForThread(threadID string) error {
	_, err := d.Exec(
		`DELETE FROM edges WHERE from_thread_id = ? OR to_thread_id = ?`,
		threadID, threadID,
	)
	return err
}
