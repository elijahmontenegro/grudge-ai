package storage

import "fmt"

const schemaVersion = 3
const schemaIdentity = "rrc-calibrated-acceptance-lean-tables-v3"

// defaultEmbeddingDim is the bootstrap vector width. It is only a
// bootstrap default: the runtime probe (EnsureEmbeddingDim) reconciles it
// to the configured embedder's native dimension before any vector is
// written, rebuilding chunk_vectors via chunkVectorsDDL if they differ. So
// a substrate on a 3072-dim embedder does not need this constant changed —
// the table is rebuilt at boot.
const defaultEmbeddingDim = 1024

// chunkVectorsDDL renders the vec0 virtual table, its rowid companion, and
// the cascade trigger for a given embedding dimension. It is the single
// source of truth for the vector-cache shape, used both at bootstrap
// (defaultEmbeddingDim, composed into `schema`) and at runtime rebuild
// (EnsureEmbeddingDim, when the probed dimension differs). Keeping one
// generator means the rebuild path and the bootstrap path can never emit
// structurally different tables.
func chunkVectorsDDL(dim int) string {
	return fmt.Sprintf(`CREATE VIRTUAL TABLE chunk_vectors USING vec0(
    chunk_rowid INTEGER PRIMARY KEY,
    embedding FLOAT[%d] distance_metric=cosine,
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
END;`, dim)
}

// schema is the full bootstrap DDL, composed once. The identity row is
// injected from the schemaVersion/schemaIdentity constants (so the stored
// identity and the gate constant cannot drift), and the vector cache is
// injected from chunkVectorsDDL at the bootstrap dimension. initialize()
// execs this on an empty DB. The three %-verbs are the only formatting in
// the string; the DDL itself contains no % literals.
var schema = fmt.Sprintf(`
CREATE TABLE schema_identity (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL,
    identity TEXT NOT NULL
);
INSERT INTO schema_identity(id, version, identity)
VALUES (1, %d, '%s');

CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    sandboxed INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    parent_thread_id TEXT REFERENCES threads(id),
    branch_point_position INTEGER,
    archived_at TIMESTAMP
);
CREATE TABLE thread_working_dirs (
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    dir TEXT NOT NULL,
    PRIMARY KEY (thread_id, dir)
);
CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    role INTEGER NOT NULL,
    content BLOB NOT NULL,
    position INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    turn_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_messages_thread_pos ON messages(thread_id, position);
CREATE INDEX idx_messages_turn ON messages(thread_id, turn_id);

CREATE TABLE edges (
    from_message_id TEXT NOT NULL,
    to_message_id TEXT NOT NULL,
    score REAL NOT NULL,
    source INTEGER NOT NULL,
    cross_encoder_score REAL NOT NULL,
    detected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    from_thread_id TEXT NOT NULL,
    to_thread_id TEXT NOT NULL,
    PRIMARY KEY (from_message_id, to_message_id, source)
);
CREATE INDEX idx_edges_to ON edges(to_message_id);
CREATE INDEX idx_edges_from_source ON edges(from_message_id, source);

CREATE TABLE chunks (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    text TEXT NOT NULL,
    byte_start INTEGER NOT NULL,
    byte_end INTEGER NOT NULL,
    token_est INTEGER NOT NULL,
    PRIMARY KEY (message_id, chunk_index)
);
CREATE INDEX idx_chunks_message ON chunks(message_id);

CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE,
    dir_path TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_plans_thread ON plans(thread_id);
CREATE TABLE view_state (
    thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
    state BLOB NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE agent_state (
    thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
    status INTEGER NOT NULL DEFAULT 0,
    mode INTEGER NOT NULL DEFAULT 0,
    round_count INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMP,
    duration_limit TEXT,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE user_input_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    input TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_input_history_thread ON user_input_history(thread_id);

-- embedding_meta records the current embedding dimension + model, the one
-- source of truth EnsureEmbeddingDim reads to decide whether chunk_vectors
-- must be rebuilt. The row is written at runtime once an embedder is probed
-- (dim is unknown until then); the table is created empty here.
CREATE TABLE embedding_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    dim INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

%s

CREATE TABLE scores (
    local_context_fingerprint TEXT NOT NULL,
    local_context_chunk_index INTEGER NOT NULL,
    candidate_message_id TEXT NOT NULL,
    candidate_chunk_index INTEGER NOT NULL,
    model_id TEXT NOT NULL,
    score REAL NOT NULL,
    PRIMARY KEY (local_context_fingerprint, local_context_chunk_index, candidate_message_id, candidate_chunk_index, model_id),
    FOREIGN KEY (candidate_message_id, candidate_chunk_index)
        REFERENCES chunks(message_id, chunk_index) ON DELETE CASCADE
);
CREATE INDEX idx_scores_local_context ON scores(local_context_fingerprint, model_id);

-- selections stores the marshaled pb.SelectionResult per Retrieval
-- Event. Everything queryable lives inside the result BLOB; the only
-- lookup keys are event_id and (anchor_message_id, created_at).
CREATE TABLE selections (
    event_id TEXT PRIMARY KEY,
    anchor_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    result BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_selections_anchor ON selections(anchor_message_id);

CREATE TABLE tick_traces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id TEXT NOT NULL,
    round INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    t_rrc_prerequisite_selection_ms INTEGER NOT NULL,
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
CREATE INDEX idx_tick_traces_thread_id ON tick_traces(thread_id, created_at DESC);
`, schemaVersion, schemaIdentity, chunkVectorsDDL(defaultEmbeddingDim))
