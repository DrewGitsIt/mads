package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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

// Create stores a new session (implicit on first message, spec §1) and
// announces it. Activity defaults to active, isolation to worktree.
//
// A non-zero CreatedAt is kept: an imported conversation carries the time it
// was really started, which the rail's age reads. Every other creator leaves
// it zero and gets now. (The Router's POST /sessions clears whatever a client
// sent, so only code that calls the Data Service directly can set it.)
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
	// Derived on read by the Router, never stored (Session.EffectiveTrust).
	sess.EffectiveTrust = ""
	if verr := contracts.ValidateSession(sess); verr != nil {
		return verr
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		if sess.ID == "" {
			sess.ID = NewID("ses")
		}
		now := time.Now().UTC()
		if sess.CreatedAt.IsZero() {
			sess.CreatedAt = now
		}
		sess.UpdatedAt = now
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
	// A record read through the Router comes back decorated; writing it
	// back must not freeze the derivation into the row.
	sess.EffectiveTrust = ""
	if verr := contracts.ValidateSession(sess); verr != nil {
		return verr
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		old, err := s.getInTx(tx, sess.ID)
		if err != nil {
			return err
		}
		sess.CreatedAt = old.CreatedAt
		// ImportedFrom is set once at import; a full-record PUT from a client
		// that predates the field must not erase the dedupe record.
		sess.ImportedFrom = old.ImportedFrom
		// AgentState and LastTurnAt are runner-owned and reach the record
		// only through their own scoped writers below. A full-record PUT
		// carries whatever its client read a round trip earlier, which is
		// exactly how the lost update SetActivity's doc describes bites —
		// so carry both forward rather than letting a rename, a summary
		// write or an activity flip stamp a stale turn state over a live
		// one.
		sess.AgentState = old.AgentState
		sess.LastTurnAt = old.LastTurnAt
		// Usage totals and billing are the same kind: a PUT carrying an
		// older read would roll back every turn counted since it.
		sess.UsageTotals = old.UsageTotals
		sess.Billing = old.Billing
		// The isolation state is the same kind of field, written only
		// through SetWorktree: the counts are recomputed at every turn end,
		// and a rename that read the record before the runner persisted its
		// worktree would otherwise erase it — and with it the only record
		// of where the session's work lives.
		sess.WorktreeDir = old.WorktreeDir
		sess.WorktreeBranch = old.WorktreeBranch
		sess.WorktreeBase = old.WorktreeBase
		sess.UncommittedCount = old.UncommittedCount
		sess.UnlandedCount = old.UnlandedCount
		sess.OutsideCount = old.OutsideCount
		// So is the one bit of Tags the user owns. SessionTagAside reaches
		// the record only through SetAside, and the runner full-PUTs this
		// record at times it chooses — persistProviderRef and the shutdown
		// flip both re-read and write it back — so a send that clears the
		// tag could be undone by a watcher that read the record a moment
		// earlier, putting the session back in the fold the user just took
		// it out of. Only this tag is carried: the rest of Tags is a client
		// field, and the runner's own re-root marker lands through a PUT.
		sess.SetAside(old.IsAside())
		// The rail position is the user's too, for the same reason: only
		// SetRailRank and SetAside write it.
		sess.RailRank = old.RailRank
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

// SetAside adds or clears SessionTagAside in a single transaction, and is
// scoped for the same reason SetActivity is (see its doc): the two callers
// both write while someone else is writing the same record. The rail's ✓ can
// land while the session's own runner is persisting a ProviderRef, and the
// send path clears the tag at the very moment a turn is starting — the
// busiest instant in a session's life for record writes. A
// Session-then-UpdateSession pair would carry a whole snapshot across two
// network calls and drop whatever the other writer landed in the gap.
//
// No-op when already at the target value, so the send path can clear
// unconditionally without a read first: the common case (a session that was
// never set aside) writes nothing and announces nothing.
func (s *Sessions) SetAside(ctx context.Context, id string, aside bool, actor string) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		if sess.IsAside() == aside {
			return nil
		}
		sess.SetAside(aside)
		// Bumped, unlike SetAgentState's: setting a session aside IS a change
		// to its user-visible state, and it is the user who made it.
		now := time.Now().UTC()
		sess.UpdatedAt = now
		if !aside {
			// Coming back from Done is picking the session up again, so it
			// returns to the top of the active list rather than to wherever
			// its start time or an old drag left it (contracts.Session.RailRank).
			rank := float64(now.UnixMilli())
			sess.RailRank = &rank
		}
		if err := s.write(tx, sess); err != nil {
			return err
		}
		// session.updated is what the rail's list subscribes to, so the fold
		// closes (or opens) on every browser watching, not just the one that
		// clicked.
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionUpdated, SessionID: id, Actor: actor,
		})
	})
}

