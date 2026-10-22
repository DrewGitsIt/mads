package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func authChanges(t *testing.T, l *Log) []contracts.AccountAuthChangedPayload {
	t.Helper()
	envs, err := l.Query(context.Background(), Filter{Types: []string{string(contracts.AccountAuthChanged)}})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]contracts.AccountAuthChangedPayload, 0, len(envs))
	for _, e := range envs {
		if e.SessionID != "" {
			t.Errorf("auth change carries session %q; it is global", e.SessionID)
		}
		var p contracts.AccountAuthChangedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func authRow(t *testing.T, s *AccountAuths, account string) contracts.AccountAuth {
	t.Helper()
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Account == account {
			return a
		}
	}
	t.Fatalf("no row for %s in %+v", account, list)
	return contracts.AccountAuth{}
}

// Signed-out flips announce once each way: repeated failures on a dead
// login are silent, and so are answered turns on a working one.
func TestAccountAuthAnnouncesOnlyFlips(t *testing.T) {
	l := openMigratedDB(t)
	s := NewAccountAuths(l)
	ctx := context.Background()
	const acct = "claude-code:/Users/me/.claude"
	t0 := time.Date(2026, 10, 22, 19, 0, 0, 0, time.UTC)

	// A working login with no row: nothing to clear, nothing written.
	if err := s.OK(ctx, contracts.AccountAuthOK{Account: acct, At: t0}, "test"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(ctx); len(list) != 0 {
		t.Fatalf("OK created a row: %+v", list)
	}

	for i, sess := range []string{"ses_a", "ses_b"} {
		if err := s.Failed(ctx, contracts.AccountAuthFailed{
			Account: acct, Provider: "claude-code", SessionID: sess, At: t0.Add(time.Duration(i+1) * time.Minute),
		}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	row := authRow(t, s, acct)
	if !row.SignedOut || row.FailedSession != "ses_b" || !row.FailedAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("after two failures = %+v, want signed out by ses_b's", row)
	}
	if got := authChanges(t, l); len(got) != 1 || !got[0].SignedOut || got[0].Account != acct || got[0].Provider != "claude-code" {
		t.Fatalf("changes = %+v, want one sign-out", got)
	}

	// A success older than the failure (a turn that ended before it)
	// clears nothing.
	if err := s.OK(ctx, contracts.AccountAuthOK{Account: acct, At: t0.Add(90 * time.Second)}, "test"); err != nil {
		t.Fatal(err)
	}
	if !authRow(t, s, acct).SignedOut {
		t.Error("an older success signed the login back in")
	}

	signIn := t0.Add(5 * time.Minute)
	for range 2 {
		if err := s.OK(ctx, contracts.AccountAuthOK{Account: acct, At: signIn}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	row = authRow(t, s, acct)
	if row.SignedOut || !row.SignedInAt.Equal(signIn) {
		t.Errorf("after sign-in = %+v, want signed in at %v", row, signIn)
	}
	got := authChanges(t, l)
	if len(got) != 2 || got[1].SignedOut {
		t.Fatalf("changes = %+v, want sign-out then one sign-in", got)
	}

	// A failure reported late, from before the sign-in, doesn't sign the
	// login out again.
	if err := s.Failed(ctx, contracts.AccountAuthFailed{Account: acct, Provider: "claude-code", At: t0.Add(4 * time.Minute)}, "test"); err != nil {
		t.Fatal(err)
	}
	if authRow(t, s, acct).SignedOut || len(authChanges(t, l)) != 2 {
		t.Error("a stale failure signed the login out again")
	}

	// A new failure after the sign-in does.
	if err := s.Failed(ctx, contracts.AccountAuthFailed{Account: acct, Provider: "claude-code", SessionID: "ses_c", At: t0.Add(10 * time.Minute)}, "test"); err != nil {
		t.Fatal(err)
	}
	if !authRow(t, s, acct).SignedOut || len(authChanges(t, l)) != 3 {
		t.Error("a fresh failure after sign-in didn't sign the login out")
	}
}

func TestAccountAuthValidates(t *testing.T) {
	s := NewAccountAuths(openMigratedDB(t))
	var verr *contracts.ValidationError
	if err := s.Failed(context.Background(), contracts.AccountAuthFailed{Account: "a"}, "test"); !errors.As(err, &verr) {
		t.Errorf("failure with no provider: err = %v, want a validation error", err)
	}
	if err := s.OK(context.Background(), contracts.AccountAuthOK{}, "test"); !errors.As(err, &verr) {
		t.Errorf("ok with no account: err = %v, want a validation error", err)
	}
}
