package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Sessions is the session store (spec §1) plus the session-scoped tracking
// records: file changes (spec §5) and commit linkage (spec §3, §5).
type Sessions struct {
	log *Log
}

// NewSessions wraps the event log's database.
func NewSessions(log *Log) *Sessions {
	return &Sessions{log: log}
}

func validateSession(s *contracts.Session) *contracts.ValidationError {
	var v contracts.ValidationError
	if s.Name == "" {
		v.Add("name", "required")
	}
	if s.Activity != "" && s.Activity != contracts.SessionActive && s.Activity != contracts.SessionInactive {
		v.Add("activity", fmt.Sprintf("must be %q or %q", contracts.SessionActive, contracts.SessionInactive))
	}
	if s.Isolation != "" && s.Isolation != contracts.IsolationWorktree && s.Isolation != contracts.IsolationCurrentDir {
		v.Add("isolation", fmt.Sprintf("must be %q or %q", contracts.IsolationWorktree, contracts.IsolationCurrentDir))
	}
	if s.NameSource != "" && s.NameSource != contracts.SessionNameSourceDerived &&
		s.NameSource != contracts.SessionNameSourceUser && s.NameSource != contracts.SessionNameSourceGenerated {
		v.Add("name_source", fmt.Sprintf("must be %q, %q, or %q",
			contracts.SessionNameSourceDerived, contracts.SessionNameSourceUser, contracts.SessionNameSourceGenerated))
	}
	if s.SynthesisMode != "" && !s.SynthesisMode.Valid() {
		v.Add("synthesis_mode", fmt.Sprintf("unknown synthesis mode %q", s.SynthesisMode))
	}
	// Fail closed: an unrecognized trust value would otherwise fall through
	// trustArgs' default branch at spawn (providers/claudecode) and run with
	// FULL access — a typo'd "read-only" must be rejected here, not silently
	// granted everything.
	if s.Trust != "" && !s.Trust.Valid() {
		v.Add("trust", "must be one of: full, limited, read_only")
	}
	if v.Empty() {
		return nil
	}
	return &v
}

// Create stores a new session (implicit on first message, spec §1) and
// announces it. Activity defaults to active, isolation to worktree.
func (s *Sessions) Create(ctx context.Context, sess *contracts.Session, actor string) error {
	if sess.Activity == "" {
		sess.Activity = contracts.SessionActive
	}
	if sess.Isolation == "" {
		sess.Isolation = contracts.IsolationWorktree
	}
	if sess.NameSource == "" {
		sess.NameSource = contracts.SessionNameSourceDerived
	}
	if verr := validateSession(sess); verr != nil {
		return verr
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		if sess.ID == "" {
			sess.ID = NewID("ses")
		}
		now := time.Now().UTC()
		sess.CreatedAt, sess.UpdatedAt = now, now
		if err := s.write(tx, sess); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"name": sess.Name, "workspace": sess.Workspace})
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionCreated, SessionID: sess.ID, Actor: actor, Payload: payload,
		})
	})
}

// Update replaces a session's fields, emitting the specific event for what
// changed: session.renamed, session.activity_changed, and session.updated
// for anything else (provider binding, summary, active task, ...).
func (s *Sessions) Update(ctx context.Context, sess *contracts.Session, actor string) error {
	if verr := validateSession(sess); verr != nil {
		return verr
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		old, err := s.getInTx(tx, sess.ID)
		if err != nil {
			return err
		}
		sess.CreatedAt = old.CreatedAt
		sess.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, sess); err != nil {
			return err
		}
		announced := false
		if old.Name != sess.Name {
			payload, _ := json.Marshal(map[string]string{"from": old.Name, "to": sess.Name})
			if err := tx.Append(&contracts.Envelope{
				Type: contracts.SessionRenamed, SessionID: sess.ID, Actor: actor, Payload: payload,
			}); err != nil {
				return err
			}
			announced = true
		}
		if old.Activity != sess.Activity {
			payload, _ := json.Marshal(map[string]string{"from": string(old.Activity), "to": string(sess.Activity)})
			if err := tx.Append(&contracts.Envelope{
				Type: contracts.SessionActivityChanged, SessionID: sess.ID, Actor: actor, Payload: payload,
			}); err != nil {
				return err
			}
			announced = true
		}
		if !announced {
			return tx.Append(&contracts.Envelope{
				Type: contracts.SessionUpdated, SessionID: sess.ID, Actor: actor,
			})
		}
		return nil
	})
}

