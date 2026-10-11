package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/drewkidwell/mads/pkg/contracts"
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

// TestMigrationConvertsExistingProfileSettingsToSnapshot exercises migration
// 0016: an old-shaped profile row (the pre-profiles-v2 ProfileSettings
// overlay, stored under "$.settings") must convert to the new sparse
// snapshot shape ("$.snapshot") with the same fields, on upgrade.
func TestMigrationConvertsExistingProfileSettingsToSnapshot(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(db, Migrations(), 15); err != nil {
		t.Fatalf("rollback to pre-0016: %v", err)
	}

	oldData := `{"id":"prf_legacy","name":"legacy","description":"","settings":{"synthesis_mode":"off","concurrency_limit":2},"system":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	if _, err := db.Exec(
		`INSERT INTO profiles (id, name, system, data, created_at, updated_at) VALUES (?, ?, 0, ?, ?, ?)`,
		"prf_legacy", "legacy", oldData, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db, Migrations()); err != nil {
		t.Fatalf("re-migrate to apply 0016: %v", err)
	}

	var migrated string
	if err := db.QueryRow("SELECT data FROM profiles WHERE id = ?", "prf_legacy").Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	var p contracts.Profile
	if err := json.Unmarshal([]byte(migrated), &p); err != nil {
		t.Fatalf("migrated row does not unmarshal as a Profile: %v (data=%s)", err, migrated)
	}
	if len(p.Snapshot) != 2 {
		t.Fatalf("migrated snapshot = %+v, want 2 fields", p.Snapshot)
	}
	var mode string
	if err := json.Unmarshal(p.Snapshot["synthesis_mode"], &mode); err != nil || mode != "off" {
		t.Fatalf("migrated snapshot.synthesis_mode = %v, want off (err=%v)", p.Snapshot["synthesis_mode"], err)
	}
	var limit int
	if err := json.Unmarshal(p.Snapshot["concurrency_limit"], &limit); err != nil || limit != 2 {
		t.Fatalf("migrated snapshot.concurrency_limit = %v, want 2 (err=%v)", p.Snapshot["concurrency_limit"], err)
	}
	if strings.Contains(migrated, `"settings"`) {
		t.Fatalf("migrated row must not retain the old settings key: %s", migrated)
	}
}

// TestMigrationDefaultTemplateUsesMakeCheck exercises migration 0017: the
// seeded default template's build/verify stages must invoke make check
// (lint+test), not bare make test, or an in-diff lint error rides the gate
// straight through to land (672a6bc).
func TestMigrationDefaultTemplateUsesMakeCheck(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatal(err)
	}

	var source string
	if err := db.QueryRow("SELECT data FROM templates WHERE id = ?", "tpl_system_default").Scan(&source); err != nil {
		t.Fatal(err)
	}
	var tpl contracts.Template
	if err := json.Unmarshal([]byte(source), &tpl); err != nil {
		t.Fatalf("migrated row does not unmarshal as a Template: %v (data=%s)", err, source)
	}
	if strings.Contains(tpl.Source, "make build && make test") {
		t.Errorf("build stage still runs make build && make test, want make check: %s", tpl.Source)
	}
	if !strings.Contains(tpl.Source, "make build && make check") {
		t.Errorf("build stage does not run make check: %s", tpl.Source)
	}
	if !strings.Contains(tpl.Source, "command: make check\n") {
		t.Errorf("verify stage does not run make check: %s", tpl.Source)
	}
}

func TestMaxVersion(t *testing.T) {
	got, err := MaxVersion(Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if got != 30 {
		t.Fatalf("MaxVersion(production set) = %d, want 30 (update this literal when a migration is added)", got)
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
