package storage

// Plan represents a persisted plan artifact.
type Plan struct {
	ID       string
	ThreadID string
	Slug     string
	DirPath  string
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
