package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"

	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
)

type DB struct {
	*sql.DB
	onEmbed atomic.Pointer[func(messageID string, chunkIndex int, modelID, threadID string, vec []float32)]
}

// SetEmbeddingObserver registers a callback invoked after every successful
// InsertChunkEmbedding, with the (message, chunk, model, thread, vector) just
// written. modelID is passed so the observer can ignore embeddings for a model
// other than the one its index holds — the ANN index is per-model. The substrate
// wires this so every embedding-insert path (lazy EnsureVector, the post-insert
// embed queue, the backfill) keeps both the index and its RAM-resident thread
// metadata current without those writers importing it. Set once at boot, before
// concurrent inserts begin.
func (d *DB) SetEmbeddingObserver(f func(messageID string, chunkIndex int, modelID, threadID string, vec []float32)) {
	d.onEmbed.Store(&f)
}

// Open creates the canonical pre-release database or opens a database
// created from the same schema. Older development schemas are rejected;
// callers delete grudge.db and restart instead of migrating.
func Open(dataDir string) (*DB, error) {
	dbPath := filepath.Join(dataDir, "grudge.db")
	dsn := "file:" + dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(30000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=temp_store(MEMORY)" +
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

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		db.Close()
		return nil, fmt.Errorf("read journal_mode: %w", err)
	}
	if mode != "wal" {
		db.Close()
		return nil, fmt.Errorf("journal_mode is %q, expected wal", mode)
	}

	out := &DB{DB: db}
	if err := out.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return out, nil
}

func (d *DB) initialize() error {
	var tableCount int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		if _, err := d.Exec(schema); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
		return nil
	}

	var version int
	var identity string
	if err := d.QueryRow(`SELECT version, identity FROM schema_identity WHERE id = 1`).Scan(&version, &identity); err != nil {
		return fmt.Errorf("unsupported development database; delete grudge.db and restart")
	}
	// Forward migrations: additive, versioned, one-way steps that bring a
	// stored shape to the next identity in the chain. An identity outside
	// the chain still fails the gate loudly — the gate guards philosophy
	// drift; a compatible column addition is lawful versioning, not drift.
	if version == 4 && identity == schemaIdentityV4 {
		if err := d.migrateV4ToV5(); err != nil {
			return fmt.Errorf("migrate schema v4 -> v5: %w", err)
		}
		version, identity = schemaVersion, schemaIdentity
	}
	if version != schemaVersion || identity != schemaIdentity {
		return fmt.Errorf("unsupported database schema %d (%s); delete grudge.db and restart", version, identity)
	}
	return nil
}

// migrateV4ToV5 adds the edges.scorer_model observation-attribute column
// (instrument identity: which scorer's units the edge's raw similarity
// and contribution weights are in — the retroactively-unrecoverable
// stamp). Pre-existing edges keep '' — honest: their instrument was
// never recorded, which is exactly the gap the stamp closes going
// forward. Idempotent via the column check so a step interrupted between
// ALTER and the identity update re-runs cleanly.
func (d *DB) migrateV4ToV5() error {
	rows, err := d.Query(`PRAGMA table_info(edges)`)
	if err != nil {
		return err
	}
	hasCol := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "scorer_model" {
			hasCol = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasCol {
		if _, err := d.Exec(`ALTER TABLE edges ADD COLUMN scorer_model TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = d.Exec(`UPDATE schema_identity SET version = ?, identity = ? WHERE id = 1`, schemaVersion, schemaIdentity)
	return err
}
