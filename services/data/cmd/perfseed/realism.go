package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/drewkidwell/mads/services/data/internal/store"
)

// Realism mode (tools/perf/docs/PLAN.md, phase 4): the workload is a COPY
// of a real database, taken by run.sh with `sqlite3 -readonly <live>
// ".backup <copy>"`. This prepares that copy — and only the copy — so a
// hermetic stack can boot on it and touch nothing real:
//
//   - schema: a copy from a newer build is stepped down to this build's
//     schema, for the one known case (0035 mesh_peers, which main lacks);
//   - paths: every absolute path in a record's JSON (session cwd/dir/
//     worktree dirs, workspace repos, delegate worktrees and transcripts,
//     config dirs) is pointed at an empty scratch dir, so no git or
//     worktree work can reach a real repo; shadow_dir is dropped, so the
//     Data Service's shadow sweep has nothing to delete. Event payloads are
//     left alone: they are what the bus replays, byte for byte, and nothing
//     executes on them;
//   - one delegate of the parent is marked running, so the Router's boot
//     sweep concludes it and the rail shows it (scenario 3), as it does
//     after a real restart.
//
// It writes the manifest the scenarios need (to a temp dir the runner
// deletes) and a summary with counts and sizes only, for the report.

type realismSummary struct {
	TotalEvents int              `json:"total_events"`
	TotalBytes  int64            `json:"total_bytes"`
	Counts      map[string]int   `json:"counts"`
	Bytes       map[string]int64 `json:"bytes"`
	Sessions    int              `json:"sessions"`
	Delegates   int              `json:"delegates"`
	Downgraded  []int            `json:"schema_steps_down,omitempty"`
	Rewritten   int              `json:"paths_rewritten"`
	Notes       []string         `json:"notes,omitempty"`
}

func openCopy(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, db.Ping()
}

// knownStepsDown are the schema steps a copy may be taken back over: the
// version and the SQL that undoes it. Anything else is refused.
var knownStepsDown = map[int]string{
	35: `DROP TABLE IF EXISTS mesh_peers`,
}

