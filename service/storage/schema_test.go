package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsNonCanonicalDevelopmentDatabase(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "grudge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE legacy_table(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	_, err = Open(dir)
	if err == nil || !strings.Contains(err.Error(), "delete") {
		t.Fatalf("expected delete-and-recreate error, got %v", err)
	}
}