// SetActivity flips a session's Activity field within a single transaction,
// unlike a Get-then-Update round trip: that pair carries a snapshot of the
// rest of the record across two separate network calls, so no matter how
// late the client re-reads before writing, a concurrent writer of the same
// session (e.g. a live interactive child persisting its own ProviderRef,
// services/session/internal/runner.go's recordMu) can still land its write
// in the gap and get overwritten. Reading and writing together here, inside
// one transaction, closes that window instead of narrowing it. No-op if
// already at the target value.
func (s *Sessions) SetActivity(ctx context.Context, id string, activity contracts.SessionActivity, actor string) error {
	if activity != contracts.SessionActive && activity != contracts.SessionInactive {
		var v contracts.ValidationError
		v.Add("activity", fmt.Sprintf("must be %q or %q", contracts.SessionActive, contracts.SessionInactive))
		return &v
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		if sess.Activity == activity {
			return nil
		}
		old := sess.Activity
		sess.Activity = activity
		sess.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, sess); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"from": string(old), "to": string(activity)})
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionActivityChanged, SessionID: id, Actor: actor, Payload: payload,
		})
	})
}

// Delete hard-deletes a session and all its orchestration artifacts (spec §1):
// tasks (with their deps/handoffs/notes/leases via cascade), file changes,
// commits, and — after the transaction lands — its shadow git store
// (docs/SHADOW-GIT-DIFFS-ADR.md: shadow dirs die with their session).
// Provider transcripts live in the provider's storage and survive.
func (s *Sessions) Delete(ctx context.Context, id, actor string) error {
	var shadowDir string
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		old, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		shadowDir = old.ShadowDir
		// tasks don't FK to sessions (they may be created before session
		// records in tests); cascade explicitly.
		if _, err := tx.Exec("DELETE FROM tasks WHERE session_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM sessions WHERE id = ?", id); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"name": old.Name})
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionDeleted, SessionID: id, Actor: actor, Payload: payload,
		})
	})
	if err != nil {
		return err
	}
	removeShadowDir(shadowDir, id)
	return nil
}

// removeShadowDir best-effort deletes one session's shadow store. The
// recorded path is only honored when its final element is the session ID —
// the shape the session runner writes (<shadow root>/<session_id>) — so a
// corrupt or hand-edited record can never aim RemoveAll anywhere else.
func removeShadowDir(dir, sessionID string) {
	if dir == "" {
		return
	}
	if filepath.Base(dir) != sessionID {
		log.Printf("sessions: refusing to remove shadow dir %q (basename is not session %s)", dir, sessionID)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("sessions: remove shadow dir %s: %v", dir, err)
	}
}

// SweepExpiredShadows removes the shadow git stores of archived sessions
// past retention (docs/SHADOW-GIT-DIFFS-ADR.md: archived sessions keep
// their shadow 30 days, config-overridable; deliberately no global size
// cap). "Archived" is an inactive session record; UpdatedAt — bumped when
// the session went inactive — is the archive timestamp. Each swept
// session's ShadowDir is cleared in its own transaction, re-checking
// activity inside it so a session resumed mid-sweep is left alone; the
// record's UpdatedAt is deliberately not bumped, so sweeping never
// reorders session lists. Returns how many stores were removed.
func (s *Sessions) SweepExpiredShadows(ctx context.Context, retention time.Duration) (int, error) {
	sessions, err := s.List(ctx, contracts.SessionInactive)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-retention)
	n := 0
	for _, sess := range sessions {
		if sess.ShadowDir == "" || sess.UpdatedAt.After(cutoff) {
			continue
		}
		var dir string
		err := s.log.WithTx(ctx, func(tx *Tx) error {
			cur, err := s.getInTx(tx, sess.ID)
			if err != nil {
				return err
			}
			if cur.Activity != contracts.SessionInactive || cur.ShadowDir == "" {
				return nil // woke up (or already swept) since the List above
			}
			dir = cur.ShadowDir
			cur.ShadowDir = ""
			return s.write(tx, cur)
		})
		if err != nil {
			log.Printf("sessions: shadow sweep %s: %v", sess.ID, err)
			continue
		}
		if dir == "" {
			continue
		}
		removeShadowDir(dir, sess.ID)
		n++
	}
	return n, nil
}

