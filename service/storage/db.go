package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/emontenegr/spidey/rrc"
	_ "modernc.org/sqlite"
)

// DB wraps an SQLite connection with WAL mode. ChunkConfig drives the
// rrc.ChunkText call inside InsertMessage; SetChunkConfig overrides
// the default if the service wants different chunking semantics.
type DB struct {
	*sql.DB
	chunkCfg rrc.ChunkConfig
}

// SetChunkConfig installs a custom chunking config. Optional — the
// default is rrc.DefaultChunkConfig().
func (d *DB) SetChunkConfig(cfg rrc.ChunkConfig) { d.chunkCfg = cfg }

// Open creates or opens the SQLite database at the given data directory.
//
// Concurrency notes:
//   - modernc/sqlite ignores the `?_journal_mode=WAL` DSN form other
//     drivers accept; it wants `?_pragma=journal_mode(WAL)` instead.
//     Previously we used the other-driver form and it was silently
//     dropped — result: journal_mode stayed at "delete" and
//     busy_timeout stayed at 0, so any concurrent write failed
//     instantly with SQLITE_BUSY. Fixed by passing pragmas in
//     modernc's format so every pool connection gets them on open.
//   - No MaxOpenConns cap. An earlier iteration set it to 1 to serialize
//     writes, but that blocked long-running reads (subscriptions, large
//     corpus fetches) against every incoming query — the UI hung.
//     WAL mode + busy_timeout handle the write contention; the default
//     pool size is fine.
func Open(dataDir string) (*DB, error) {
	dbPath := filepath.Join(dataDir, "spidey.db")
	// modernc/sqlite DSN: query-string pragmas are applied on every
	// connection the pool opens, not just the first. `_txlock=immediate`
	// ensures every BEGIN grabs the write lock up front, avoiding the
	// "two readers upgrading to writers" SQLITE_BUSY deadlock.
	dsn := dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" + // WAL-safe default — fsync at checkpoint, not per-commit
		"&_pragma=busy_timeout(30000)" + // ms — wait up to 30s for a lock rather than failing instantly
		"&_pragma=foreign_keys(ON)" + // CASCADE delete
		"&_pragma=temp_store(MEMORY)" + // keep temp tables off disk
		"&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	// Verify journal_mode actually flipped — WAL activation can fail if
	// another process has the DB open in rollback mode, and a silent
	// revert would resurrect the BUSY storm. Fail fast instead.
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		db.Close()
		return nil, fmt.Errorf("read journal_mode: %w", err)
	}
	if mode != "wal" {
		db.Close()
		return nil, fmt.Errorf("journal_mode is %q, expected wal — another process may hold the DB", mode)
	}

	d := &DB{DB: db, chunkCfg: rrc.DefaultChunkConfig()}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return d, nil
}

func (d *DB) migrate() error {
	if _, err := d.Exec(schema); err != nil {
		return err
	}
	// Schema version guard — v2 introduces chunk-keyed embeddings and
	// per-chunk-pair scores. Running against an older DB drops the
	// obsolete derived tables (embeddings, scores, edges) so they get
	// recreated with the new keys. Messages and threads are preserved.
	var ver int
	if err := d.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&ver); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	if ver < 2 {
		if _, err := d.Exec(migrationV2); err != nil {
			return fmt.Errorf("migrate v2: %w", err)
		}
		if _, err := d.Exec("INSERT INTO schema_version(version) VALUES (2)"); err != nil {
			return fmt.Errorf("record schema_version 2: %w", err)
		}
	}
	return nil
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

-- Schema version sentinel. Each row is a migration that has run.
-- Introduced at v2 to gate the embeddings/scores → chunk-keyed rewrite.
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Chunks: paragraph-sized slices of a message, persisted so embeddings
-- and cross-encoder scores can be keyed at sub-message granularity.
-- The old message-level scoring silently truncated any message past
-- the classifier's 512-token limit — the character bible (~6K tokens)
-- had 11/12 of its content invisible to RRC. Chunking restores
-- visibility; messages remain the graph unit.
CREATE TABLE IF NOT EXISTS chunks (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    text TEXT NOT NULL,
    byte_start INTEGER NOT NULL,
    byte_end INTEGER NOT NULL,
    token_est INTEGER NOT NULL,
    PRIMARY KEY (message_id, chunk_index)
);
CREATE INDEX IF NOT EXISTS idx_chunks_message ON chunks(message_id);

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

