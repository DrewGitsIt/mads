package main

import (
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/services/data/internal/store"
)

func TestCheckSchemaCompatible(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := checkSchemaCompatible(db, store.Migrations()); err != nil {
		t.Fatalf("freshly migrated schema should be compatible with its own binary: %v", err)
	}
}

func TestCheckSchemaCompatibleRefusesNewerSchema(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	// Simulate a newer binary having applied a migration this binary's
	// embedded set doesn't know about.
	if _, err := db.Exec(
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (9999, 'future', '2026-01-01T00:00:00Z')",
	); err != nil {
		t.Fatal(err)
	}

	if err := checkSchemaCompatible(db, store.Migrations()); err == nil {
		t.Fatal("expected refusal when on-disk schema version exceeds this binary's MaxVersion")
	}
}
