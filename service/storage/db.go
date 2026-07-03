package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
)

type DB struct {
	*sql.DB
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
	if version != schemaVersion || identity != schemaIdentity {
		return fmt.Errorf("unsupported database schema %d (%s); delete grudge.db and restart", version, identity)
	}
	return nil
}
