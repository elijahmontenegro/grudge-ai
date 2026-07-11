package storage

import (
	"time"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// InsertEdge persists a DAG edge.
func (d *DB) InsertEdge(e *rrcv1.Edge) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO edges
		 (from_message_id, to_message_id, score, source, cross_encoder_score,
		  detected_at, from_thread_id, to_thread_id, scorer_model)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.FromMessageId, e.ToMessageId, e.Score, int(e.Source),
		e.CrossEncoderScore,
		e.DetectedAt.AsTime(), e.FromThreadId, e.ToThreadId, e.ScorerModel,
	)
	return err
}

// AllEdges loads all DAG edges for engine startup.
func (d *DB) AllEdges() ([]*rrcv1.Edge, error) {
	rows, err := d.Query(
		`SELECT from_message_id, to_message_id, score, source, cross_encoder_score,
		        detected_at, from_thread_id, to_thread_id, scorer_model
		 FROM edges
		 ORDER BY from_message_id, to_message_id, source`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []*rrcv1.Edge
	for rows.Next() {
		e := &rrcv1.Edge{}
		var sourceInt int
		var detectedAt time.Time
		if err := rows.Scan(
			&e.FromMessageId, &e.ToMessageId, &e.Score, &sourceInt,
			&e.CrossEncoderScore,
			&detectedAt, &e.FromThreadId, &e.ToThreadId, &e.ScorerModel,
		); err != nil {
			return nil, err
		}
		e.Source = rrcv1.EdgeSource(sourceInt)
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

// CountProvenanceEdges returns the number of recorded provenance
// edges — the corpus-structure watermark the mass-refit arming check
// reads (see substrate.Holder).
func (d *DB) CountProvenanceEdges() (int, error) {
	var n int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE source = ?`,
		int(rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE),
	).Scan(&n)
	return n, err
}
