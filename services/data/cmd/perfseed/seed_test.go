package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// testProfile is a small profile covering the types whose shape and
// bookkeeping the seeder special-cases.
func testProfile() *Profile {
	ev := func(n int, mean float64, p50, p95, maxB int) TypeStats {
		return TypeStats{Count: n, Mean: mean, P50: p50, P95: p95, Max: maxB}
	}
	return &Profile{
		Name: "test", Seed: 7, Sessions: 6, ActiveSessions: 1, DelegateSessions: 3, Delegates: 9,
		RunningDelegates: 2, Links: 2, WatchedRepos: 1, SessionSkew: 1.1,
		Events: map[string]TypeStats{
			"session.subagent_event":    ev(900, 2300, 900, 9000, 60000),
			"session.subagent_task":     ev(300, 430, 300, 1200, 5000),
			"session.delegate_changed":  ev(40, 930, 900, 1100, 1300),
			"session.agent_state":       ev(200, 110, 100, 180, 260),
			"session.created":           ev(9, 350, 340, 400, 1200),
			"session.deleted":           ev(3, 43, 40, 60, 300),
			"session.turn_ended":        ev(50, 4900, 2000, 20000, 80000),
			"session.updated":           ev(20, 0, 0, 0, 0),
			"session.usage_updated":     ev(20, 2, 2, 2, 2),
			"file.changed":              ev(80, 141, 130, 220, 254),
			"job.queued":                ev(100, 55, 55, 59, 59),
			"repo.changed":              {Count: 5, Mean: 77, P50: 77, P95: 89, Max: 89, GlobalFrac: 1},
			"link.created":              ev(2, 125, 124, 131, 131),
			"link.message_injected":     ev(4, 1000, 800, 3000, 7000),
			"config.updated":            ev(1, 0, 0, 0, 0),
			"workspace.created":         ev(1, 15, 15, 15, 15),
			"peer.reachability_changed": ev(3, 112, 110, 150, 157), // not on this build: skipped
		},
	}
}

func seedTest(t *testing.T, p *Profile) (*sql.DB, *Manifest) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mads.db")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	m, err := Seed(context.Background(), path, p, SeedOptions{RepoDir: "/tmp/perf-repo", Now: now})
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	db, err := openReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, m
}

func TestSeedRefusesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mads.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Seed(context.Background(), path, testProfile(), SeedOptions{RepoDir: "/tmp/x"}); err == nil {
		t.Fatal("Seed wrote over an existing database")
	}
}

