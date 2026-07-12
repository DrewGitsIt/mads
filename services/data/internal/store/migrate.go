package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrations is the production migration set, embedded at build time.
// Migrate/Rollback take an fs.FS so tests can drive fixture sets.
func Migrations() fs.FS { return migrationFS }

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

type migration struct {
	version  int
	name     string
	up, down string
	hasDown  bool
}

// Migrate applies all pending migrations from fsys in version order, each in
// its own transaction recorded in schema_migrations. Running it twice is a
// no-op the second time.
func Migrate(db *sql.DB, fsys fs.FS) error {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if err := ensureMigrationTable(db); err != nil {
		return err
	}
	current, err := Version(db)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		if err := applyInTx(db, m.up, func(tx *sql.Tx) error {
			_, err := tx.Exec(
				"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
				m.version, m.name, time.Now().UTC().Format(time.RFC3339),
			)
			return err
		}); err != nil {
			return fmt.Errorf("store: migration %04d_%s up: %w", m.version, m.name, err)
		}
	}
	return nil
}

// Rollback reverts applied migrations from fsys, newest first, until the
// schema version equals target. Rollback(db, fsys, 0) reverts everything.
func Rollback(db *sql.DB, fsys fs.FS, target int) error {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if err := ensureMigrationTable(db); err != nil {
		return err
	}
	current, err := Version(db)
	if err != nil {
		return err
	}
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if m.version > current || m.version <= target {
			continue
		}
		if !m.hasDown {
			return fmt.Errorf("store: migration %04d_%s has no down file", m.version, m.name)
		}
		if err := applyInTx(db, m.down, func(tx *sql.Tx) error {
			_, err := tx.Exec("DELETE FROM schema_migrations WHERE version = ?", m.version)
			return err
		}); err != nil {
			return fmt.Errorf("store: migration %04d_%s down: %w", m.version, m.name, err)
		}
	}
	return nil
}

// Version returns the highest applied migration version, 0 if none.
func Version(db *sql.DB) (int, error) {
	if err := ensureMigrationTable(db); err != nil {
		return 0, err
	}
	var v int
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}

func ensureMigrationTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("store: ensure schema_migrations: %w", err)
	}
	return nil
}

func applyInTx(db *sql.DB, script string, record func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(script); err != nil {
		return err
	}
	if err := record(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations dir: %w", err)
	}
	byVersion := map[int]*migration{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("store: migration file %q does not match NNNN_name.up|down.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		mig := byVersion[version]
		if mig == nil {
			mig = &migration{version: version, name: m[2]}
			byVersion[version] = mig
		}
		if mig.name != m[2] {
			return nil, fmt.Errorf("store: version %04d has conflicting names %q and %q", version, mig.name, m[2])
		}
		if m[3] == "up" {
			mig.up = string(body)
		} else {
			mig.down = string(body)
			mig.hasDown = true
		}
	}
	ms := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.up == "" {
			return nil, fmt.Errorf("store: migration %04d_%s has a down file but no up file", m.version, m.name)
		}
		ms = append(ms, *m)
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}