// Get returns one session.
func (s *Sessions) Get(ctx context.Context, id string) (*contracts.Session, error) {
	var data string
	err := s.log.db.QueryRowContext(ctx, "SELECT data FROM sessions WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sessions: %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var sess contracts.Session
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return nil, fmt.Errorf("sessions: corrupt data for %s: %w", id, err)
	}
	return &sess, nil
}

// List returns sessions, optionally filtered by activity, newest first.
func (s *Sessions) List(ctx context.Context, activity contracts.SessionActivity) ([]contracts.Session, error) {
	q, args := "SELECT data FROM sessions", []any{}
	if activity != "" {
		q += " WHERE activity = ?"
		args = append(args, string(activity))
	}
	q += " ORDER BY updated_at DESC"
	rows, err := s.log.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []contracts.Session
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var sess contracts.Session
		if err := json.Unmarshal([]byte(data), &sess); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, sess)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// RecordFileChange stores a change with attribution computed by the §5 rule
// against the session's isolation mode, and emits file.changed.
func (s *Sessions) RecordFileChange(ctx context.Context, fc *contracts.FileChange, actor string) error {
	var v contracts.ValidationError
	if fc.Path == "" {
		v.Add("path", "required")
	}
	switch fc.Change {
	case "added", "modified", "deleted":
	default:
		v.Add("change", fmt.Sprintf("must be added, modified, or deleted; got %q", fc.Change))
	}
	switch fc.Origin {
	case contracts.OriginToolUse, contracts.OriginCommit, contracts.OriginObserved:
	default:
		v.Add("origin", fmt.Sprintf("unknown origin %q", fc.Origin))
	}
	if !v.Empty() {
		return &v
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, fc.SessionID)
		if err != nil {
			return err
		}
		fc.Attributed = contracts.AttributedChange(sess.Isolation, fc.Origin)
		if fc.Timestamp.IsZero() {
			fc.Timestamp = time.Now().UTC()
		}
		res, err := tx.Exec(
			`INSERT INTO file_changes (session_id, task_id, repo, path, change, origin, attributed, actor, timestamp)
			 VALUES (?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?)`,
			fc.SessionID, fc.TaskID, fc.Repo, fc.Path, fc.Change, string(fc.Origin),
			boolToInt(fc.Attributed), fc.Actor, fc.Timestamp.Format(time.RFC3339Nano),
		)
		if err != nil {
			return err
		}
		if fc.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"path": fc.Path, "change": fc.Change, "attributed": fc.Attributed,
		})
		return tx.Append(&contracts.Envelope{
			Type: contracts.FileChanged, SessionID: fc.SessionID, TaskID: fc.TaskID,
			Actor: actor, Payload: payload,
		})
	})
}

