package storage

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