-- Embedding cache: one vector per (chunk, embedder model) triple.
-- Key includes chunk_index so the same message's multiple chunks each
-- get their own vector. Model ID in the PK lets multiple model
-- versions coexist for the same chunk (user swaps embedder → old rows
-- stay, new rows accrue). FK on (message_id, chunk_index) cascades
-- from chunks which itself cascades from messages.
CREATE TABLE IF NOT EXISTS embeddings (
    message_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    vector BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (message_id, chunk_index, model_id),
    FOREIGN KEY (message_id, chunk_index) REFERENCES chunks(message_id, chunk_index) ON DELETE CASCADE
);

-- Score cache: one row per (prior-chunk, target-chunk, reranker model)
-- quintuple. Keys at chunk granularity so the cache survives across
-- different chunk combinations and model switches invalidate cleanly.
-- Aggregation to message-pair score happens at read time in the engine.
CREATE TABLE IF NOT EXISTS scores (
    from_message_id TEXT NOT NULL,
    from_chunk_index INTEGER NOT NULL,
    to_message_id TEXT NOT NULL,
    to_chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    score REAL NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (from_message_id, from_chunk_index, to_message_id, to_chunk_index, model_id),
    FOREIGN KEY (from_message_id, from_chunk_index) REFERENCES chunks(message_id, chunk_index) ON DELETE CASCADE,
    FOREIGN KEY (to_message_id, to_chunk_index) REFERENCES chunks(message_id, chunk_index) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_scores_target ON scores(to_message_id, to_chunk_index, model_id);

-- Selection audit trail. One row per SelectionResult produced during
-- a Retrieval Event. Persisted so every historical turn can be
-- introspected — without this, restart wipes the in-memory map and
-- "what did RRC pull for turn N?" becomes unanswerable. event_id
-- matches the engine's synthesized ID (sel-<target_message_id>).
CREATE TABLE IF NOT EXISTS selections (
    event_id TEXT PRIMARY KEY,
    target_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL,
    scope INTEGER NOT NULL,
    result BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_selections_target ON selections(target_message_id);
CREATE INDEX IF NOT EXISTS idx_selections_thread_created ON selections(thread_id, created_at);
`

// migrationV2 converts pre-v2 deployments to chunk-keyed embeddings
// and per-chunk-pair scores. Pre-v2 embeddings/scores/edges were
// keyed at message level under the old 512-token NLI classifier; the
// new schema (above) supersedes them. We drop the derived tables and
// let the runtime rebuild them from scratch — messages and threads
// stay untouched, so the user's conversation history is preserved.
const migrationV2 = `
DROP TABLE IF EXISTS embeddings;
DROP TABLE IF EXISTS scores;
DROP TABLE IF EXISTS edges;

CREATE TABLE embeddings (
    message_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    vector BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (message_id, chunk_index, model_id),
    FOREIGN KEY (message_id, chunk_index) REFERENCES chunks(message_id, chunk_index) ON DELETE CASCADE
);

CREATE TABLE scores (
    from_message_id TEXT NOT NULL,
    from_chunk_index INTEGER NOT NULL,
    to_message_id TEXT NOT NULL,
    to_chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    score REAL NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (from_message_id, from_chunk_index, to_message_id, to_chunk_index, model_id),
    FOREIGN KEY (from_message_id, from_chunk_index) REFERENCES chunks(message_id, chunk_index) ON DELETE CASCADE,
    FOREIGN KEY (to_message_id, to_chunk_index) REFERENCES chunks(message_id, chunk_index) ON DELETE CASCADE
);
CREATE INDEX idx_scores_target ON scores(to_message_id, to_chunk_index, model_id);

CREATE TABLE edges (
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
CREATE INDEX idx_edges_to ON edges(to_message_id);
`
