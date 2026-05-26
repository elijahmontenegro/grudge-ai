package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"

	// Order matters here: the sqlite-vec bindings install a WASM
	// binary that includes the vec0 extension into sqlite3.Binary.
	// Don't import ncruces/go-sqlite3/embed alongside — it would
	// overwrite Binary with a vec0-less build, breaking vec0.
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// DB wraps an SQLite connection with WAL mode. Storage performs no
// algorithm work — chunking is the caller's responsibility; storage
// receives a pre-chunked slice and persists it alongside each message.
type DB struct {
	*sql.DB
}

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
	// ncruces/go-sqlite3 driver via WASM (pure Go, no cgo). The
	// blank-imported sqlite-vec-go-bindings/ncruces auto-registers
	// the vec0 virtual-table module so chunk_vectors works on every
	// connection. DSN pragmas apply per connection. `_txlock=immediate`
	// ensures every BEGIN grabs the write lock up front, avoiding the
	// "two readers upgrading to writers" SQLITE_BUSY deadlock.
	dsn := "file:" + dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(30000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=temp_store(MEMORY)" +
		// trusted_schema=ON allows triggers to write to virtual tables
		// (vec0 chunk_vectors). Default OFF is a SQLite security
		// feature against tainted attached databases; Spidey's
		// single-application-owned DB is a trusted-schema use case.
		"&_pragma=trusted_schema(ON)" +
		"&_txlock=immediate"

	db, err := sql.Open("sqlite3", dsn)
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

	d := &DB{DB: db}
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
	if ver < 3 {
		if _, err := d.Exec(migrationV3); err != nil {
			return fmt.Errorf("migrate v3: %w", err)
		}
		if _, err := d.Exec("INSERT INTO schema_version(version) VALUES (3)"); err != nil {
			return fmt.Errorf("record schema_version 3: %w", err)
		}
	}
	if ver < 4 {
		if _, err := d.Exec(migrationV4); err != nil {
			return fmt.Errorf("migrate v4: %w", err)
		}
		if _, err := d.Exec("INSERT INTO schema_version(version) VALUES (4)"); err != nil {
			return fmt.Errorf("record schema_version 4: %w", err)
		}
	}
	if ver < 5 {
		if _, err := d.Exec(migrationV5); err != nil {
			return fmt.Errorf("migrate v5: %w", err)
		}
		if _, err := d.Exec("INSERT INTO schema_version(version) VALUES (5)"); err != nil {
			return fmt.Errorf("record schema_version 5: %w", err)
		}
	}
	if ver < 6 {
		if _, err := d.Exec(migrationV6); err != nil {
			return fmt.Errorf("migrate v6: %w", err)
		}
		if _, err := d.Exec("INSERT INTO schema_version(version) VALUES (6)"); err != nil {
			return fmt.Errorf("record schema_version 6: %w", err)
		}
	}
	if ver < 7 {
		if _, err := d.Exec(migrationV7); err != nil {
			return fmt.Errorf("migrate v7: %w", err)
		}
		if _, err := d.Exec("INSERT INTO schema_version(version) VALUES (7)"); err != nil {
			return fmt.Errorf("record schema_version 7: %w", err)
		}
	}
	return nil
}

// migrationV7 introduces tick_traces, an append-only per-tick latency
// log. Each row decomposes one SendMessage call's wall clock across
// the five hot-path stages (RRC OnMessage, Select, Assemble, Complete,
// Stream/Persist) so a slow agent localizes to the right stage on
// inspection. Independent of selection / score / edge tables — pure
// telemetry, no FK cascade. Orphan rows after a thread delete are
// fine and useful for cross-run aggregates.
const migrationV7 = `
CREATE TABLE IF NOT EXISTS tick_traces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id TEXT NOT NULL,
    round INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    t_rrc_onmessage_ms INTEGER NOT NULL,
    t_select_ms INTEGER NOT NULL,
    t_assemble_ms INTEGER NOT NULL,
    t_complete_ms INTEGER NOT NULL,
    t_stream_ms INTEGER NOT NULL,
    t_persist_ms INTEGER NOT NULL,
    t_total_ms INTEGER NOT NULL,
    completer_model TEXT,
    corpus_size INTEGER NOT NULL,
    selected_count INTEGER NOT NULL,
    assembled_tokens_est INTEGER NOT NULL,
    errored INTEGER NOT NULL DEFAULT 0,
    error_msg TEXT
);
CREATE INDEX IF NOT EXISTS idx_tick_traces_thread_id ON tick_traces(thread_id, created_at DESC);
`

