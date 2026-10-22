package runner

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// keyedProvider is a provider that can name its default login and nothing
// else: the runner only asks it that.
type keyedProvider struct {
	provider.Provider
	account string
}

func (p keyedProvider) DefaultAccount() string { return p.account }

func authRows(t *testing.T, data *dataclient.Client) map[string]contracts.AccountAuth {
	t.Helper()
	list, err := data.AccountAuths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]contracts.AccountAuth{}
	for _, a := range list {
		out[a.Account] = a
	}
	return out
}

// An auth error signs the event's login out, attributed to this session; a
// turn with real token usage signs the login the harness last named back
// in. A zero-token turn end (the failed turn's own) clears nothing.
func TestRunnerReportsAuthFailureAndRecovery(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, provider.NewRegistry())
	r.mu.Lock()
	r.providerName, r.prov = "claude-code", keyedProvider{account: "claude-code:/default"}
	r.mu.Unlock()

	const work = "claude-code:/Users/me/.claude-work"
	r.observeConversation(provider.Event{Kind: provider.EventAccount, Account: &provider.AccountStatus{BillingMode: provider.BillingPlan, Account: work}})
	r.observeConversation(provider.Event{Kind: provider.EventError, Text: "API Error: Connection refused"})
	if rows := authRows(t, data); len(rows) != 0 {
		t.Fatalf("an unclassified error was reported: %+v", rows)
	}

	r.observeConversation(provider.Event{Kind: provider.EventError, ErrorKind: provider.ErrorKindAuth,
		Text: "Not logged in · Please run /login", Account: &provider.AccountStatus{Account: work}})
	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd, Usage: &provider.Usage{}})
	row, ok := authRows(t, data)[work]
	if !ok || !row.SignedOut || row.Provider != "claude-code" || row.FailedSession != sess.ID {
		t.Fatalf("after the auth failure = %+v, want %s signed out by this session", row, work)
	}

	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 3}})
	if row := authRows(t, data)[work]; row.SignedOut {
		t.Errorf("after an answered turn = %+v, want signed in", row)
	}
}

// An auth error with no account on it, from a session whose harness never
// named one, lands on the provider's default login.
func TestRunnerAuthFailureFallsBackToDefaultAccount(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, provider.NewRegistry())
	r.mu.Lock()
	r.providerName, r.prov = "claude-code", keyedProvider{account: "claude-code:/default"}
	r.mu.Unlock()

	r.observeConversation(provider.Event{Kind: provider.EventError, ErrorKind: provider.ErrorKindAuth, Text: "Login expired · Please run /login"})
	if row, ok := authRows(t, data)["claude-code:/default"]; !ok || !row.SignedOut {
		t.Errorf("rows = %+v, want the default login signed out", authRows(t, data))
	}
}
