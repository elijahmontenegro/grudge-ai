package storage

import (
	"database/sql"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CreateThread inserts a new thread.
func (d *DB) CreateThread(t *pb.Thread) error {
	_, err := d.Exec(
		`INSERT INTO threads (id, name, sandboxed, created_at, parent_thread_id, branch_point_position)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.Id, t.Name, boolToInt(t.Sandboxed), t.CreatedAt.AsTime(),
		nilStr(t.ParentThreadId), nilInt64(t.BranchPointPosition),
	)
	if err != nil {
		return err
	}

	for _, dir := range t.WorkingDirs {
		if _, err := d.Exec(
			`INSERT INTO thread_working_dirs (thread_id, dir) VALUES (?, ?)`,
			t.Id, dir,
		); err != nil {
			return err
		}
	}
	return nil
}

// GetThread retrieves a thread by ID.
func (d *DB) GetThread(id string) (*pb.Thread, error) {
	t := &pb.Thread{}
	var createdAt time.Time
	var parentID sql.NullString
	var branchPos sql.NullInt64
	var archivedAt sql.NullTime

	err := d.QueryRow(
		`SELECT id, name, sandboxed, created_at, parent_thread_id, branch_point_position, archived_at
		 FROM threads WHERE id = ?`, id,
	).Scan(&t.Id, &t.Name, &t.Sandboxed, &createdAt, &parentID, &branchPos, &archivedAt)
	if err != nil {
		return nil, err
	}

	t.CreatedAt = timestamppb.New(createdAt)
	if parentID.Valid {
		t.ParentThreadId = &parentID.String
	}
	if branchPos.Valid {
		t.BranchPointPosition = &branchPos.Int64
	}
	if archivedAt.Valid {
		ts := timestamppb.New(archivedAt.Time)
		t.ArchivedAt = ts
	}

	rows, err := d.Query(`SELECT dir FROM thread_working_dirs WHERE thread_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dir string
		if err := rows.Scan(&dir); err != nil {
			return nil, err
		}
		t.WorkingDirs = append(t.WorkingDirs, dir)
	}

	return t, rows.Err()
}

// ListThreads returns all threads, optionally including archived.
func (d *DB) ListThreads(includeArchived bool) ([]*pb.Thread, error) {
	query := `SELECT id FROM threads`
	if !includeArchived {
		query += ` WHERE archived_at IS NULL`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []*pb.Thread
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		t, err := d.GetThread(id)
		if err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// ArchiveThread sets the archived_at timestamp.
func (d *DB) ArchiveThread(id string) error {
	_, err := d.Exec(`UPDATE threads SET archived_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// UnarchiveThread clears the archived_at timestamp.
func (d *DB) UnarchiveThread(id string) error {
	_, err := d.Exec(`UPDATE threads SET archived_at = NULL WHERE id = ?`, id)
	return err
}

// DeleteThread hard deletes a thread and all related data (CASCADE).
func (d *DB) DeleteThread(id string) error {
	_, err := d.Exec(`DELETE FROM threads WHERE id = ?`, id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nilStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nilInt64(i *int64) any {
	if i == nil {
		return nil
	}
	return *i
}