func TestSeedCountsMatchProfile(t *testing.T) {
	p := testProfile()
	db, m := seedTest(t, p)
	counts, _, err := statsByType(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for typ, st := range p.Events {
		if !isRegistered(typ) {
			if counts[typ] != 0 {
				t.Errorf("%s: unregistered type written", typ)
			}
			continue
		}
		if counts[typ] != st.Count {
			t.Errorf("%s: %d events, profile says %d", typ, counts[typ], st.Count)
		}
	}
	for typ, n := range counts {
		if _, ok := p.Events[typ]; !ok {
			t.Logf("%s: %d events from the store's bootstrap, not in the profile", typ, n)
		}
	}
	if len(m.Skipped) != 1 || !strings.HasPrefix(m.Skipped[0], "peer.reachability_changed") {
		t.Errorf("skipped = %v, want only peer.reachability_changed", m.Skipped)
	}
}

func TestSeedSeqContiguous(t *testing.T) {
	db, m := seedTest(t, testProfile())
	var n, lo, hi int64
	if err := db.QueryRow(`SELECT count(*), min(seq), max(seq) FROM events`).Scan(&n, &lo, &hi); err != nil {
		t.Fatal(err)
	}
	if lo != 1 || hi != n {
		t.Fatalf("seq runs %d..%d over %d events: not contiguous from 1", lo, hi, n)
	}
	if int(n) != m.TotalEvents {
		t.Fatalf("manifest says %d events, log has %d", m.TotalEvents, n)
	}
}

func TestSeedRecordsConsistent(t *testing.T) {
	p := testProfile()
	db, m := seedTest(t, p)
	ctx := context.Background()
	log := store.NewLog(db)

	sessions, err := store.NewSessions(log).List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != p.Sessions {
		t.Fatalf("%d sessions, want %d", len(sessions), p.Sessions)
	}
	live := map[string]bool{}
	for _, s := range sessions {
		live[s.ID] = true
		if s.AgentState == nil || s.AgentState.State != contracts.AgentIdle {
			t.Errorf("session %s: agent state %+v, want idle", s.ID, s.AgentState)
		}
	}
	if m.Parent.ID != m.Sessions[0].ID || !live[m.SwitchA.ID] || !live[m.SwitchB.ID] || m.SwitchA.ID == m.SwitchB.ID {
		t.Fatalf("manifest scenario refs are wrong: %+v / %+v / %+v", m.Parent, m.SwitchA, m.SwitchB)
	}

	// Each delegate's record is its last session.delegate_changed.
	envs, err := log.Query(ctx, store.Filter{Types: []string{"session.delegate_changed"}})
	if err != nil {
		t.Fatal(err)
	}
	last := map[string]contracts.Delegate{}
	for _, e := range envs {
		var d contracts.Delegate
		if err := json.Unmarshal(e.Payload, &d); err != nil {
			t.Fatal(err)
		}
		last[d.SessionID+"/"+d.HarnessID] = d
	}
	dstore := store.NewDelegates(log)
	total, running := 0, 0
	toolUse := map[string]string{} // tool_use_id → session
	for _, s := range sessions {
		ds, err := dstore.ForSession(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		total += len(ds)
		for _, d := range ds {
			toolUse[d.ToolUseID] = s.ID
			ev, ok := last[s.ID+"/"+d.HarnessID]
			if !ok {
				t.Fatalf("delegate %s has no event", d.HarnessID)
			}
			a, _ := json.Marshal(ev)
			b, _ := json.Marshal(d)
			if string(a) != string(b) {
				t.Errorf("delegate %s: record and last event differ:\n%s\n%s", d.HarnessID, b, a)
			}
			if d.State == contracts.DelegateRunning {
				running++
				if s.ID != m.Parent.ID {
					t.Errorf("running delegate %s outside the parent session", d.HarnessID)
				}
			}
		}
	}
	if total != p.Delegates || running != p.RunningDelegates || len(m.Parent.Running) != p.RunningDelegates {
		t.Fatalf("delegates %d (running %d, manifest %d), want %d (running %d)",
			total, running, len(m.Parent.Running), p.Delegates, p.RunningDelegates)
	}

	// Subagent traffic belongs to a real delegate of the session it is on.
	sub, err := log.Query(ctx, store.Filter{Types: []string{"session.subagent_event", "session.subagent_task"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range sub {
		var x struct {
			ParentToolUseID string `json:"parent_tool_use_id"`
			ToolUseID       string `json:"tool_use_id"`
		}
		if err := json.Unmarshal(e.Payload, &x); err != nil {
			t.Fatalf("seq %d: %v", e.Seq, err)
		}
		id := x.ParentToolUseID
		if e.Type == contracts.SessionSubagentTask {
			id = x.ToolUseID
		}
		if toolUse[id] != e.SessionID {
			t.Fatalf("seq %d (%s): tool use %q is not a delegate of session %s", e.Seq, e.Type, id, e.SessionID)
		}
	}

	// Global types carry no session; created/deleted extras are phantoms.
	all, err := log.Query(ctx, store.Filter{Types: []string{"repo.changed", "session.deleted"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if e.Type == contracts.RepoChanged && e.SessionID != "" {
			t.Errorf("repo.changed seq %d has a session", e.Seq)
		}
		if e.Type == contracts.SessionDeleted && live[e.SessionID] {
			t.Errorf("session.deleted seq %d names a live session", e.Seq)
		}
	}
}

func TestSeedPayloadSizes(t *testing.T) {
	p := testProfile()
	_, m := seedTest(t, p)
	// Types whose payload has a free-text field land on the profile's mean.
	for _, typ := range []string{"session.subagent_event", "session.subagent_task", "session.turn_ended", "file.changed", "session.delegate_changed"} {
		want := p.Events[typ].Mean * float64(p.Events[typ].Count)
		got := float64(m.Bytes[typ])
		if math.Abs(got-want)/want > 0.10 {
			t.Errorf("%s: %.0f bytes, want %.0f ±10%%", typ, got, want)
		}
	}
}

func TestSeedDeterministic(t *testing.T) {
	p := testProfile()
	dbA, _ := seedTest(t, p)
	dbB, _ := seedTest(t, p)
	// The store mints its own ids and timestamps for workspaces, links and
	// delegates; everything the seeder generates must repeat exactly.
	q := `SELECT seq, type, COALESCE(session_id,''), timestamp, COALESCE(payload,'') FROM events
	      WHERE actor = 'perfseed' AND type NOT IN ('session.delegate_changed','session.created','link.created','link.message_injected','workspace.created','config.updated')
	        AND type <> 'session.agent_state' ORDER BY seq`
	read := func(db *sql.DB) []string {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var seq int64
			var a, b, c, d string
			if err := rows.Scan(&seq, &a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, a+"|"+b+"|"+c+"|"+d)
		}
		return out
	}
	a, b := read(dbA), read(dbB)
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("runs differ in length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("event %d differs between two seeds of one profile:\n%.200s\n%.200s", i, a[i], b[i])
		}
	}
}

func TestCalibrateReadsOnlyShape(t *testing.T) {
	p := testProfile()
	path := filepath.Join(t.TempDir(), "mads.db")
	if _, err := Seed(context.Background(), path, p, SeedOptions{RepoDir: "/tmp/r", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := openReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`DELETE FROM events`); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
	got, err := calibrate(context.Background(), db, "cal")
	if err != nil {
		t.Fatal(err)
	}
	if got.Sessions != p.Sessions || got.Delegates != p.Delegates || got.Links != p.Links {
		t.Errorf("records: %+v", got)
	}
	if got.Events["session.subagent_event"].Count != 900 {
		t.Errorf("subagent_event count %d", got.Events["session.subagent_event"].Count)
	}
	if g := got.Events["repo.changed"].GlobalFrac; g != 1 {
		t.Errorf("repo.changed global_frac %v", g)
	}
	// The round trip seeds again: a calibrated profile is a valid input.
	if err := got.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSeedDelegateTranscriptReadsAsClaudeCode(t *testing.T) {
	p := testProfile()
	dir := t.TempDir()
	tr := filepath.Join(dir, "projects")
	m, err := Seed(context.Background(), filepath.Join(dir, "mads.db"), p, SeedOptions{RepoDir: "/tmp/perf-repo", TranscriptsDir: tr, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Parent.WithFile == nil || m.Parent.NoFile == nil || m.Parent.WithFile.HarnessID == m.Parent.NoFile.HarnessID {
		t.Fatalf("manifest parent: %+v", m.Parent)
	}
	db, err := openReadOnly(filepath.Join(dir, "mads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	sess, err := store.NewSessions(store.NewLog(db)).Get(context.Background(), m.Parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Provider != "claude-code" || sess.ProviderRef == "" {
		t.Fatalf("parent provider %q ref %q", sess.Provider, sess.ProviderRef)
	}
	prov := claudecode.New(claudecode.Config{TranscriptDir: tr})
	msgs, ok, err := prov.DelegateTranscript(context.Background(), sess.ProviderRef, m.Parent.WithFile.HarnessID)
	if err != nil || !ok || len(msgs) < 2 {
		t.Fatalf("claude-code read of the seeded file: ok=%v err=%v msgs=%d", ok, err, len(msgs))
	}
	if _, ok, _ := prov.DelegateTranscript(context.Background(), sess.ProviderRef, m.Parent.NoFile.HarnessID); ok {
		t.Fatal("the no-file delegate has a file")
	}
}

func TestRealismPreparesTheCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "copy.db")
	if _, err := Seed(context.Background(), path, testProfile(), SeedOptions{RepoDir: "/tmp/perf-repo/x", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := openCopy(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// A shadow store recorded on a session: the copy must not keep it.
	if _, err := db.Exec(`UPDATE sessions SET data = json_set(data, '$.shadow_dir', '/home/someone/.local/share/x') WHERE rowid = 1`); err != nil {
		t.Fatal(err)
	}
	before, _, _ := statsByType(context.Background(), db)
	m, sum, err := realism(context.Background(), db, "/tmp/perf-safe")
	if err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE data LIKE '%/tmp/perf-repo%' OR data LIKE '%shadow_dir%'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 || sum.Rewritten == 0 {
		t.Fatalf("paths left in sessions: %d (rewritten %d)", leaked, sum.Rewritten)
	}
	var running int
	if err := db.QueryRow(`SELECT count(*) FROM delegates WHERE session_id = ? AND state = 'running'`, m.Parent.ID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running < 1 || len(m.Parent.Running) != 1 || m.Rail == nil || len(m.Rail.IDs) != 3 || m.SwitchA.ID == m.SwitchB.ID {
		t.Fatalf("manifest: %+v", m)
	}
	// Events are untouched: the replay is the thing measured.
	for typ, n := range before {
		if m.Counts[typ] != n {
			t.Fatalf("%s: %d events before, %d after", typ, n, m.Counts[typ])
		}
	}
}