// SetRailRank records where the user dragged a session in the rail's active
// list (contracts.Session.RailRank). Scoped for SetAside's reason: the drop
// can land while the session's runner is writing the same record.
//
// UpdatedAt is left alone, as SetAgentState leaves it: a position in one
// view is not a change to the session, and bumping it would restart the
// archive clock SweepExpiredShadows reads.
func (s *Sessions) SetRailRank(ctx context.Context, id string, rank float64, actor string) error {
	if math.IsNaN(rank) || math.IsInf(rank, 0) {
		var v contracts.ValidationError
		v.Add("rail_rank", "must be a finite number")
		return &v
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		if sess.RailRank != nil && *sess.RailRank == rank {
			return nil
		}
		sess.RailRank = &rank
		if err := s.write(tx, sess); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionUpdated, SessionID: id, Actor: actor,
		})
	})
}

// SetAgentState mirrors one main-agent transition onto the session record
// and appends its session.agent_state event in the same transaction, so a
// cold GET /sessions and the live stream can never report different things
// about the same turn. Scoped for exactly the reason SetActivity is (see its
// doc): the Runner writes this field several times a turn while it is also
// writing ProviderRef and turn-end usage onto the same record, and a
// Get-then-Update round trip would let either side drop the other's field.
//
// UpdatedAt is deliberately left untouched. A tool-label transition is not a
// change to the session's user-visible state, and bumping it would reshuffle
// the updated_at-ordered list several times a turn — and, worse, restart the
// archive clock SweepExpiredShadows reads off it.
func (s *Sessions) SetAgentState(ctx context.Context, id string, state contracts.AgentState, actor string) error {
	if state.State != contracts.AgentWorking && state.State != contracts.AgentIdle {
		var v contracts.ValidationError
		v.Add("state", fmt.Sprintf("must be %q or %q", contracts.AgentWorking, contracts.AgentIdle))
		return &v
	}
	if state.Since.IsZero() {
		state.Since = time.Now().UTC()
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		sess.AgentState = &state
		if err := s.write(tx, sess); err != nil {
			return err
		}
		payload, _ := json.Marshal(state)
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionAgentState, SessionID: id, Actor: actor, Payload: payload,
		})
	})
}

// RecordTurnEnd stamps the end of one turn onto the session record:
// LastTurnAt always, LastUsage too when the provider reported any. Scoped
// like SetActivity rather than the Get-then-Update pair it replaces
// (services/session/internal/runner's persistTurnEnd), whose two round trips
// were open to losing the ProviderRef or agent state a concurrent writer of
// the same record landed in the gap. Nothing else writes LastTurnAt, which
// is the point: rename, summary and activity writes leave it exactly where
// the last turn left it. Emits session.updated, the same announcement that
// round trip produced.
func (s *Sessions) RecordTurnEnd(ctx context.Context, id string, usage *contracts.Usage, at time.Time, actor string) error {
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		sess.LastTurnAt = &at
		if usage != nil {
			sess.LastUsage = usage
		}
		sess.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, sess); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionUpdated, SessionID: id, Actor: actor,
		})
	})
}

