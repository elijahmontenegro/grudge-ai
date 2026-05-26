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
