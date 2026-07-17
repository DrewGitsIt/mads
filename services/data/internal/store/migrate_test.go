package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// fixture is a two-migration set exercising the framework independently of
// the production migrations.
var fixture = fstest.MapFS{
	"migrations/0001_widgets.up.sql":   {Data: []byte("CREATE TABLE widgets (id INTEGER PRIMARY KEY);")},
	"migrations/0001_widgets.down.sql": {Data: []byte("DROP TABLE widgets;")},
	"migrations/0002_gears.up.sql":     {Data: []byte("CREATE TABLE gears (id INTEGER PRIMARY KEY);")},
	"migrations/0002_gears.down.sql":   {Data: []byte("DROP TABLE gears;")},
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigrateUpDownRoundTrip(t *testing.T) {
	db := openTestDB(t)

	if err := Migrate(db, fixture); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(db); v != 2 {
		t.Fatalf("after up: version = %d, want 2", v)
	}
	if !tableExists(t, db, "widgets") || !tableExists(t, db, "gears") {
		t.Fatal("after up: expected both tables")
	}

	// Idempotent: a second run applies nothing.
	if err := Migrate(db, fixture); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	if err := Rollback(db, fixture, 1); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(db); v != 1 {
		t.Fatalf("after partial rollback: version = %d, want 1", v)
	}
	if tableExists(t, db, "gears") || !tableExists(t, db, "widgets") {
		t.Fatal("partial rollback reverted the wrong migration")
	}

	if err := Rollback(db, fixture, 0); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(db); v != 0 {
		t.Fatalf("after full rollback: version = %d, want 0", v)
	}
	if tableExists(t, db, "widgets") {
		t.Fatal("full rollback left widgets behind")
	}
}

func TestMigrateFailureRollsBackAtomically(t *testing.T) {
	db := openTestDB(t)
	broken := fstest.MapFS{
		"migrations/0001_bad.up.sql": {Data: []byte(
			"CREATE TABLE fine (id INTEGER PRIMARY KEY); CREATE TABLE fine (id INTEGER PRIMARY KEY);",
		)},
	}
	if err := Migrate(db, broken); err == nil {
		t.Fatal("expected failure from broken migration")
	}
	if tableExists(t, db, "fine") {
		t.Error("failed migration left partial schema behind")
	}
	if v, _ := Version(db); v != 0 {
		t.Errorf("failed migration recorded a version: %d", v)
	}
}

func TestMigrateRejectsMalformedFilenames(t *testing.T) {
	db := openTestDB(t)
	bad := fstest.MapFS{
		"migrations/init.sql": {Data: []byte("CREATE TABLE x (id INTEGER);")},
	}
	if err := Migrate(db, bad); err == nil {
		t.Fatal("expected error for malformed migration filename")
	}
}

// The production migration set must always round-trip cleanly.
func TestProductionMigrations(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatal(err)
	}
	v, _ := Version(db)
	if v < 1 {
		t.Fatalf("production set applied nothing (version %d)", v)
	}
	if err := Rollback(db, Migrations(), 0); err != nil {
		t.Fatalf("production set does not roll back: %v", err)
	}
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatalf("production set does not re-apply after rollback: %v", err)
	}
}

func TestMaxVersion(t *testing.T) {
	got, err := MaxVersion(Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if got != 14 {
		t.Fatalf("MaxVersion(production set) = %d, want 14 (update this literal when a migration is added)", got)
	}

	// fstest.MapFS can't represent an empty directory (it synthesizes
	// directories from file paths), so use a real empty "migrations" dir on
	// disk via os.DirFS instead.
	emptyRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(emptyRoot, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	empty, err := MaxVersion(os.DirFS(emptyRoot))
	if err != nil {
		t.Fatal(err)
	}
	if empty != 0 {
		t.Fatalf("MaxVersion(empty fixture) = %d, want 0", empty)
	}
}
