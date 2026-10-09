package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Delegates is the delegate store (docs/DELEGATES-DESIGN.md): one record per
// harness-run subagent, keyed by (session, harness id). The session runner is
// the only writer — it owns the lifecycle and puts whole records; this store
// keeps identity (id, created_at) and announces each real change as
// session.delegate_changed in the same transaction.
type Delegates struct {
	log *Log
}

// NewDelegates wires the delegate store to the event log.
func NewDelegates(log *Log) *Delegates {
	return &Delegates{log: log}
}

func validateDelegate(d *contracts.Delegate) *contracts.ValidationError {
	var v contracts.ValidationError
	if d.SessionID == "" {
		v.Add("session_id", "required")
	}
	if d.HarnessID == "" {
		v.Add("harness_id", "required")
	}
	if !d.State.Valid() {
		v.Add("state", fmt.Sprintf("unknown state %q", d.State))
	}
	if d.StartedAt.IsZero() {
		v.Add("started_at", "required")
	}
	if v.Empty() {
		return nil
	}
	return &v
}

// Put creates or replaces the record for (d.SessionID, d.HarnessID). The
// store owns ID and CreatedAt (kept from an existing row); everything else is
// the caller's. A put that changes nothing is a silent success — no write, no
// event — so the runner can retry or re-publish freely. *d is overwritten
// with the stored record.
func (s *Delegates) Put(ctx context.Context, d *contracts.Delegate, actor string) error {
	if verr := validateDelegate(d); verr != nil {
		return verr
	}
	// UTC before anything compares or encodes: the same instant in another
	// zone encodes differently, and a retry must still read as unchanged.
	d.StartedAt = d.StartedAt.UTC()
	if d.EndedAt != nil {
		ended := d.EndedAt.UTC()
		d.EndedAt = &ended
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM sessions WHERE id = ?`, d.SessionID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("delegates: session %s: %w", d.SessionID, ErrNotFound)
			}
			return err
		}
		old, err := scanDelegate(tx.QueryRow(
			`SELECT data FROM delegates WHERE session_id = ? AND harness_id = ?`, d.SessionID, d.HarnessID))
		switch {
		case errors.Is(err, ErrNotFound):
			d.ID = NewID("dlg")
			d.CreatedAt = time.Now().UTC()
		case err != nil:
			return err
		default:
			d.ID, d.CreatedAt = old.ID, old.CreatedAt
			d.UpdatedAt = old.UpdatedAt
			if sameDelegate(old, d) {
				*d = *old
				return nil
			}
		}
		d.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO delegates (id, session_id, harness_id, state, started_at, data, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (session_id, harness_id) DO UPDATE SET
			   state = excluded.state, started_at = excluded.started_at,
			   data = excluded.data, updated_at = excluded.updated_at`,
			d.ID, d.SessionID, d.HarnessID, string(d.State), d.StartedAt.UTC().Format(time.RFC3339Nano),
			string(data), d.CreatedAt.Format(time.RFC3339Nano), d.UpdatedAt.Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{
			Type: contracts.SessionDelegateChanged, SessionID: d.SessionID, Actor: actor, Payload: data,
		})
	})
}

// sameDelegate compares two records by their JSON encoding, which is the
// shape that is stored and announced — and sidesteps time.Time's location
// and monotonic-clock fields, which differ between a decoded record and a
// freshly built one without the instant differing.
func sameDelegate(a, b *contracts.Delegate) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// ForSession returns the session's delegates, oldest first.
func (s *Delegates) ForSession(ctx context.Context, sessionID string) ([]contracts.Delegate, error) {
	return s.query(ctx, `SELECT data FROM delegates WHERE session_id = ? ORDER BY created_at`, sessionID)
}

// List returns every delegate, oldest first — only those in state when it is
// non-empty. The session rail's cold read is List("running"): one call for
// every session's live delegates, never one per card.
func (s *Delegates) List(ctx context.Context, state contracts.DelegateState) ([]contracts.Delegate, error) {
	if state == "" {
		return s.query(ctx, `SELECT data FROM delegates ORDER BY created_at`)
	}
	if !state.Valid() {
		var v contracts.ValidationError
		v.Add("state", fmt.Sprintf("unknown state %q", state))
		return nil, &v
	}
	return s.query(ctx, `SELECT data FROM delegates WHERE state = ? ORDER BY created_at`, string(state))
}

// LiveOrEndedAfter lists every running delegate plus every finished one that
// ended at or after t, oldest first — the session rail's cold read under a
// fade window (Config.DelegateFade). Filtered here rather than in SQL: the
// end time lives inside the JSON document, where its RFC 3339 form (a
// variable number of fractional digits) doesn't sort as text.
func (s *Delegates) LiveOrEndedAfter(ctx context.Context, t time.Time) ([]contracts.Delegate, error) {
	all, err := s.query(ctx, `SELECT data FROM delegates ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	out := []contracts.Delegate{}
	for _, d := range all {
		if d.State == contracts.DelegateRunning || (d.EndedAt != nil && !d.EndedAt.Before(t)) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *Delegates) query(ctx context.Context, q string, args ...any) ([]contracts.Delegate, error) {
	rows, err := s.log.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := []contracts.Delegate{}
	for rows.Next() {
		d, err := scanDelegateRow(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, *d)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

func scanDelegate(row rowScanner) (*contracts.Delegate, error) {
	d, err := scanDelegateRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("delegates: not found: %w", ErrNotFound)
	}
	return d, err
}

func scanDelegateRow(row rowScanner) (*contracts.Delegate, error) {
	var data string
	if err := row.Scan(&data); err != nil {
		return nil, err
	}
	var d contracts.Delegate
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return nil, err
	}
	return &d, nil
}
