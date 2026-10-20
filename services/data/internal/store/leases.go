package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// LeaseHeldError reports a write, claim or release rejected because a live
// lease exists and the caller didn't prove it holds it (no token, or the
// wrong one).
type LeaseHeldError struct {
	TaskID string
	Holder string
}

func (e *LeaseHeldError) Error() string {
	return fmt.Sprintf("task %s is leased by %q", e.TaskID, e.Holder)
}

// ErrLeaseLost is Renew's only refusal: the lease is gone, expired, or
// belongs to a different token. The caller no longer holds the task and
// must stop writing to it.
var ErrLeaseLost = errors.New("lease lost")

// Leases implements the write-lease model (spec §15,
// docs/PIPELINE-LEASES-DESIGN.md Part A). Claim to get a token, write
// with it (each write renews), Renew when idle, release with it when done.
// A lease means "this process is working on this task right now"; it is
// not the task's progress, so losing one (the TTL sweep, ReleaseAll,
// Reclaim) deletes the lease and never changes the task's status. An
// in_progress task with no live lease is Interrupted, a pipeline concept.
type Leases struct {
	log *Log
	// now is the clock, swappable in tests.
	now func() time.Time
}

// NewLeases wraps the event log's database.
func NewLeases(log *Log) *Leases {
	return &Leases{log: log, now: func() time.Time { return time.Now().UTC() }}
}

type leaseTokenKey struct{}

// WithLeaseToken attaches a request's lease token (the X-Mads-Lease
// header) to ctx. Every transaction opened with that ctx carries it, and
// checkAndRenew uses it to authorise writes to a leased task.
func WithLeaseToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, leaseTokenKey{}, token)
}

func leaseTokenFrom(ctx context.Context) string {
	tok, _ := ctx.Value(leaseTokenKey{}).(string)
	return tok
}

// ClaimRequest is one claim. Actor is the caller's X-Mads-Actor; Holder,
// when set, must equal Actor or begin with "<Actor>:" (e.g.
// "pipeline:run_3f9a"). Empty Holder means Holder = Actor. Empty Actor
// (an in-process caller with no request) skips the prefix rule.
type ClaimRequest struct {
	Actor  string
	Holder string
	TTL    time.Duration
	// Identity, when non-nil, is recorded so a future engine launch can
	// prove this exact holder died (spec: reap-on-launch).
	Identity *contracts.LeaseIdentity
}

// Claim takes the lease on a free task for holder (which is also the
// actor), with no identity recorded.
func (s *Leases) Claim(ctx context.Context, taskID, holder string, ttl time.Duration) (*contracts.Lease, error) {
	return s.ClaimWith(ctx, taskID, ClaimRequest{Actor: holder, Holder: holder, TTL: ttl})
}

// ClaimWith takes the lease on a task. It succeeds only when the task has
// no live lease, whoever holds it (the same holder included: renewing is
// Renew's job, and a claim that could renew would let a process silently
// re-take a lease it lost). Claiming over an expired lease replaces it.
// The returned lease carries a fresh random token; the store keeps only
// its SHA-256.
func (s *Leases) ClaimWith(ctx context.Context, taskID string, req ClaimRequest) (*contracts.Lease, error) {
	holder := req.Holder
	if holder == "" {
		holder = req.Actor
	}
	if holder == "" {
		verr := &contracts.ValidationError{}
		verr.Add("holder", "required")
		return nil, verr
	}
	if req.Actor != "" && !holderMatchesActor(holder, req.Actor) {
		verr := &contracts.ValidationError{}
		verr.Add("holder", fmt.Sprintf("must be %q or start with %q", req.Actor, req.Actor+":"))
		return nil, verr
	}
	token, hash, err := newLeaseToken()
	if err != nil {
		return nil, err
	}
	var lease *contracts.Lease
	err = s.log.WithTx(ctx, func(tx *Tx) error {
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
			if now.Before(expires) {
				return &LeaseHeldError{TaskID: taskID, Holder: curHolder}
			}
		}
		l := &contracts.Lease{
			TaskID: taskID, Holder: holder, TTL: req.TTL.Milliseconds(),
			ClaimedAt: now, ExpiresAt: now.Add(req.TTL), Identity: req.Identity,
		}
		var machineID, bootID sql.NullString
		var pid, pidStartTicks sql.NullInt64
		if id := req.Identity; id != nil {
			machineID = sql.NullString{String: id.MachineID, Valid: true}
			bootID = sql.NullString{String: id.BootID, Valid: true}
			pid = sql.NullInt64{Int64: int64(id.PID), Valid: true}
			pidStartTicks = sql.NullInt64{Int64: id.PIDStartTicks, Valid: true}
		}
		if _, err := tx.Exec(
			`INSERT INTO task_leases (task_id, holder, ttl_ms, claimed_at, expires_at, machine_id, boot_id, pid, pid_start_ticks, token_hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (task_id) DO UPDATE SET
			   holder = excluded.holder, ttl_ms = excluded.ttl_ms,
			   claimed_at = excluded.claimed_at, expires_at = excluded.expires_at,
			   machine_id = excluded.machine_id, boot_id = excluded.boot_id,
			   pid = excluded.pid, pid_start_ticks = excluded.pid_start_ticks,
			   token_hash = excluded.token_hash`,
			l.TaskID, l.Holder, l.TTL,
			l.ClaimedAt.Format(time.RFC3339Nano), l.ExpiresAt.Format(time.RFC3339Nano),
			machineID, bootID, pid, pidStartTicks, hash,
		); err != nil {
			return err
		}
		lease = l
		return nil
	})
	if err != nil {
		return nil, err
	}
	lease.Token = token
	return lease, nil
}

