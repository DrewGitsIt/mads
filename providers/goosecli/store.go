package goosecli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // the repo's CGO-free driver (services/data/internal/store)

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/providers/provider"
)

// The TranscriptReader reads goose's own store, sessions.db (SQLite, WAL),
// written by the goose process itself — not `goose session export`, which
// spawns goose and re-serialises the whole session on every call: the
// session child reads the store after every settle hint (docs/
// CHAT-STREAM-DESIGN.md §3), so a multi-hour session would pay a process
// spawn and a full-session JSON dump per tool call. The store is read
// read-only (mode=ro, query_only) and incrementally by row id.
//
// The schema is goose's private one (testdata/sessions-schema.sql, goose
// 1.50.1, schema_version 16). Two guards keep a reader of it honest: the
// preflight version window (pkg/preflight: goose 1.46–1.51), and
// checkStoreSchema here — the tables and columns this reader uses must all
// be present, or the reader falls back to the export (Transcript) and
// refuses incremental reads (TranscriptSince → ErrUnsupported).
//
// goose writes messages in conversation order and, live, only ever inserts
// (agent.rs:3532-3554 writes a message when its model call and tools are
// done — survey lag.txt: the text before a tool lands with the tool, 6 s
// after it for `sleep 6`). Row-id order is therefore conversation order and
// a row id is a cursor. goose's own reads order by (created_timestamp, id)
// (session_manager.rs get_conversation); for rows written live the two
// agree. The exceptions rewrite rows — replace_conversation (compaction)
// deletes and reinserts the session, truncation deletes its tail — and the
// cursor notices: it remembers how many of the session's rows lay at or
// below it, and a different count now is provider.ErrCursorStale.

// errStoreUnusable: sessions.db can't serve this session — absent, holding
// no row for it, or not the schema this reader knows. Transcript falls back
// to the export; TranscriptSince answers ErrUnsupported.
var errStoreUnusable = errors.New("goose sessions.db unusable")

// storeColumns is every column this reader touches, per table.
var storeColumns = map[string][]string{
	"sessions": {"id"},
	"messages": {"id", "message_id", "session_id", "role", "content_json", "created_timestamp", "metadata_json"},
}

// storeCursor is TranscriptSince's position: the last row id read for the
// session, and how many of the session's rows had an id at or below it.
type storeCursor struct {
	RowID int64
	N     int64
}

// storePath is where the spawned goose keeps sessions.db, from exactly the
// environment the engine hands it (goose crates/goose/src/config/paths.rs
// Paths::data_dir): an absolute GOOSE_PATH_ROOT means <root>/data; else
// etcetera's XDG strategy, which goose uses on macOS too — $XDG_DATA_HOME
// when absolute, else $HOME/.local/share — under "goose". Then
// sessions/sessions.db (session_manager.rs SESSIONS_FOLDER, DB_NAME). A
// StartOptions.Env override of these per session isn't visible here.
func (p *Provider) storePath() string {
	env := agentenv.Build(p.cfg.EnvMode, os.Environ(), p.cfg.Env, p.cfg.EnvPassthrough, nil)
	get := func(name string) string {
		v := ""
		for _, kv := range env {
			if val, ok := strings.CutPrefix(kv, name+"="); ok {
				v = val // last wins, as in the child
			}
		}
		return v
	}
	if root := get("GOOSE_PATH_ROOT"); filepath.IsAbs(root) {
		return filepath.Join(root, "data", "sessions", "sessions.db")
	}
	data := get("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		home := get("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "goose", "sessions", "sessions.db")
}

// openStore opens sessions.db read-only. mode=ro, not immutable: goose may
// be writing it, and the WAL must be read to see its latest rows.
func openStore(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%w: %w", errStoreUnusable, err)
	}
	dsn := "file:" + url.PathEscape(path) + "?mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// checkStoreSchema is the column guard: every table and column in
// storeColumns must exist.
func checkStoreSchema(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) error {
	for table, want := range storeColumns {
		rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return err
			}
			have[name] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, col := range want {
			if !have[col] {
				return fmt.Errorf("%w: table %s has no column %s (a goose schema this reader doesn't know)", errStoreUnusable, table, col)
			}
		}
	}
	return nil
}

// readStore reads ref's messages from sessions.db in row-id order. With a
// cursor, only the rows after it, and the cursor after them; a cursor whose
// rows were rewritten is provider.ErrCursorStale. errStoreUnusable when
// the store can't serve the session at all.
func (p *Provider) readStore(ctx context.Context, ref string, cur *storeCursor) ([]exportMessage, storeCursor, error) {
	path := p.storePath()
	db, err := openStore(path)
	if err != nil {
		return nil, storeCursor{}, err
	}
	defer func() { _ = db.Close() }()
	// One read transaction: the cursor check and the rows come from the
	// same snapshot.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storeCursor{}, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkStoreSchema(ctx, tx); err != nil {
		return nil, storeCursor{}, err
	}
	var one int
	switch err := tx.QueryRowContext(ctx, "SELECT 1 FROM sessions WHERE id = ?", ref).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, storeCursor{}, fmt.Errorf("%w: no session %s in %s", errStoreUnusable, ref, path)
	case err != nil:
		return nil, storeCursor{}, fmt.Errorf("read %s: %w", path, err)
	}
	var from storeCursor
	if cur != nil {
		from = *cur
		var n int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE session_id = ? AND id <= ?", ref, from.RowID).Scan(&n); err != nil {
			return nil, storeCursor{}, fmt.Errorf("read %s: %w", path, err)
		}
		if n != from.N {
			return nil, from, fmt.Errorf("goose rewrote session %s under the cursor (%d rows at or below row %d, was %d): %w", ref, n, from.RowID, from.N, provider.ErrCursorStale)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(message_id, ''), role, content_json, created_timestamp, COALESCE(metadata_json, '')
		FROM messages WHERE session_id = ? AND id > ? ORDER BY id`, ref, from.RowID)
	if err != nil {
		return nil, storeCursor{}, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = rows.Close() }()
	next := from
	var out []exportMessage
	for rows.Next() {
		var m exportMessage
		var content, meta string
		if err := rows.Scan(&m.rowID, &m.ID, &m.Role, &content, &m.Created, &meta); err != nil {
			return nil, storeCursor{}, fmt.Errorf("read %s: %w", path, err)
		}
		next.RowID, next.N = m.rowID, next.N+1
		// A row this reader can't decode is goose's format moving under
		// it: the same verdict as a missing column.
		if err := json.Unmarshal([]byte(content), &m.Content); err != nil {
			return nil, storeCursor{}, fmt.Errorf("%w: row %d content_json: %w", errStoreUnusable, m.rowID, err)
		}
		if meta != "" {
			if err := json.Unmarshal([]byte(meta), &m.Metadata); err != nil {
				return nil, storeCursor{}, fmt.Errorf("%w: row %d metadata_json: %w", errStoreUnusable, m.rowID, err)
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, storeCursor{}, fmt.Errorf("read %s: %w", path, err)
	}
	return out, next, nil
}
