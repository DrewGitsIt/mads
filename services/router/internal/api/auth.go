package api

import (
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// handleSessionAuth answers GET /sessions/{id}/auth: whether the login the
// session runs on is signed out, and the command that signs it back in
// (docs/AUTH-SIGNIN-DESIGN.md). Login state belongs to the login, not the
// session, so this reads the account's row, wherever it was reported from.
func (s *server) handleSessionAuth(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	rows, err := s.data.AccountAuths(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := contracts.SessionAuth{Provider: sess.Provider, Account: sessionAccount(sess, rows, s.defaultAccount)}
	for _, a := range rows {
		if a.Account == out.Account && out.Account != "" {
			out.SignedOut, out.FailedAt, out.SignedInAt = a.SignedOut, a.FailedAt, a.SignedInAt
			break
		}
	}
	out.Login = s.loginCommand(sess.Provider, out.Account)
	writeJSON(w, http.StatusOK, out)
}

// sessionAccount is the login sess runs on: the one its harness last
// reported (billing.account); else the one a failure in this very session
// was reported against, which covers a custom-env session whose first turn
// failed before anything else was heard; else the provider's default login.
func sessionAccount(sess contracts.Session, rows []contracts.AccountAuth, defaultAccount func(string) string) string {
	if sess.Billing != nil && sess.Billing.Account != "" {
		return sess.Billing.Account
	}
	var latest *contracts.AccountAuth
	for i := range rows {
		if a := &rows[i]; a.FailedSession == sess.ID && (latest == nil || a.FailedAt.After(latest.FailedAt)) {
			latest = a
		}
	}
	if latest != nil {
		return latest.Account
	}
	return defaultAccount(sess.Provider)
}

// loginCommand is the provider's sign-in command for account
// (provider.Authenticator); nil when it has none or can't build one.
func (s *server) loginCommand(name, account string) *contracts.LoginCommand {
	if s.providers == nil || name == "" || account == "" {
		return nil
	}
	p, err := s.providers.Get(name)
	if err != nil {
		return nil
	}
	a, ok := p.(provider.Authenticator)
	if !ok {
		return nil
	}
	cmd, ok := a.LoginCommand(account)
	if !ok {
		return nil
	}
	env := cmd.Env
	if env == nil {
		env = []string{} // the UI reads an array, never null
	}
	return &contracts.LoginCommand{Argv: cmd.Argv, Env: env}
}

// handleAccountSignedIn answers POST /accounts/signed-in: the login command
// for an account exited 0, so every session on that login is signed in
// again. The Data Service announces account.auth_changed if it was signed
// out.
func (s *server) handleAccountSignedIn(w http.ResponseWriter, r *http.Request) {
	var body contracts.AccountSignedIn
	if !decode(w, r, &body) {
		return
	}
	if body.Account == "" {
		var v contracts.ValidationError
		v.Add("account", "required")
		writeError(w, &v)
		return
	}
	if err := s.data.AccountAuthOK(r.Context(), contracts.AccountAuthOK{Account: body.Account, At: time.Now().UTC()}); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