// AddUsage folds one turn's usage into the session's running totals
// (contracts.UsageTotals) inside one transaction — a sum can't be written as
// a Get-then-Update round trip without losing whatever another increment
// added in the gap. No event and no UpdatedAt bump: the turn end it rides
// beside already announced the change (RecordTurnEnd's session.updated).
func (s *Sessions) AddUsage(ctx context.Context, id string, inc contracts.UsageIncrement) error {
	if inc.IsZero() {
		return nil
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		if sess.UsageTotals == nil {
			sess.UsageTotals = &contracts.UsageTotals{}
		}
		sess.UsageTotals.Add(inc)
		return s.write(tx, sess)
	})
}

// SetBilling applies the harness's latest billing report: a non-empty Mode
// replaces the mode, a non-nil Limits the rate-limit windows. Scoped for the
// reason SetActivity gives. Like AddUsage it appends nothing: the snapshot
// is read on demand (GET /sessions/{id}/report), and a rate-limit report
// arrives every turn.
func (s *Sessions) SetBilling(ctx context.Context, id string, p contracts.SessionBillingPatch) error {
	switch p.Mode {
	case "", contracts.BillingPlan, contracts.BillingUsage, contracts.BillingUnknown:
	default:
		var v contracts.ValidationError
		v.Add("mode", fmt.Sprintf("must be %q, %q or %q", contracts.BillingPlan, contracts.BillingUsage, contracts.BillingUnknown))
		return &v
	}
	if p.Mode == "" && p.Limits == nil {
		return nil
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		b := contracts.SessionBilling{Mode: contracts.BillingUnknown}
		if sess.Billing != nil {
			b = *sess.Billing
		}
		if p.Mode != "" {
			b.Mode = p.Mode
		}
		if p.Limits != nil {
			at := p.At
			if at.IsZero() {
				at = time.Now()
			}
			at = at.UTC()
			b.Limits, b.LimitsAt = p.Limits, &at
		}
		sess.Billing = &b
		return s.write(tx, sess)
	})
}

// SetWorktree applies a scoped write of a session's isolation state — its
// worktree folder, branch and base, and D4's header counts — inside one
// transaction, for the reason SetActivity gives. Nil fields are left alone.
// Emits session.updated only when something actually changed: the runner
// re-sends its counts at every turn end, and an unchanged count is not news
// to anyone listening. UpdatedAt is left alone for the same reason
// SetAgentState leaves it: a recount isn't a user-visible edit.
func (s *Sessions) SetWorktree(ctx context.Context, id string, p contracts.SessionWorktreePatch, actor string) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, id)
		if err != nil {
			return err
		}
		before := *sess
		setIf(&sess.WorktreeDir, p.WorktreeDir)
		setIf(&sess.WorktreeBranch, p.WorktreeBranch)
		setIf(&sess.WorktreeBase, p.WorktreeBase)
		setIf(&sess.UncommittedCount, p.UncommittedCount)
		setIf(&sess.UnlandedCount, p.UnlandedCount)
		setIf(&sess.OutsideCount, p.OutsideCount)
		if sess.WorktreeDir == before.WorktreeDir && sess.WorktreeBranch == before.WorktreeBranch &&
			sess.WorktreeBase == before.WorktreeBase && sess.UncommittedCount == before.UncommittedCount &&
			sess.UnlandedCount == before.UnlandedCount && sess.OutsideCount == before.OutsideCount {
			return nil
		}
		if err := s.write(tx, sess); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionUpdated, SessionID: id, Actor: actor,
		})
	})
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
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
		// Delegates are the session's own (unlike links, which deletion
		// deliberately leaves dangling): a deleted session's subagents must
		// not linger in the rail's running list.
		if _, err := tx.Exec("DELETE FROM delegates WHERE session_id = ?", id); err != nil {
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
		return insertFileChange(tx, sess, fc, actor)
	})
}

// insertFileChange writes one ledger row, attributed by the §5 rule against
// sess's isolation, and emits file.changed.
func insertFileChange(tx *Tx, sess *contracts.Session, fc *contracts.FileChange, actor string) error {
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
}

