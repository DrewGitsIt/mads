package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// mkShadowDir creates a fake shadow store dir named after the session (the
// shape the session runner records) and returns its path.
func mkShadowDir(t *testing.T, sessionID string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), sessionID)
	if err := os.MkdirAll(filepath.Join(dir, "abc123"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ageSession rewrites a session's UpdatedAt directly in the row — the
// sweep's "archived N days ago" input, unreachable through Update, which
// always stamps now.
func ageSession(t *testing.T, l *Log, id string, when time.Time) {
	t.Helper()
	ctx := context.Background()
	var data string
	if err := l.db.QueryRowContext(ctx, "SELECT data FROM sessions WHERE id = ?", id).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var sess contracts.Session
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		t.Fatal(err)
	}
	sess.UpdatedAt = when
	raw, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.ExecContext(ctx, "UPDATE sessions SET data = ?, updated_at = ? WHERE id = ?",
		string(raw), when.Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
}

// withShadow flips a created session to the given activity and shadow dir.
func withShadow(t *testing.T, s *Sessions, sess contracts.Session, activity contracts.SessionActivity, dir string) contracts.Session {
	t.Helper()
	sess.Activity = activity
	sess.ShadowDir = dir
	if err := s.Update(context.Background(), &sess, "test"); err != nil {
		t.Fatal(err)
	}
	return sess
}

// TestSessionDeleteRemovesShadowDir: deleting a session removes its shadow
// store from disk — shadow dirs die with their session.
func TestSessionDeleteRemovesShadowDir(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()

	sess := mkSession(t, s, contracts.Session{})
	dir := mkShadowDir(t, sess.ID)
	withShadow(t, s, sess, contracts.SessionInactive, dir)

	if err := s.Delete(ctx, sess.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("shadow dir still exists after session delete: %v", err)
	}
}

// TestSessionDeleteRefusesForeignShadowDir: a recorded shadow_dir whose
// basename isn't the session ID is never removed — the guard against a
// corrupt record aiming RemoveAll somewhere real.
func TestSessionDeleteRefusesForeignShadowDir(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()

	foreign := t.TempDir() // basename is a temp name, not the session ID
	sess := mkSession(t, s, contracts.Session{})
	withShadow(t, s, sess, contracts.SessionInactive, foreign)

	if err := s.Delete(ctx, sess.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("guard failed: foreign dir removed (%v)", err)
	}
}

// TestSweepExpiredShadows: only inactive sessions past retention lose
// their shadow store; recent, active, and shadow-less sessions are left
// alone, and a swept record's ShadowDir is cleared without bumping
// UpdatedAt.
func TestSweepExpiredShadows(t *testing.T) {
	retention := 30 * 24 * time.Hour
	old := time.Now().UTC().Add(-31 * 24 * time.Hour).Truncate(time.Second)
	recent := time.Now().UTC().Add(-24 * time.Hour)

	tests := []struct {
		name      string
		activity  contracts.SessionActivity
		updatedAt time.Time
		hasShadow bool
		wantSwept bool
	}{
		{name: "expired inactive is swept", activity: contracts.SessionInactive, updatedAt: old, hasShadow: true, wantSwept: true},
		{name: "recent inactive is kept", activity: contracts.SessionInactive, updatedAt: recent, hasShadow: true},
		{name: "expired active is kept", activity: contracts.SessionActive, updatedAt: old, hasShadow: true},
		{name: "expired inactive without shadow is a no-op", activity: contracts.SessionInactive, updatedAt: old},
	}

	s, _, l := newSessionStore(t)
	ctx := context.Background()
	type row struct {
		sess contracts.Session
		dir  string
		tc   int
	}
	var rows []row
	for i, tc := range tests {
		sess := mkSession(t, s, contracts.Session{Name: tc.name})
		dir := ""
		if tc.hasShadow {
			dir = mkShadowDir(t, sess.ID)
		}
		withShadow(t, s, sess, tc.activity, dir)
		ageSession(t, l, sess.ID, tc.updatedAt)
		rows = append(rows, row{sess: sess, dir: dir, tc: i})
	}

	n, err := s.SweepExpiredShadows(ctx, retention)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d shadow store(s), want 1", n)
	}

	for _, r := range rows {
		tc := tests[r.tc]
		t.Run(tc.name, func(t *testing.T) {
			if r.dir != "" {
				_, err := os.Stat(r.dir)
				if tc.wantSwept && !os.IsNotExist(err) {
					t.Errorf("shadow dir still exists: %v", err)
				}
				if !tc.wantSwept && err != nil {
					t.Errorf("shadow dir unexpectedly gone: %v", err)
				}
			}
			got, err := s.Get(ctx, r.sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantSwept {
				if got.ShadowDir != "" {
					t.Errorf("shadow_dir = %q after sweep, want cleared", got.ShadowDir)
				}
				if !got.UpdatedAt.Equal(tc.updatedAt) {
					t.Errorf("UpdatedAt = %v after sweep, want untouched %v", got.UpdatedAt, tc.updatedAt)
				}
			} else if tc.hasShadow && got.ShadowDir == "" {
				t.Error("shadow_dir cleared for a session the sweep should have kept")
			}
		})
	}
}