// FileChanges returns a session's changes in record order.
func (s *Sessions) FileChanges(ctx context.Context, sessionID string) ([]contracts.FileChange, error) {
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT id, session_id, COALESCE(task_id, ''), repo, path, change, origin, attributed, actor, timestamp
		 FROM file_changes WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	var out []contracts.FileChange
	for rows.Next() {
		var (
			fc         contracts.FileChange
			attributed int
			ts         string
		)
		if err := rows.Scan(&fc.ID, &fc.SessionID, &fc.TaskID, &fc.Repo, &fc.Path, &fc.Change,
			&fc.Origin, &attributed, &fc.Actor, &ts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		fc.Attributed = attributed != 0
		if fc.Timestamp, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, fc)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// CommitReport is the input to RecordCommit: what the observing layer knows.
// TrailerTaskID comes from a "Task: <id>" commit-message trailer; heuristic
// candidates come from file-overlap analysis (Session Service, W4.4).
type CommitReport struct {
	Commit           contracts.Commit
	TrailerTaskID    string
	HeuristicTaskIDs []string
}

// RecordCommit stores a commit, resolving its task linkage through the §5
// chain in order: trailer → session's active task → file heuristics → orphan
// flag. Emits commit.created always, commit.linked when a rung resolved.
func (s *Sessions) RecordCommit(ctx context.Context, rep *CommitReport, actor string) (*contracts.Commit, error) {
	c := rep.Commit
	var v contracts.ValidationError
	if c.Hash == "" {
		v.Add("hash", "required")
	}
	if c.Message == "" {
		v.Add("message", "required")
	}
	if !v.Empty() {
		return nil, &v
	}
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, c.SessionID)
		if err != nil {
			return err
		}
		taskExists := func(id string) bool {
			if id == "" {
				return false
			}
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", id).Scan(&n); err != nil {
				return false
			}
			return n == 1
		}

		switch {
		case taskExists(rep.TrailerTaskID):
			c.TaskID, c.LinkedVia = rep.TrailerTaskID, contracts.LinkedViaTrailer
		case taskExists(sess.ActiveTaskID):
			c.TaskID, c.LinkedVia = sess.ActiveTaskID, contracts.LinkedViaSessionContext
		default:
			for _, cand := range rep.HeuristicTaskIDs {
				if taskExists(cand) {
					c.TaskID, c.LinkedVia = cand, contracts.LinkedViaHeuristic
					break
				}
			}
		}
		c.Orphan = c.TaskID == ""
		if c.Timestamp.IsZero() {
			c.Timestamp = time.Now().UTC()
		}

		if _, err := tx.Exec(
			`INSERT INTO commits (hash, session_id, task_id, repo, message, author, linked_via, orphan, timestamp)
			 VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
			c.Hash, c.SessionID, c.TaskID, c.Repo, c.Message, c.Author,
			c.LinkedVia, boolToInt(c.Orphan), c.Timestamp.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("commits: insert %s: %w", c.Hash, err)
		}
		payload, _ := json.Marshal(map[string]any{"hash": c.Hash, "orphan": c.Orphan})
		if err := tx.Append(&contracts.Envelope{
			Type: contracts.CommitCreated, SessionID: c.SessionID, TaskID: c.TaskID,
			Actor: actor, Payload: payload,
		}); err != nil {
			return err
		}
		if !c.Orphan {
			linkPayload, _ := json.Marshal(map[string]string{"hash": c.Hash, "via": c.LinkedVia})
			return tx.Append(&contracts.Envelope{
				Type: contracts.CommitLinked, SessionID: c.SessionID, TaskID: c.TaskID,
				Actor: actor, Payload: linkPayload,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Commits returns a session's commits, oldest first.
func (s *Sessions) Commits(ctx context.Context, sessionID string) ([]contracts.Commit, error) {
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT hash, session_id, COALESCE(task_id, ''), repo, message, author, linked_via, orphan, timestamp
		 FROM commits WHERE session_id = ? ORDER BY timestamp, hash`, sessionID)
	if err != nil {
		return nil, err
	}
	var out []contracts.Commit
	for rows.Next() {
		var (
			c      contracts.Commit
			orphan int
			ts     string
		)
		if err := rows.Scan(&c.Hash, &c.SessionID, &c.TaskID, &c.Repo, &c.Message, &c.Author,
			&c.LinkedVia, &orphan, &ts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.Orphan = orphan != 0
		if c.Timestamp, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

func (s *Sessions) getInTx(tx *Tx, id string) (*contracts.Session, error) {
	var data string
	err := tx.QueryRow("SELECT data FROM sessions WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sessions: %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var sess contracts.Session
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Sessions) write(tx *Tx, sess *contracts.Session) error {
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO sessions (id, activity, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   activity = excluded.activity, data = excluded.data, updated_at = excluded.updated_at`,
		sess.ID, string(sess.Activity), string(data),
		sess.CreatedAt.Format(time.RFC3339Nano), sess.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}
