package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Plan represents a persisted plan artifact.
type Plan struct {
	ID       string
	ThreadID string
	Slug     string
	DirPath  string
}

// PlanDirForThread returns the absolute plan directory for a
// thread. Layout mirrors sandbox.WorkspaceDir:
//
//	{dataDir}/plans/plan-{threadID}/
//	{dataDir}/sandboxes/sbx-{threadID}/
//
// Rejects any threadID that isn't the server-generated
// `thread-{unixnano}` format. Retains the HasPrefix
// belt-and-suspenders check in case the format changes in the
// future and someone forgets to re-tighten here.
func PlanDirForThread(dataDir, threadID string) (string, error) {
	if err := ValidateThreadID(threadID); err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Join(dataDir, "plans"))
	if err != nil {
		return "", fmt.Errorf("resolve plans root: %w", err)
	}
	dir := filepath.Clean(filepath.Join(root, "plan-"+threadID))
	if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid threadID: path escapes plans directory")
	}
	return dir, nil
}

// InsertPlan persists a plan record.
func (d *DB) InsertPlan(p *Plan) error {
	_, err := d.Exec(
		`INSERT INTO plans (id, thread_id, slug, dir_path) VALUES (?, ?, ?, ?)`,
		p.ID, p.ThreadID, p.Slug, p.DirPath,
	)
	return err
}

// GetPlanByThread retrieves the plan for a thread.
func (d *DB) GetPlanByThread(threadID string) (*Plan, error) {
	p := &Plan{}
	err := d.QueryRow(
		`SELECT id, thread_id, slug, dir_path FROM plans WHERE thread_id = ? ORDER BY updated_at DESC LIMIT 1`,
		threadID,
	).Scan(&p.ID, &p.ThreadID, &p.Slug, &p.DirPath)
	return p, err
}

// GetPlanBySlug retrieves a plan by its slug.
func (d *DB) GetPlanBySlug(slug string) (*Plan, error) {
	p := &Plan{}
	err := d.QueryRow(
		`SELECT id, thread_id, slug, dir_path FROM plans WHERE slug = ?`, slug,
	).Scan(&p.ID, &p.ThreadID, &p.Slug, &p.DirPath)
	return p, err
}
