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

// Claim takes (or renews) the lease on a task, with no identity recorded
// (pre-reap-on-launch behavior — used by callers that don't carry a
// pkg/procid identity, e.g. tests exercising a plain/foreign claim).
func (s *Leases) Claim(ctx context.Context, taskID, holder string, ttl time.Duration) (*contracts.Lease, error) {
	return s.ClaimWithIdentity(ctx, taskID, holder, ttl, nil)
}

// ClaimWithIdentity takes (or renews) the lease on a task. Re-claiming as
// the current holder extends the meter; claiming over an expired lease
// succeeds and replaces it; claiming over a live lease held by someone else
// returns LeaseHeldError. identity, when non-nil, is recorded so a future
// engine launch can prove this exact holder died (spec: reap-on-launch);
// nil records no identity — same as a pre-migration row, never provable.
func (s *Leases) ClaimWithIdentity(ctx context.Context, taskID, holder string, ttl time.Duration, identity *contracts.LeaseIdentity) (*contracts.Lease, error) {
	if holder == "" {
		verr := &contracts.ValidationError{}
		verr.Add("holder", "required")
		return nil, verr
	}
	var lease *contracts.Lease
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
		l := &contracts.Lease{
			TaskID: taskID, Holder: holder, TTL: ttl.Milliseconds(),
			ClaimedAt: now, ExpiresAt: now.Add(ttl), Identity: identity,
		}
		var machineID, bootID sql.NullString
		var pid, pidStartTicks sql.NullInt64
		if identity != nil {
			machineID = sql.NullString{String: identity.MachineID, Valid: true}
			bootID = sql.NullString{String: identity.BootID, Valid: true}
			pid = sql.NullInt64{Int64: int64(identity.PID), Valid: true}
			pidStartTicks = sql.NullInt64{Int64: identity.PIDStartTicks, Valid: true}
		}
		if _, err := tx.Exec(
			`INSERT INTO task_leases (task_id, holder, ttl_ms, claimed_at, expires_at, machine_id, boot_id, pid, pid_start_ticks)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (task_id) DO UPDATE SET
			   holder = excluded.holder, ttl_ms = excluded.ttl_ms,
			   claimed_at = excluded.claimed_at, expires_at = excluded.expires_at,
			   machine_id = excluded.machine_id, boot_id = excluded.boot_id,
			   pid = excluded.pid, pid_start_ticks = excluded.pid_start_ticks`,
			l.TaskID, l.Holder, l.TTL,
			l.ClaimedAt.Format(time.RFC3339Nano), l.ExpiresAt.Format(time.RFC3339Nano),
			machineID, bootID, pid, pidStartTicks,
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

// ByHolder lists every live lease currently held by holder — the reap-on-
// launch candidate set (spec: reap-on-launch). Identity is set on a
// returned lease only when all four identity columns are non-NULL;
// partial data is not an identity, same "all or nothing" as the write side.
func (s *Leases) ByHolder(ctx context.Context, holder string) ([]contracts.Lease, error) {
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT task_id, holder, ttl_ms, claimed_at, expires_at, machine_id, boot_id, pid, pid_start_ticks
		 FROM task_leases WHERE holder = ?`, holder)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var leases []contracts.Lease
	for rows.Next() {
		var (
			l                    contracts.Lease
			claimedAt, expiresAt string
			machineID, bootID    sql.NullString
			pid, pidStartTicks   sql.NullInt64
		)
		if err := rows.Scan(&l.TaskID, &l.Holder, &l.TTL, &claimedAt, &expiresAt,
			&machineID, &bootID, &pid, &pidStartTicks); err != nil {
			return nil, err
		}
		if l.ClaimedAt, err = time.Parse(time.RFC3339Nano, claimedAt); err != nil {
			return nil, err
		}
		if l.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
			return nil, err
		}
		if machineID.Valid && bootID.Valid && pid.Valid && pidStartTicks.Valid {
			l.Identity = &contracts.LeaseIdentity{
				MachineID: machineID.String, BootID: bootID.String,
				PID: int(pid.Int64), PIDStartTicks: pidStartTicks.Int64,
			}
		}
		leases = append(leases, l)
	}
	return leases, rows.Err()
}

// Reclaim is the proof-or-nothing transition (spec: reap-on-launch): the
// caller has already established, via pkg/procid, that identity's process
// is provably dead. This is the compare-and-swap that makes the transition
// safe: if the lease has moved on since that proof was established (renewed
// — impossible if truly dead, but also covers a second engine racing to
// reclaim the same dead lease first), reclaimed is false and nothing
// changes. On match: the lease is released and, if the task is still
// in_progress, requeued to ready with an evidence event (same event type
// as the TTL reaper's blocked-transition, distinguished by its "to" and
// "reason").
func (s *Leases) Reclaim(ctx context.Context, taskID, holder string, identity contracts.LeaseIdentity, evidence string) (bool, error) {
	reclaimed := false
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		var (
			curHolder          string
			machineID, bootID  sql.NullString
			pid, pidStartTicks sql.NullInt64
		)
		err := tx.QueryRow(
			"SELECT holder, machine_id, boot_id, pid, pid_start_ticks FROM task_leases WHERE task_id = ?", taskID,
		).Scan(&curHolder, &machineID, &bootID, &pid, &pidStartTicks)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // already gone: safe no-op
		}
		if err != nil {
			return err
		}
		if curHolder != holder ||
			!machineID.Valid || machineID.String != identity.MachineID ||
			!bootID.Valid || bootID.String != identity.BootID ||
			!pid.Valid || int(pid.Int64) != identity.PID ||
			!pidStartTicks.Valid || pidStartTicks.Int64 != identity.PIDStartTicks {
			return nil // the lease moved on since proof was established
		}
		if _, err := tx.Exec("DELETE FROM task_leases WHERE task_id = ?", taskID); err != nil {
			return err
		}
		reclaimed = true

		var sessionID, status string
		err = tx.QueryRow("SELECT session_id, status FROM tasks WHERE id = ?", taskID).Scan(&sessionID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // task deleted out from under the lease
		}
		if err != nil {
			return err
		}
		if status != string(contracts.StatusInProgress) {
			return nil
		}
		if _, err := tx.Exec("UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?",
			string(contracts.StatusReady), s.now().Format(time.RFC3339Nano), taskID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{
			"from": status, "to": string(contracts.StatusReady), "reason": evidence,
		})
		return tx.Append(&contracts.Envelope{
			Type: contracts.TaskStatusChanged, SessionID: sessionID, TaskID: taskID,
			Actor: "system", Payload: payload,
		})
	})
	return reclaimed, err
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
