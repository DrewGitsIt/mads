package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// LeaseHeldError reports a write or claim rejected because another holder's
// lease is active.
type LeaseHeldError struct {
	TaskID string
	Holder string
}

func (e *LeaseHeldError) Error() string {
	return fmt.Sprintf("task %s is leased by %q", e.TaskID, e.Holder)
}

// Lease is the current claim on a task.
type Lease struct {
	TaskID    string    `json:"task_id"`
	Holder    string    `json:"holder"`
	TTL       int64     `json:"ttl_ms"`
	ClaimedAt time.Time `json:"claimed_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Leases implements the write-lease model (spec §15). Claim before mutating,
// write freely (each write renews), release when done. A holder that dies
// silently just stops renewing: the sweep releases the lease, marks the task
// blocked, and announces it.
type Leases struct {
	log *Log
	// now is the clock, swappable in tests.
	now func() time.Time
}

// NewLeases wraps the event log's database.
func NewLeases(log *Log) *Leases {
	return &Leases{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// Claim takes (or renews) the lease on a task. Re-claiming as the current
// holder extends the meter; claiming over an expired lease succeeds and
// replaces it; claiming over a live lease held by someone else returns
// LeaseHeldError.
func (s *Leases) Claim(ctx context.Context, taskID, holder string, ttl time.Duration) (*Lease, error) {
	if holder == "" {
		verr := &contracts.ValidationError{}
		verr.Add("holder", "required")
		return nil, verr
	}
	var lease *Lease
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("leases: task %s: %w", taskID, ErrNotFound)
		}
		now := s.now()
		var (
			curHolder  string
			curExpires string
		)
		err := tx.QueryRow("SELECT holder, expires_at FROM task_leases WHERE task_id = ?", taskID).
			Scan(&curHolder, &curExpires)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// free to claim
		case err != nil:
			return err
		default:
			expires, perr := time.Parse(time.RFC3339Nano, curExpires)
			if perr != nil {
				return perr
			}
			if curHolder != holder && now.Before(expires) {
				return &LeaseHeldError{TaskID: taskID, Holder: curHolder}
			}
		}
		l := &Lease{
			TaskID: taskID, Holder: holder, TTL: ttl.Milliseconds(),
			ClaimedAt: now, ExpiresAt: now.Add(ttl),
		}
		if _, err := tx.Exec(
			`INSERT INTO task_leases (task_id, holder, ttl_ms, claimed_at, expires_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (task_id) DO UPDATE SET
			   holder = excluded.holder, ttl_ms = excluded.ttl_ms,
			   claimed_at = excluded.claimed_at, expires_at = excluded.expires_at`,
			l.TaskID, l.Holder, l.TTL,
			l.ClaimedAt.Format(time.RFC3339Nano), l.ExpiresAt.Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
		lease = l
		return nil
	})
	return lease, err
}

// Release drops the holder's lease. Releasing a lease you don't hold is an
// error; releasing an already-free task is a no-op (idempotent for retries).
func (s *Leases) Release(ctx context.Context, taskID, holder string) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		var curHolder string
		err := tx.QueryRow("SELECT holder FROM task_leases WHERE task_id = ?", taskID).Scan(&curHolder)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if curHolder != holder {
			return &LeaseHeldError{TaskID: taskID, Holder: curHolder}
		}
		_, err = tx.Exec("DELETE FROM task_leases WHERE task_id = ?", taskID)
		return err
	})
}

// ReleaseAll drops every lease a holder has — the supervision path (spec §15):
// when the Router sees a child exit, it releases that child's leases
// immediately instead of waiting for the meters to run out. Each task that
// was mid-flight (in_progress) is marked blocked with a task.status_changed
// event explaining why.
func (s *Leases) ReleaseAll(ctx context.Context, holder, reason string) (int, error) {
	released := 0
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		rows, err := tx.tx.Query("SELECT task_id FROM task_leases WHERE holder = ?", holder)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.releaseAndBlock(tx, id, reason); err != nil {
				return err
			}
		}
		released = len(ids)
		return nil
	})
	return released, err
}

// ExpireStale is the meter reader: it releases every expired lease, blocking
// mid-flight tasks with an announcement. Run periodically; it is cheap
// (indexed on expires_at) and a no-op when nothing expired.
func (s *Leases) ExpireStale(ctx context.Context) (int, error) {
	expired := 0
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		now := s.now().Format(time.RFC3339Nano)
		rows, err := tx.tx.Query("SELECT task_id, holder FROM task_leases WHERE expires_at < ?", now)
		if err != nil {
			return err
		}
		type stale struct{ id, holder string }
		var stales []stale
		for rows.Next() {
			var st stale
			if err := rows.Scan(&st.id, &st.holder); err != nil {
				_ = rows.Close()
				return err
			}
			stales = append(stales, st)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, st := range stales {
			reason := fmt.Sprintf("lease expired: %s stopped renewing", st.holder)
			if err := s.releaseAndBlock(tx, st.id, reason); err != nil {
				return err
			}
		}
		expired = len(stales)
		return nil
	})
	return expired, err
}

// releaseAndBlock removes a lease and, if its task was mid-flight, marks it
// blocked and announces the change.
func (s *Leases) releaseAndBlock(tx *Tx, taskID, reason string) error {
	if _, err := tx.Exec("DELETE FROM task_leases WHERE task_id = ?", taskID); err != nil {
		return err
	}
	var sessionID, status string
	err := tx.QueryRow("SELECT session_id, status FROM tasks WHERE id = ?", taskID).Scan(&sessionID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // task deleted out from under the lease; nothing to block
	}
	if err != nil {
		return err
	}
	if status != string(contracts.StatusInProgress) {
		return nil
	}
	if _, err := tx.Exec("UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?",
		string(contracts.StatusBlocked), s.now().Format(time.RFC3339Nano), taskID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{
		"from": status, "to": string(contracts.StatusBlocked), "reason": reason,
	})
	return tx.Append(&contracts.Envelope{
		Type: contracts.TaskStatusChanged, SessionID: sessionID, TaskID: taskID,
		Actor: "system", Payload: payload,
	})
}

// ActiveForSession reports whether any task under sessionID currently has
// an unexpired write-lease — the signal that a live engine (an interactive
// child, or a CLI `mads run` with no other visible liveness signal) is
// working under this session right now, independent of which actor string
// holds the lease. The heartbeat in services/pipeline/engine/engine.go
// keeps this honest even when a task is silent for its whole run: writes
// alone are not a frequent enough signal (see that file's LeaseTTL comment).
func (s *Leases) ActiveForSession(ctx context.Context, sessionID string) (bool, error) {
	var n int
	err := s.log.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_leases tl
		 JOIN tasks t ON t.id = tl.task_id
		 WHERE t.session_id = ? AND tl.expires_at > ?`,
		sessionID, s.now().Format(time.RFC3339Nano),
	).Scan(&n)
	return n > 0, err
}

// checkAndRenew enforces only-holder-may-write inside a task mutation and
// renews the holder's meter as a side effect of the write (spec §15). Free
// tasks (no live lease) are writable by anyone.
func checkAndRenew(tx *Tx, taskID, actor string, now time.Time) error {
	var (
		holder     string
		ttlMs      int64
		expiresRaw string
	)
	err := tx.QueryRow("SELECT holder, ttl_ms, expires_at FROM task_leases WHERE task_id = ?", taskID).
		Scan(&holder, &ttlMs, &expiresRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresRaw)
	if err != nil {
		return err
	}
	if now.After(expires) {
		return nil // expired: the sweep will collect it; the write is free
	}
	if holder != actor {
		return &LeaseHeldError{TaskID: taskID, Holder: holder}
	}
	_, err = tx.Exec("UPDATE task_leases SET expires_at = ? WHERE task_id = ?",
		now.Add(time.Duration(ttlMs)*time.Millisecond).Format(time.RFC3339Nano), taskID)
	return err
}
