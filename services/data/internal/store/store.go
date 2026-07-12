// Package store owns the Data Service's SQLite database: opening it with the
// right pragmas, migrating its schema, and (from W1.2 on) the outbox helper
// that commits state changes and their events in one transaction.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the CGO-free "sqlite" driver
)

// Open opens (creating if needed) the SQLite database at path, configured for
// the Data Service: WAL journaling, foreign keys on, and a busy timeout so
// writers queue instead of erroring. The parent directory is created if
// missing.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// The Data Service is SQLite's only client (spec §15), and SQLite allows
	// one writer at a time. A single pooled connection sidesteps
	// SQLITE_BUSY between our own connections; revisit with a dedicated
	// read pool if profiling ever demands it.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	return db, nil
}
