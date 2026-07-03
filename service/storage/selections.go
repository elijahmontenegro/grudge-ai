package storage

import (
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
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
// Everything queryable (scope, fingerprint, Local Context ids, thread)
// lives inside the marshaled result; the row carries only the lookup
// keys.
func (d *DB) SaveSelection(result *rrcv1.SelectionResult, anchorMessageID string) error {
	if result == nil {
		return nil
	}
	blob, err := proto.Marshal(result)
	if err != nil {
		return err
	}
	_, err = d.Exec(
		`INSERT OR REPLACE INTO selections
		 (event_id, anchor_message_id, result)
		 VALUES (?, ?, ?)`,
		result.EventId, anchorMessageID, blob,
	)
	return err
}

// GetSelection fetches a persisted SelectionResult by event_id.
// Returns (nil, nil) if not found — selections are optional; Retrieval
// Autonomous continuations without a new event skip Selection
// entirely and have no row.
func (d *DB) GetSelection(eventID string) (*rrcv1.SelectionResult, error) {
	var blob []byte
	err := d.QueryRow(
		`SELECT result FROM selections WHERE event_id = ?`,
		eventID,
	).Scan(&blob)
	if err != nil {
		return nil, nil
	}
	result := &rrcv1.SelectionResult{}
	if err := proto.Unmarshal(blob, result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetSelectionForMessage returns the SelectionResult anchored to the
// given stored event.
func (d *DB) GetSelectionForMessage(messageID string) (*rrcv1.SelectionResult, error) {
	var blob []byte
	err := d.QueryRow(
		`SELECT result FROM selections WHERE anchor_message_id = ? ORDER BY created_at DESC LIMIT 1`,
		messageID,
	).Scan(&blob)
	if err != nil {
		return nil, nil
	}
	result := &rrcv1.SelectionResult{}
	if err := proto.Unmarshal(blob, result); err != nil {
		return nil, err
	}
	return result, nil
}
