package contracts

import "time"

// AccountAuth is one provider login's sign-in state (docs/AUTH-SIGNIN-
// DESIGN.md). Login state belongs to the login a session runs on, not to the
// session: Account is provider.AccountStatus.Account, the same key that
// shares plan limits, so signing in from any session clears every session on
// it. A row exists only once a send on the login has failed for auth.
type AccountAuth struct {
	Account  string `json:"account"`
	Provider string `json:"provider"`
	// FailedAt is the latest auth failure; FailedSession the session it
	// happened in. The Router falls back on FailedSession to find the login
	// of a session whose record names none yet (its first turn failed).
	FailedAt      time.Time `json:"failed_at,omitzero"`
	FailedSession string    `json:"failed_session,omitempty"`
	// SignedInAt is the latest sign-in or successful turn that cleared a
	// failure. Zero until the login has been signed out once and recovered.
	SignedInAt time.Time `json:"signed_in_at,omitzero"`
	// SignedOut is FailedAt after SignedInAt, computed by the store.
	SignedOut bool `json:"signed_out"`
}

// AccountAuthFailed is PUT /accounts/auth/failed's body: a send on Account
// was refused for auth in SessionID at At. A zero At means now.
type AccountAuthFailed struct {
	Account   string    `json:"account"`
	Provider  string    `json:"provider"`
	SessionID string    `json:"session_id,omitempty"`
	At        time.Time `json:"at,omitzero"`
}

// AccountAuthOK is PUT /accounts/auth/ok's body: Account was seen working
// (a sign-in finished, or a turn got a real answer) at At. A zero At means
// now. It only ever clears a signed-out row; anything else is a no-op.
type AccountAuthOK struct {
	Account string    `json:"account"`
	At      time.Time `json:"at,omitzero"`
}

// AccountAuthChangedPayload is account.auth_changed's payload.
type AccountAuthChangedPayload struct {
	Account   string `json:"account"`
	Provider  string `json:"provider"`
	SignedOut bool   `json:"signed_out"`
}

// SessionAuth is the Router's GET /sessions/{id}/auth: the session's login
// and whether it is signed out, plus the command that signs it back in when
// the provider has one (provider.Authenticator).
type SessionAuth struct {
	Provider   string        `json:"provider"`
	Account    string        `json:"account"`
	SignedOut  bool          `json:"signed_out"`
	FailedAt   time.Time     `json:"failed_at,omitzero"`
	SignedInAt time.Time     `json:"signed_in_at,omitzero"`
	Login      *LoginCommand `json:"login,omitempty"`
}

// LoginCommand mirrors provider.LoginCommand: Argv[0] is an absolute path;
// Env is KEY=VALUE overrides on top of the runner's environment.
type LoginCommand struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
}

// AccountSignedIn is the Router's POST /accounts/signed-in body: the login
// command for Account exited 0.
type AccountSignedIn struct {
	Account string `json:"account"`
}