// RecordShellFileChanges stores what one scan of a session's folder found
// its shell commands changed, as tool use, in one transaction. A change is
// skipped as contested when another session's tools recorded the same path
// between its Since and Until: in a shared checkout that edit is more likely
// theirs. Timestamps are compared parsed, not as stored strings, since rows
// carry whatever offset their writer used.
func (s *Sessions) RecordShellFileChanges(ctx context.Context, sessionID string, changes []contracts.ShellFileChange, actor string) (contracts.ShellFileChangesResult, error) {
	res := contracts.ShellFileChangesResult{Recorded: []contracts.FileChange{}, Contested: []string{}}
	var v contracts.ValidationError
	for i, c := range changes {
		if c.Path == "" {
			v.Add(fmt.Sprintf("changes[%d].path", i), "required")
		}
		switch c.Change {
		case "added", "modified", "deleted":
		default:
			v.Add(fmt.Sprintf("changes[%d].change", i), fmt.Sprintf("must be added, modified, or deleted; got %q", c.Change))
		}
	}
	if !v.Empty() {
		return res, &v
	}
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		sess, err := s.getInTx(tx, sessionID)
		if err != nil {
			return err
		}
		for _, c := range changes {
			contested, err := claimedByOthers(tx, sessionID, c)
			if err != nil {
				return err
			}
			if contested {
				res.Contested = append(res.Contested, c.Path)
				continue
			}
			fc := contracts.FileChange{
				SessionID: sessionID, Repo: c.Repo, Path: c.Path, Change: c.Change,
				Origin: contracts.OriginToolUse, Actor: actor, Timestamp: c.Timestamp,
			}
			if err := insertFileChange(tx, sess, &fc, actor); err != nil {
				return err
			}
			res.Recorded = append(res.Recorded, fc)
		}
		return nil
	})
	if err != nil {
		return contracts.ShellFileChangesResult{}, err
	}
	return res, nil
}

// claimedByOthers reports whether a session other than sessionID recorded
// c's path as tool use between c.Since and c.Until.
func claimedByOthers(tx *Tx, sessionID string, c contracts.ShellFileChange) (bool, error) {
	rows, err := tx.Query(
		`SELECT timestamp FROM file_changes
		 WHERE path = ? AND session_id != ? AND origin = ? AND task_id IS NULL`,
		c.Path, sessionID, string(contracts.OriginToolUse))
	if err != nil {
		return false, err
	}
	claimed := false
	for !claimed && rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			_ = rows.Close()
			return false, err
		}
		if at, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			claimed = !at.Before(c.Since) && !at.After(c.Until)
		}
	}
	return claimed, errors.Join(rows.Err(), rows.Close())
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
	// Source is set for a commit mads made itself (contracts.CommitSource*),
	// with Files and Drafted describing it. Not stored: they ride on
	// commit.created only, which is what the conversation's cards replay.
	Source  string
	Files   int
	Drafted bool
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
		p := map[string]any{"hash": c.Hash, "orphan": c.Orphan}
		if rep.Source != "" {
			p["source"], p["message"] = rep.Source, c.Message
			if rep.Files > 0 {
				p["files"] = rep.Files
			}
			if rep.Drafted {
				p["drafted"] = true
			}
		}
		payload, _ := json.Marshal(p)
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

// DeleteCommit removes one of sessionID's commit records and appends
// commit.undone — the record side of a Commit the user took back (`git
// reset --soft`), so no card goes on naming a commit no branch holds. A
// hash this session didn't record is ErrNotFound.
func (s *Sessions) DeleteCommit(ctx context.Context, sessionID, hash string, actor string) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(`DELETE FROM commits WHERE session_id = ? AND hash = ?`, sessionID, hash)
		if err != nil {
			return fmt.Errorf("commits: delete %s: %w", hash, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("commits: %s in session %s: %w", hash, sessionID, ErrNotFound)
		}
		payload, _ := json.Marshal(map[string]string{"hash": hash})
		return tx.Append(&contracts.Envelope{
			Type: contracts.CommitUndone, SessionID: sessionID, Actor: actor, Payload: payload,
		})
	})
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
