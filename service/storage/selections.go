package storage

import (
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/proto"
)

// Selection audit trail — durable record of every SelectionResult
// produced at a Retrieval Event. Without persistence the in-memory
// introspection map is wiped on every restart and historical turns
// become unauditable. Schema is in db.go.
//
// The full SelectionResult proto is serialized as a BLOB. This keeps
// the structure of selected/excluded items (scores, hop depth, via
// edges, exclusion reasons) round-trippable without flattening into
// normalized tables — the whole payload is what the introspection
// panel wants and it's small.

// SaveSelection persists a SelectionResult. Idempotent by event_id.
func (d *DB) SaveSelection(result *pb.SelectionResult, targetMessageID, threadID string) error {
	if result == nil {
		return nil
	}
	blob, err := proto.Marshal(result)
	if err != nil {
		return err
	}
	_, err = d.Exec(
		`INSERT OR REPLACE INTO selections (event_id, target_message_id, thread_id, scope, result) VALUES (?, ?, ?, ?, ?)`,
		result.EventId, targetMessageID, threadID, int(result.Scope), blob,
	)
	return err
}

// GetSelection fetches a persisted SelectionResult by event_id.
// Returns (nil, nil) if not found — selections are optional; Retrieval
// Events without a Query (autonomous continuation) skip Selection
// entirely and have no row.
func (d *DB) GetSelection(eventID string) (*pb.SelectionResult, error) {
	var blob []byte
	err := d.QueryRow(
		`SELECT result FROM selections WHERE event_id = ?`,
		eventID,
	).Scan(&blob)
	if err != nil {
		return nil, nil
	}
	result := &pb.SelectionResult{}
	if err := proto.Unmarshal(blob, result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetSelectionForMessage returns the SelectionResult that drove the
// turn which produced the given target message. The engine's event_id
// format is sel-<target_message_id>, so we can derive the lookup key
// without an extra column scan, but the target_message_id column is
// still the supported query path for when the format changes.
func (d *DB) GetSelectionForMessage(messageID string) (*pb.SelectionResult, error) {
	var blob []byte
	err := d.QueryRow(
		`SELECT result FROM selections WHERE target_message_id = ? ORDER BY created_at DESC LIMIT 1`,
		messageID,
	).Scan(&blob)
	if err != nil {
		return nil, nil
	}
	result := &pb.SelectionResult{}
	if err := proto.Unmarshal(blob, result); err != nil {
		return nil, err
	}
	return result, nil
}

// LatestSelectionForThread returns the most recent SelectionResult for
// a thread — backs the old "latest" convenience that the introspection
// panel's live view consumes.
func (d *DB) LatestSelectionForThread(threadID string) (*pb.SelectionResult, error) {
	var blob []byte
	err := d.QueryRow(
		`SELECT result FROM selections WHERE thread_id = ? ORDER BY created_at DESC LIMIT 1`,
		threadID,
	).Scan(&blob)
	if err != nil {
		return nil, nil
	}
	result := &pb.SelectionResult{}
	if err := proto.Unmarshal(blob, result); err != nil {
		return nil, err
	}
	return result, nil
}
