package store

import (
	"path/filepath"
	"testing"
)

func TestOpenConfiguresSQLite(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "nested", "mads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Error("foreign_keys pragma is off")
	}
}