// Renew extends a live lease by its TTL, only for the token that claimed
// it. A missing, expired or differently-held lease returns ErrLeaseLost.
// It never creates a lease: a process that lost its lease must claim
// again (and can only if nobody else has).
func (s *Leases) Renew(ctx context.Context, taskID, token string) (*contracts.Lease, error) {
	var lease *contracts.Lease
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		var (
			l                    contracts.Lease
			claimedAt, expiresAt string
			hash                 sql.NullString
		)
		err := tx.QueryRow("SELECT holder, ttl_ms, claimed_at, expires_at, token_hash FROM task_leases WHERE task_id = ?", taskID).
			Scan(&l.Holder, &l.TTL, &claimedAt, &expiresAt, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
		expires, err := time.Parse(time.RFC3339Nano, expiresAt)
		if err != nil {
			return err
		}
		now := s.now()
		if !now.Before(expires) || !tokenMatches(hash, token) {
			return ErrLeaseLost
		}
		if l.ClaimedAt, err = time.Parse(time.RFC3339Nano, claimedAt); err != nil {
			return err
		}
		l.TaskID = taskID
		l.ExpiresAt = now.Add(time.Duration(l.TTL) * time.Millisecond)
		if _, err := tx.Exec("UPDATE task_leases SET expires_at = ? WHERE task_id = ?",
			l.ExpiresAt.Format(time.RFC3339Nano), taskID); err != nil {
			return err
		}
		lease = &l
		return nil
	})
	return lease, err
}

// Release drops a lease for the token that claimed it. A wrong or missing
// token is a LeaseHeldError; releasing an already-free task is a no-op
// (idempotent for retries).
func (s *Leases) Release(ctx context.Context, taskID, token string) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		var (
			curHolder string
			hash      sql.NullString
		)
		err := tx.QueryRow("SELECT holder, token_hash FROM task_leases WHERE task_id = ?", taskID).Scan(&curHolder, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !tokenMatches(hash, token) {
			return &LeaseHeldError{TaskID: taskID, Holder: curHolder}
		}
		_, err = tx.Exec("DELETE FROM task_leases WHERE task_id = ?", taskID)
		return err
	})
}

// ReleaseAll drops every lease a holder has, tokenless: the supervision
// path (spec §15). When the Router sees a child exit, it releases that
// child's leases immediately instead of waiting for the meters to run
// out. Task status is untouched: an in_progress task whose lease goes is
// Interrupted, and the next run picks it up.
func (s *Leases) ReleaseAll(ctx context.Context, holder, reason string) (int, error) {
	var ids []string
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		rows, err := tx.Query("SELECT task_id FROM task_leases WHERE holder = ?", holder)
		if err != nil {
			return err
		}
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
		_, err = tx.Exec("DELETE FROM task_leases WHERE holder = ?", holder)
		return err
	})
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		log.Printf("leases: released %s held by %s: %s", id, holder, reason)
	}
	return len(ids), nil
}

// ExpireStale is the meter reader: it deletes every expired lease. Task
// status is untouched (see ReleaseAll). Run periodically; it is cheap
// (indexed on expires_at) and a no-op when nothing expired.
func (s *Leases) ExpireStale(ctx context.Context) (int, error) {
	type stale struct{ id, holder string }
	var stales []stale
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		now := s.now().Format(time.RFC3339Nano)
		rows, err := tx.Query("SELECT task_id, holder FROM task_leases WHERE expires_at < ?", now)
		if err != nil {
			return err
		}
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
		_, err = tx.Exec("DELETE FROM task_leases WHERE expires_at < ?", now)
		return err
	})
	if err != nil {
		return 0, err
	}
	for _, st := range stales {
		log.Printf("leases: expired %s: %s stopped renewing", st.id, st.holder)
	}
	return len(stales), nil
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

const leaseColumns = `task_id, holder, ttl_ms, claimed_at, expires_at, machine_id, boot_id, pid, pid_start_ticks`

// ByHolder lists every lease currently held by exactly holder. Identity is
// set on a returned lease only when all four identity columns are
// non-NULL; partial data is not an identity, same "all or nothing" as the
// write side.
func (s *Leases) ByHolder(ctx context.Context, holder string) ([]contracts.Lease, error) {
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT `+leaseColumns+` FROM task_leases WHERE holder = ?`, holder)
	if err != nil {
		return nil, err
	}
	return scanLeases(rows)
}

// ByKind lists every live lease whose holder is of kind: the holder is
// kind itself or starts with "<kind>:" (e.g. every "pipeline:<runID>").
// This is the reap-on-launch candidate set: a starting engine sees every
// pipeline run's leases, not just its own.
func (s *Leases) ByKind(ctx context.Context, kind string) ([]contracts.Lease, error) {
	prefix := kind + ":"
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT `+leaseColumns+` FROM task_leases
		 WHERE (holder = ? OR substr(holder, 1, ?) = ?) AND expires_at > ?`,
		kind, len(prefix), prefix, s.now().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	return scanLeases(rows)
}