// migrationV6 promotes `model_id` from a vec0 auxiliary column to a
// partition key. Auxiliary columns (declared `+col`) are
// content-only — vec0 forbids them in KNN WHERE clauses, so the
// natural query `WHERE embedding MATCH ? AND k = ? AND model_id = ?`
// was failing with "illegal WHERE constraint on auxiliary column".
//
// V5's workaround was wrong on principle: fetch all neighbors,
// filter model_id in Go. That defeats vec0's partitioned index and
// gives unbounded false-neighbor rates when multiple models'
// vectors coexist (model swaps, A/B tests). The partition-key
// declaration is what vec0 expects for "this column scopes the
// index" — KNN runs only within the partition the WHERE selects,
// sub-linear and exact.
//
// Per directive: no vector migration. chunk_vectors and the rowid
// mapping table are dropped and recreated; the corpus re-embeds as
// turns happen. Existing chunks/messages are unaffected.
const migrationV6 = `
DROP TABLE IF EXISTS chunk_vectors;
DROP TABLE IF EXISTS chunk_vector_rowids;
DROP TRIGGER IF EXISTS trg_cv_cascade_chunks;

CREATE VIRTUAL TABLE chunk_vectors USING vec0(
    chunk_rowid INTEGER PRIMARY KEY,
    embedding FLOAT[1024] distance_metric=cosine,
    model_id TEXT partition key,
    +message_id TEXT,
    +chunk_index INTEGER,
    +thread_id TEXT,
    +role INTEGER
);

CREATE TABLE chunk_vector_rowids (
    chunk_rowid INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    UNIQUE (message_id, chunk_index, model_id)
);

CREATE TRIGGER trg_cv_cascade_chunks
AFTER DELETE ON chunks
FOR EACH ROW
BEGIN
    DELETE FROM chunk_vectors
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
    DELETE FROM chunk_vector_rowids
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
END;
`

// migrationV3 swaps the legacy BLOB-vector embeddings table for the
// sqlite-vec vec0 virtual table that backs sub-linear ANN. Per
// directive, no migration of vector data — the corpus re-embeds as
// turns happen. Dim was 2560 here (zembed-1 native); migrationV4
// rebuilds at 1024 (Qwen3-Embedding-0.6B native).
const migrationV3 = `
DROP TABLE IF EXISTS embeddings;

CREATE VIRTUAL TABLE IF NOT EXISTS chunk_vectors USING vec0(
    chunk_rowid INTEGER PRIMARY KEY,
    embedding FLOAT[2560],
    +message_id TEXT,
    +chunk_index INTEGER,
    +thread_id TEXT,
    +model_id TEXT,
    +role INTEGER
);

CREATE TABLE IF NOT EXISTS chunk_vector_rowids (
    chunk_rowid INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    UNIQUE (message_id, chunk_index, model_id)
);

CREATE TRIGGER IF NOT EXISTS trg_cv_cascade_chunks
AFTER DELETE ON chunks
FOR EACH ROW
BEGIN
    DELETE FROM chunk_vectors
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
    DELETE FROM chunk_vector_rowids
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
END;
`

// migrationV5 rebuilds chunk_vectors with an explicit
// distance_metric=cosine declaration. Without the clause, sqlite-vec
// defaults to L2 — under L2 with unit-normalized embeddings the
// *ordering* matches cosine but the returned distance value is L2²,
// so `similarity = 1 - distance` is mathematically wrong. The engine's
// Layer-1-score path (post-Phase-3) consumes `1 - distance` as a
// similarity in [0, 1], so the conversion must be honest. Declaring
// the metric makes vec0 return cosine distance directly.
//
// Per directive: no vector migration. chunk_vectors and the rowid
// mapping table are dropped and recreated; the corpus re-embeds as
// turns happen. Existing chunks/messages are unaffected.
const migrationV5 = `
DROP TABLE IF EXISTS chunk_vectors;
DROP TABLE IF EXISTS chunk_vector_rowids;
DROP TRIGGER IF EXISTS trg_cv_cascade_chunks;

CREATE VIRTUAL TABLE chunk_vectors USING vec0(
    chunk_rowid INTEGER PRIMARY KEY,
    embedding FLOAT[1024] distance_metric=cosine,
    +message_id TEXT,
    +chunk_index INTEGER,
    +thread_id TEXT,
    +model_id TEXT,
    +role INTEGER
);

CREATE TABLE chunk_vector_rowids (
    chunk_rowid INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    UNIQUE (message_id, chunk_index, model_id)
);

CREATE TRIGGER trg_cv_cascade_chunks
AFTER DELETE ON chunks
FOR EACH ROW
BEGIN
    DELETE FROM chunk_vectors
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
    DELETE FROM chunk_vector_rowids
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
END;
`

