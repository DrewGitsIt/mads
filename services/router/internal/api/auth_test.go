package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newAuthServer is a Router with claude-code registered on a stand-in
// binary path: building a login command never runs it.
func newAuthServer(t *testing.T) (*httptest.Server, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	for _, p := range []provider.Provider{fake.New(), claudecode.New(claudecode.Config{Bin: "/opt/claude/bin/claude"})} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv, dataclient.New(base, "session")
}

func getSessionAuth(t *testing.T, srvURL, id string) (contracts.SessionAuth, map[string]json.RawMessage) {
	t.Helper()
	resp, err := http.Get(srvURL + "/sessions/" + id + "/auth")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %d, want 200", resp.StatusCode)
	}
	raw := decodeJSON[json.RawMessage](t, resp)
	var out contracts.SessionAuth
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return out, fields
}

// The session's login is the one its harness reported (billing.account),
// else the one a failure in this session was reported against, else the
// provider's default; signing in clears the login for every session on it.
func TestSessionAuthResolvesTheLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	srv, data := newAuthServer(t)
	ctx := context.Background()
	mk := func(name string) contracts.Session {
		t.Helper()
		sess := contracts.Session{Name: name, Provider: "claude-code", Activity: contracts.SessionInactive}
		if err := data.CreateSession(ctx, &sess); err != nil {
			t.Fatal(err)
		}
		return sess
	}
	const work = "claude-code:/Users/me/.claude-work"
	billed := mk("billed")
	if err := data.SetSessionBilling(ctx, billed.ID, contracts.SessionBillingPatch{
		Mode: contracts.BillingPlan, Account: work,
		Limits: []contracts.RateLimit{{ID: "five_hour", UsedPct: 10, Scope: "account"}},
	}); err != nil {
		t.Fatal(err)
	}
	firstTurnFailed := mk("first turn failed")
	fresh := mk("fresh")

	if err := data.AccountAuthFailed(ctx, contracts.AccountAuthFailed{Account: work, Provider: "claude-code", SessionID: billed.ID}); err != nil {
		t.Fatal(err)
	}
	if err := data.AccountAuthFailed(ctx, contracts.AccountAuthFailed{Account: "claude-code:/custom", Provider: "claude-code", SessionID: firstTurnFailed.ID}); err != nil {
		t.Fatal(err)
	}

	got, _ := getSessionAuth(t, srv.URL, billed.ID)
	if got.Account != work || got.Provider != "claude-code" || !got.SignedOut || got.FailedAt.IsZero() {
		t.Errorf("billed session = %+v, want %s signed out", got, work)
	}
	if got.Login == nil || !slices.Equal(got.Login.Argv, []string{"/opt/claude/bin/claude", "auth", "login"}) ||
		!slices.Equal(got.Login.Env, []string{"CLAUDE_CONFIG_DIR=/Users/me/.claude-work"}) {
		t.Errorf("billed session login = %+v, want claude auth login on its config dir", got.Login)
	}

	if got, _ := getSessionAuth(t, srv.URL, firstTurnFailed.ID); got.Account != "claude-code:/custom" || !got.SignedOut {
		t.Errorf("session with only a failure = %+v, want the failure's login", got)
	}

	got, fields := getSessionAuth(t, srv.URL, fresh.ID)
	if def := "claude-code:" + filepath.Join(home, ".claude"); got.Account != def || got.SignedOut {
		t.Errorf("fresh session = %+v, want %s, signed in", got, def)
	}
	if _, ok := fields["failed_at"]; ok {
		t.Errorf("fresh session carries failed_at: %s", fields["failed_at"])
	}
	var login map[string]json.RawMessage
	if err := json.Unmarshal(fields["login"], &login); err != nil || string(login["env"]) != "[]" {
		t.Errorf("default login = %s, want env [] (CLAUDE_CONFIG_DIR would pick another keychain entry)", fields["login"])
	}

	resp := postJSON(t, srv.URL+"/accounts/signed-in", contracts.AccountSignedIn{Account: work})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("signed-in status = %d, want 204", resp.StatusCode)
	}
	if got, _ := getSessionAuth(t, srv.URL, billed.ID); got.SignedOut || got.SignedInAt.IsZero() || time.Since(got.SignedInAt) > time.Minute {
		t.Errorf("after sign-in = %+v, want signed in just now", got)
	}

	resp = postJSON(t, srv.URL+"/accounts/signed-in", map[string]string{})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("signed-in with no account: status = %d, want 400", resp.StatusCode)
	}
}

func TestSessionAuthNotFound(t *testing.T) {
	srv, _ := newAuthServer(t)
	resp, err := http.Get(srv.URL + "/sessions/ses_nope/auth")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
