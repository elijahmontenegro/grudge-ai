package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps an SQLite connection with WAL mode.
type DB struct {
	*sql.DB
}

// Open creates or opens the SQLite database at the given data directory.
func Open(dataDir string) (*DB, error) {
	dbPath := filepath.Join(dataDir, "spidey.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	d := &DB{db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return d, nil
}

func (d *DB) migrate() error {
	_, err := d.Exec(schema)
	return err
}

const schema = `
-- Proto-mapped tables (derive from proto field definitions)

CREATE TABLE IF NOT EXISTS threads (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    sandboxed INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    parent_thread_id TEXT REFERENCES threads(id),
    branch_point_position INTEGER,
    archived_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS thread_working_dirs (
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    dir TEXT NOT NULL,
    PRIMARY KEY (thread_id, dir)
);

CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    role INTEGER NOT NULL,
    content BLOB NOT NULL,
    position INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_thread_pos ON messages(thread_id, position);

CREATE TABLE IF NOT EXISTS edges (
    from_message_id TEXT NOT NULL,
    to_message_id TEXT NOT NULL,
    score REAL NOT NULL,
    source INTEGER NOT NULL,
    cross_encoder_score REAL NOT NULL,
    qud_weight REAL NOT NULL,
    temporal_proximity REAL NOT NULL,
    detected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    from_thread_id TEXT NOT NULL,
    to_thread_id TEXT NOT NULL,
    PRIMARY KEY (from_message_id, to_message_id)
);
CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_message_id);

CREATE TABLE IF NOT EXISTS scores (
    from_message_id TEXT NOT NULL,
    to_message_id TEXT NOT NULL,
    score REAL NOT NULL,
    PRIMARY KEY (from_message_id, to_message_id)
);

CREATE TABLE IF NOT EXISTS quds (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    question TEXT NOT NULL,
    established_by TEXT NOT NULL,
    parent_qud_id TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    addressed_by TEXT NOT NULL DEFAULT '[]',
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_quds_thread ON quds(thread_id);

-- Service-specific tables (authored, not generated)

CREATE TABLE IF NOT EXISTS plans (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE,
    dir_path TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_plans_thread ON plans(thread_id);

CREATE TABLE IF NOT EXISTS view_state (
    thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
    state BLOB NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agent_state (
    thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
    status INTEGER NOT NULL DEFAULT 0,
    mode INTEGER NOT NULL DEFAULT 0,
    round_count INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMP,
    duration_limit TEXT,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS user_input_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    input TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_input_history_thread ON user_input_history(thread_id);

CREATE TABLE IF NOT EXISTS embeddings (
    message_id TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    vector BLOB NOT NULL,
    model TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`