// migrationV4 rebuilds chunk_vectors at dim 1024 (Qwen3-Embedding-0.6B
// native) replacing v3's dim 2560 (zembed-1 native). The embedder
// changed during the substrate selection arc — Qwen3-Embedding-0.6B
// + zerank-1-small is the chosen Apache 2.0 pair that fits 12GB GPU
// co-resident at bf16 with margin, after the eval-winning NC pair
// (zembed-1 + zerank-2) was disqualified by the hardware constraint.
//
// Per directive: no vector migration. chunk_vectors and the rowid
// mapping table are dropped and recreated; the corpus re-embeds as
// turns happen. Existing chunks/messages are unaffected.
const migrationV4 = `
DROP TABLE IF EXISTS chunk_vectors;
DROP TABLE IF EXISTS chunk_vector_rowids;
DROP TRIGGER IF EXISTS trg_cv_cascade_chunks;

CREATE VIRTUAL TABLE chunk_vectors USING vec0(
    chunk_rowid INTEGER PRIMARY KEY,
    embedding FLOAT[1024],
    +message_id TEXT,
    +chunk_index INTEGER,
    +thread_id TEXT,
    +model_id TEXT,
    +role INTEGER
);

CREATE TABLE chunk_vector_rowids (
    chunk_rowid INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    UNIQUE (message_id, chunk_index, model_id)
);

CREATE TRIGGER trg_cv_cascade_chunks
AFTER DELETE ON chunks
FOR EACH ROW
BEGIN
    DELETE FROM chunk_vectors
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
    DELETE FROM chunk_vector_rowids
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
END;
`

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

-- chunk_vectors: sqlite-vec vec0 virtual table providing the ANN
-- index for sub-linear KNN. Auxiliary columns (prefixed with +) are
-- stored alongside vectors and pushed into the KNN search as filter
-- predicates — chunk_vectors WHERE embedding MATCH ? AND k = ?
-- AND model_id = ? AND thread_id = ? does pre-traversal filtering,
-- not post-filtering (which collapses recall when the predicate is
-- selective). Dim 1024 matches Qwen3-Embedding-0.6B's native output;
-- if a different embedder is configured (different dim) the chunk_vectors
-- table must be recreated via a new migration.
--
-- vec0 is a virtual table; SQLite's FK cascade doesn't reach it.
-- The trigger below cascades chunk deletes through to the vec0 rows.
-- model_id is a vec0 partition key (not an aux column) so KNN
-- queries filter natively on it: an equality clause alongside the
-- MATCH+k constraint runs sub-linearly within the chosen model's
-- partition. Aux columns (+message_id, +chunk_index, +thread_id,
-- +role) are storage-only and cannot appear in KNN WHERE.
CREATE VIRTUAL TABLE IF NOT EXISTS chunk_vectors USING vec0(
    chunk_rowid INTEGER PRIMARY KEY,
    embedding FLOAT[1024] distance_metric=cosine,
    model_id TEXT partition key,
    +message_id TEXT,
    +chunk_index INTEGER,
    +thread_id TEXT,
    +role INTEGER
);

-- chunk_vector_rowids: monotonic AUTOINCREMENT id assignment for the
-- vec0 PRIMARY KEY. Hash-based rowids would have collision probability
-- ~10^-5 per row at 10M chunks; non-zero collisions mean silent vector
-- loss when two distinct (message_id, chunk_index, model_id) triples
-- map to the same rowid → second INSERT overwrites the first → recall
-- hole nothing detects. Mapping table eliminates the failure mode
-- structurally (UNIQUE constraint catches duplicates at insert).
CREATE TABLE IF NOT EXISTS chunk_vector_rowids (
    chunk_rowid INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    UNIQUE (message_id, chunk_index, model_id)
);

-- Cascade chunk deletes through to the vec0 ANN index AND the rowid
-- mapping. SQLite gates trigger writes to virtual tables behind
-- trusted_schema=ON (set per connection in Open). Without this
-- trigger, orphan vectors accumulate in chunk_vectors, show up in
-- top-K KNN results, and silently displace real candidates — a
-- recall hole nothing else catches.
CREATE TRIGGER IF NOT EXISTS trg_cv_cascade_chunks
AFTER DELETE ON chunks
FOR EACH ROW
BEGIN
    DELETE FROM chunk_vectors
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
    DELETE FROM chunk_vector_rowids
    WHERE message_id = OLD.message_id AND chunk_index = OLD.chunk_index;
END;

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

-- Per-tick latency trace: one row per SendMessage invocation,
-- decomposed across the five hot-path stages. Append-only, narrow,
-- indexed for "WHERE thread_id = ? ORDER BY created_at DESC". No FK
-- cascade — telemetry survives thread deletes for cross-run aggregates.
CREATE TABLE IF NOT EXISTS tick_traces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id TEXT NOT NULL,
    round INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    t_rrc_onmessage_ms INTEGER NOT NULL,
    t_select_ms INTEGER NOT NULL,
    t_assemble_ms INTEGER NOT NULL,
    t_complete_ms INTEGER NOT NULL,
    t_stream_ms INTEGER NOT NULL,
    t_persist_ms INTEGER NOT NULL,
    t_total_ms INTEGER NOT NULL,
    completer_model TEXT,
    corpus_size INTEGER NOT NULL,
    selected_count INTEGER NOT NULL,
    assembled_tokens_est INTEGER NOT NULL,
    errored INTEGER NOT NULL DEFAULT 0,
    error_msg TEXT
);
CREATE INDEX IF NOT EXISTS idx_tick_traces_thread_id ON tick_traces(thread_id, created_at DESC);
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