func scanLeases(rows *sql.Rows) ([]contracts.Lease, error) {
	defer func() { _ = rows.Close() }()
	var leases []contracts.Lease
	for rows.Next() {
		var (
			l                    contracts.Lease
			claimedAt, expiresAt string
			machineID, bootID    sql.NullString
			pid, pidStartTicks   sql.NullInt64
			err                  error
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

// Reclaim is the proof-or-nothing transition (spec: reap-on-launch),
// tokenless because the dead process's token died with it. The caller
// (whose actor must be the lease holder's kind, see HolderKind) has
// already established, via pkg/procid, that identity's process is
// provably dead. This is the compare-and-swap that makes the transition
// safe: if the lease has moved on since that proof was established
// (renewed — impossible if truly dead, but also covers a second engine
// racing to reclaim the same dead lease first), reclaimed is false and
// nothing changes. On match the lease is deleted and the evidence logged.
// Task status is untouched: an in_progress task becomes Interrupted, and
// the next run picks it up where it stopped.
func (s *Leases) Reclaim(ctx context.Context, taskID, actor string, identity contracts.LeaseIdentity, evidence string) (bool, error) {
	reclaimed := false
	var holder string
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		var (
			machineID, bootID  sql.NullString
			pid, pidStartTicks sql.NullInt64
		)
		err := tx.QueryRow(
			"SELECT holder, machine_id, boot_id, pid, pid_start_ticks FROM task_leases WHERE task_id = ?", taskID,
		).Scan(&holder, &machineID, &bootID, &pid, &pidStartTicks)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // already gone: safe no-op
		}
		if err != nil {
			return err
		}
		if HolderKind(holder) != actor ||
			!machineID.Valid || machineID.String != identity.MachineID ||
			!bootID.Valid || bootID.String != identity.BootID ||
			!pid.Valid || int(pid.Int64) != identity.PID ||
			!pidStartTicks.Valid || pidStartTicks.Int64 != identity.PIDStartTicks {
			return nil // another kind's lease, or it moved on since proof was established
		}
		if _, err := tx.Exec("DELETE FROM task_leases WHERE task_id = ?", taskID); err != nil {
			return err
		}
		reclaimed = true
		return nil
	})
	if err == nil && reclaimed {
		log.Printf("leases: reclaimed %s held by %s: %s", taskID, holder, evidence)
	}
	return reclaimed, err
}

// HolderKind is a lease holder's kind: the part before the first ':'
// ("pipeline" for "pipeline:run_3f9a"), or the whole holder when it has
// no colon.
func HolderKind(holder string) string {
	kind, _, _ := strings.Cut(holder, ":")
	return kind
}

// holderMatchesActor is the claim-time prefix rule: a holder must be the
// actor itself or "<actor>:<something>". It stops accidental cross-kind
// claims; it is not a security boundary (the actor is self-declared).
func holderMatchesActor(holder, actor string) bool {
	return holder == actor || (strings.HasPrefix(holder, actor+":") && len(holder) > len(actor)+1)
}

// newLeaseToken returns a random 128-bit token (base64url) and the hex
// SHA-256 the store keeps in its place.
func newLeaseToken() (token, hash string, err error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("leases: token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, hashLeaseToken(token), nil
}

func hashLeaseToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenMatches reports whether token is the one whose hash is stored. A
// NULL hash or an empty token never matches.
func tokenMatches(stored sql.NullString, token string) bool {
	if !stored.Valid || stored.String == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored.String), []byte(hashLeaseToken(token))) == 1
}

// checkAndRenew enforces only-the-token-holder-may-write inside a task
// mutation and renews the lease as a side effect of the write (spec §15).
// The proof is the request's lease token (tx.leaseToken, from
// WithLeaseToken), not the actor: attribution and authorisation are
// separate. Free tasks (no live lease) are writable by anyone.
func checkAndRenew(tx *Tx, taskID string, now time.Time) error {
	var (
		holder     string
		ttlMs      int64
		expiresRaw string
		hash       sql.NullString
	)
	err := tx.QueryRow("SELECT holder, ttl_ms, expires_at, token_hash FROM task_leases WHERE task_id = ?", taskID).
		Scan(&holder, &ttlMs, &expiresRaw, &hash)
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
	if !tokenMatches(hash, tx.leaseToken) {
		return &LeaseHeldError{TaskID: taskID, Holder: holder}
	}
	_, err = tx.Exec("UPDATE task_leases SET expires_at = ? WHERE task_id = ?",
		now.Add(time.Duration(ttlMs)*time.Millisecond).Format(time.RFC3339Nano), taskID)
	return err
}
