package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// AccountAuths is the provider-login sign-in store (docs/AUTH-SIGNIN-
// DESIGN.md). Session runners report a send refused for auth (Failed) and a
// turn that got a real answer (OK); the Router reports a finished sign-in
// (OK). account.auth_changed is appended in the same transaction, and only
// when signed-out flips, so a burst of failed sends on one dead login
// announces once.
type AccountAuths struct {
	log *Log
}

// NewAccountAuths wires the account auth store to the event log.
func NewAccountAuths(log *Log) *AccountAuths {
	return &AccountAuths{log: log}
}

// Failed records an auth failure on f.Account, creating the row on the first
// one. A failure older than the login's last sign-in (a report that lost a
// race with the sign-in) is kept as history but doesn't sign it out again.
func (s *AccountAuths) Failed(ctx context.Context, f contracts.AccountAuthFailed, actor string) error {
	var v contracts.ValidationError
	if f.Account == "" {
		v.Add("account", "required")
	}
	if f.Provider == "" {
		v.Add("provider", "required")
	}
	if !v.Empty() {
		return &v
	}
	at := f.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		old, err := scanAccountAuth(tx.QueryRow(accountAuthSelect+` WHERE account = ?`, f.Account))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next := contracts.AccountAuth{Account: f.Account, Provider: f.Provider, FailedAt: at, FailedSession: f.SessionID}
		if old != nil {
			next.SignedInAt = old.SignedInAt
			// Reports can land out of order; the newest failure stands.
			if old.FailedAt.After(at) {
				next.FailedAt, next.FailedSession = old.FailedAt, old.FailedSession
			}
		}
		next.SignedOut = next.FailedAt.After(next.SignedInAt)
		if _, err := tx.Exec(
			`INSERT INTO account_auth (account, provider, failed_at, failed_session, signed_in_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (account) DO UPDATE SET
			   provider = excluded.provider, failed_at = excluded.failed_at,
			   failed_session = excluded.failed_session`,
			next.Account, next.Provider, formatAuthTime(next.FailedAt), next.FailedSession, formatAuthTime(next.SignedInAt),
		); err != nil {
			return err
		}
		if next.SignedOut && (old == nil || !old.SignedOut) {
			return appendAuthChanged(tx, &next, actor)
		}
		return nil
	})
}

// OK records that ok.Account worked at ok.At. It only touches a row that is
// signed out, and only with a time after the failure: the runner reports
// every answered turn, so this is a read and nothing more almost every time.
func (s *AccountAuths) OK(ctx context.Context, ok contracts.AccountAuthOK, actor string) error {
	if ok.Account == "" {
		var v contracts.ValidationError
		v.Add("account", "required")
		return &v
	}
	at := ok.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		cur, err := scanAccountAuth(tx.QueryRow(accountAuthSelect+` WHERE account = ?`, ok.Account))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !cur.SignedOut || !at.After(cur.FailedAt) {
			return nil
		}
		if _, err := tx.Exec(`UPDATE account_auth SET signed_in_at = ? WHERE account = ?`, formatAuthTime(at), cur.Account); err != nil {
			return err
		}
		cur.SignedInAt, cur.SignedOut = at, false
		return appendAuthChanged(tx, cur, actor)
	})
}

// List returns every login that has ever failed auth, by account.
func (s *AccountAuths) List(ctx context.Context) ([]contracts.AccountAuth, error) {
	rows, err := s.log.db.QueryContext(ctx, accountAuthSelect+` ORDER BY account`)
	if err != nil {
		return nil, err
	}
	out := []contracts.AccountAuth{}
	for rows.Next() {
		a, err := scanAccountAuth(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, *a)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

const accountAuthSelect = `SELECT account, provider, failed_at, failed_session, signed_in_at FROM account_auth`

// scanAccountAuth reads one row; sql.ErrNoRows passes through.
func scanAccountAuth(row rowScanner) (*contracts.AccountAuth, error) {
	var a contracts.AccountAuth
	var failed, signedIn string
	if err := row.Scan(&a.Account, &a.Provider, &failed, &a.FailedSession, &signedIn); err != nil {
		return nil, err
	}
	var err error
	if a.FailedAt, err = parseAuthTime(failed); err != nil {
		return nil, err
	}
	if a.SignedInAt, err = parseAuthTime(signedIn); err != nil {
		return nil, err
	}
	a.SignedOut = a.FailedAt.After(a.SignedInAt)
	return &a, nil
}

// formatAuthTime stores a zero time as "" (never happened).
func formatAuthTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseAuthTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func appendAuthChanged(tx *Tx, a *contracts.AccountAuth, actor string) error {
	payload, err := json.Marshal(contracts.AccountAuthChangedPayload{Account: a.Account, Provider: a.Provider, SignedOut: a.SignedOut})
	if err != nil {
		return err
	}
	return tx.Append(&contracts.Envelope{Type: contracts.AccountAuthChanged, Actor: actor, Payload: payload})
}