func realism(ctx context.Context, db *sql.DB, safeRoot string) (*Manifest, *realismSummary, error) {
	sum := &realismSummary{}
	maxKnown, err := store.MaxVersion(store.Migrations())
	if err != nil {
		return nil, nil, err
	}
	var v int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(version),0) FROM schema_migrations`).Scan(&v); err != nil {
		return nil, nil, err
	}
	for ; v > maxKnown; v-- {
		q, ok := knownStepsDown[v]
		if !ok {
			return nil, nil, fmt.Errorf("realism: the copy is schema v%d; this build knows v%d and has no step down from v%d", v, maxKnown, v)
		}
		if _, err := db.ExecContext(ctx, q); err != nil {
			return nil, nil, err
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v); err != nil {
			return nil, nil, err
		}
		sum.Downgraded = append(sum.Downgraded, v)
	}

	// Paths, in every table whose records are JSON.
	tables, err := tablesWithData(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range tables {
		n, err := rewritePaths(ctx, db, t, safeRoot, endpointTables[t])
		if err != nil {
			return nil, nil, fmt.Errorf("realism: %s: %w", t, err)
		}
		sum.Rewritten += n
	}
	// And plain columns: file_changes.repo/path and commits.repo name real
	// repos, and the Router's changeset runs git in them.
	n, err := rewriteColumns(ctx, db, safeRoot)
	if err != nil {
		return nil, nil, err
	}
	sum.Rewritten += n

	// Scenario sessions: rail-visible (not set aside), most events first.
	type cand struct {
		id, name     string
		events, subs int
		delegates    int
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, json_extract(s.data,'$.name'),
		       (SELECT count(*) FROM events e WHERE e.session_id = s.id),
		       (SELECT count(*) FROM events e WHERE e.session_id = s.id AND e.type = 'session.subagent_event'),
		       (SELECT count(*) FROM delegates d WHERE d.session_id = s.id)
		FROM sessions s
		WHERE COALESCE(json_extract(s.data,'$.aside'), 0) = 0
		ORDER BY 3 DESC`)
	if err != nil {
		return nil, nil, err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var name sql.NullString
		if err := rows.Scan(&c.id, &name, &c.events, &c.subs, &c.delegates); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		c.name = name.String
		cands = append(cands, c)
	}
	_ = rows.Close()
	if len(cands) < 2 {
		return nil, nil, fmt.Errorf("realism: fewer than two rail-visible sessions")
	}
	m := &Manifest{Profile: "realism"}
	var parent *cand
	for i := range cands {
		if cands[i].delegates > 0 && (parent == nil || cands[i].subs > parent.subs) {
			parent = &cands[i]
		}
	}
	var picks []cand
	for _, c := range cands {
		if parent != nil && c.id == parent.id {
			continue
		}
		if c.name != "" {
			picks = append(picks, c)
		}
		if len(picks) == 2 {
			break
		}
	}
	if len(picks) < 2 {
		return nil, nil, fmt.Errorf("realism: no two named rail-visible sessions")
	}
	m.SwitchA = ManifestRef{ID: picks[0].id, Name: picks[0].name}
	m.SwitchB = ManifestRef{ID: picks[1].id, Name: picks[1].name}
	m.Sessions = []ManifestRef{m.SwitchA, m.SwitchB}
	rail := []string{m.SwitchA.ID, m.SwitchB.ID}
	if parent != nil {
		m.Parent = ManifestParent{ManifestRef: ManifestRef{ID: parent.id, Name: parent.name}, Delegates: parent.delegates}
		m.Sessions = append(m.Sessions, m.Parent.ManifestRef)
		rail = append(rail, parent.id)
		// Its most recently started delegate goes back to running.
		var id, harness, desc string
		var raw string
		err := db.QueryRowContext(ctx, `SELECT id, harness_id, data FROM delegates WHERE session_id = ? ORDER BY started_at DESC LIMIT 1`, parent.id).Scan(&id, &harness, &raw)
		if err != nil {
			return nil, nil, err
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, nil, err
		}
		d["state"] = "running"
		delete(d, "ended_at")
		delete(d, "reason")
		desc, _ = d["description"].(string)
		b, _ := json.Marshal(d)
		if _, err := db.ExecContext(ctx, `UPDATE delegates SET state = 'running', data = ? WHERE id = ?`, string(b), id); err != nil {
			return nil, nil, err
		}
		m.Parent.Running = []ManifestDelegate{{HarnessID: harness, Description: desc}}
		sum.Notes = append(sum.Notes, "one delegate of the parent marked running so the boot sweep surfaces it in the rail")
	} else {
		sum.Notes = append(sum.Notes, "no session with delegates: scenario 3 can't run")
	}
	m.Rail = &ManifestRail{IDs: rail}

	m.Counts, m.Bytes, err = statsByType(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	for t, n := range m.Counts {
		m.TotalEvents += n
		m.TotalBytes += m.Bytes[t]
	}
	sum.TotalEvents, sum.TotalBytes, sum.Counts, sum.Bytes = m.TotalEvents, m.TotalBytes, m.Counts, m.Bytes
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&sum.Sessions); err != nil {
		return nil, nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM delegates`).Scan(&sum.Delegates); err != nil {
		return nil, nil, err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return nil, nil, err
	}
	return m, sum, nil
}

func tablesWithData(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'
		AND EXISTS (SELECT 1 FROM pragma_table_info(sqlite_master.name) WHERE name = 'data')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// endpointTables hold configuration that could reach a network service or
// start a program: their URL, endpoint, key, token, command and host
// fields are blanked in the copy (fast_model_* included).
var endpointTables = map[string]bool{"config": true, "capabilities": true, "agents": true, "profiles": true, "templates": true, "workspaces": true}

var endpointKey = regexp.MustCompile(`(?i)url|endpoint|fast_model|api_?key|token|secret|password|command|^args$|^env$|host|webhook|registry`)

// pathInText finds absolute paths inside free text (a handoff's notes, a
// task's scope), for rewriting where a path isn't the whole value.
var pathInText = regexp.MustCompile(`(?:/Users|/home|/private|/var|/tmp|/Volumes|/opt|/srv|/mnt)/[^\s"'<>()\[\],;:` + "`" + `]*`)

// rewritePaths points every absolute-path string in table's JSON records
// at safeRoot, rewrites paths inside text, drops shadow_dir, and (for an
// endpoint table) blanks endpoint fields. Returns how many it changed.
func rewritePaths(ctx context.Context, db *sql.DB, table, safeRoot string, blankEndpoints bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT rowid, data FROM "`+table+`"`)
	if err != nil {
		return 0, err
	}
	type upd struct {
		rowid int64
		data  string
	}
	var ups []upd
	n := 0
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return 0, err
		}
		var v any
		if json.Unmarshal([]byte(raw), &v) != nil {
			continue
		}
		c := 0
		v = walkPaths(v, safeRoot, blankEndpoints, &c)
		if c > 0 {
			b, _ := json.Marshal(v)
			ups = append(ups, upd{id, string(b)})
			n += c
		}
	}
	_ = rows.Close()
	for _, u := range ups {
		if _, err := db.ExecContext(ctx, `UPDATE "`+table+`" SET data = ? WHERE rowid = ?`, u.data, u.rowid); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func isPath(x string) bool {
	return x == "/" || (strings.HasPrefix(x, "/") && strings.Count(x, "/") > 1) || strings.HasPrefix(x, "~/")
}

// rewriteColumns points every path-valued TEXT column outside the event
// log (and outside the JSON data columns, done above) at safeRoot/<base>.
func rewriteColumns(ctx context.Context, db *sql.DB, safe string) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT IN ('events','sqlite_sequence','schema_migrations','meta')
		AND p.name <> 'data' AND upper(p.type) LIKE 'TEXT%'`)
	if err != nil {
		return 0, err
	}
	type col struct{ t, c string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.t, &c.c); err != nil {
			_ = rows.Close()
			return 0, err
		}
		cols = append(cols, c)
	}
	_ = rows.Close()
	total := 0
	for _, c := range cols {
		q := fmt.Sprintf(`SELECT rowid, %q FROM %q WHERE %q LIKE '%%/%%'`, c.c, c.t, c.c)
		rs, err := db.QueryContext(ctx, q)
		if err != nil {
			return 0, err
		}
		type upd struct {
			id int64
			v  string
		}
		var ups []upd
		for rs.Next() {
			var id int64
			var v string
			if err := rs.Scan(&id, &v); err != nil {
				_ = rs.Close()
				return 0, err
			}
			var nv string
			switch {
			case isPath(v):
				nv = safe
				if v != "/" {
					nv = safe + "/" + filepath.Base(v)
				}
			case strings.Contains(v, ":/") && isPath(v[strings.Index(v, ":/")+1:]):
				// "<provider>:<dir>" keys (account_auth.account).
				nv = v[:strings.Index(v, ":/")+1] + safe
			case pathInText.MatchString(v):
				// Paths inside text: task_handoffs.content, task_notes.content, tasks.scope.
				nv = pathInText.ReplaceAllString(v, safe)
			default:
				continue
			}
			ups = append(ups, upd{id, nv})
		}
		_ = rs.Close()
		for _, u := range ups {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE %q SET %q = ? WHERE rowid = ?`, c.t, c.c), u.v, u.id); err != nil {
				return 0, err
			}
		}
		total += len(ups)
	}
	return total, nil
}

func walkPaths(v any, safe string, blank bool, n *int) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == "shadow_dir" {
				delete(x, k)
				*n++
				continue
			}
			if blank && endpointKey.MatchString(k) {
				switch e.(type) {
				case string:
					x[k] = ""
				case []any:
					x[k] = []any{}
				case map[string]any:
					x[k] = map[string]any{}
				default:
					x[k] = walkPaths(e, safe, blank, n)
					continue
				}
				*n++
				continue
			}
			x[k] = walkPaths(e, safe, blank, n)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = walkPaths(e, safe, blank, n)
		}
		return x
	case string:
		// A path: "/" itself, or rooted with at least one more segment
		// ("/compact"-style command names are left alone).
		if isPath(x) {
			*n++
			return safe
		}
		if r := pathInText.ReplaceAllString(x, safe); r != x {
			*n++
			return r
		}
		return x
	}
	return v
}
