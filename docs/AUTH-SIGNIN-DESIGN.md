# Signing back in when an agent's login expires

**Status:** In progress on `auth-signin` (2026-10-22). Order: claude-code, then codex, then hermes.
**Driver:** Drew's claude login expired mid-session. Getting back in meant opening the
shell dock, starting `claude`, typing `/login`, finishing in the browser, then resending.
It worked, but nothing in mads helped.

## Decisions (Drew, 2026-10-22)

- **Forward flow only.** mads reacts to a send that definitely failed for auth. It does
  not probe login status ahead of time: `claude auth status` reports `loggedIn: true` from
  stored credentials, so it can't be trusted to catch a dead refresh token.
- **Account scope.** Login state belongs to the login a session runs on, not to the
  session or to the provider. The key is `provider.AccountStatus.Account`, the same one
  that shares plan limits (claude-code: `claude-code:<config dir>`).
- **Sign in shows only on a failed message**, in a session whose send failed. Account
  scope decides when it clears: signing in from any session clears every session on that
  login.
- **After signing in:** a toast ("Signed in to Claude") and Retry. No automatic resend.

## What the user sees

1. A send fails for auth. Where "Not delivered" was, the message now says
   **Signed out of Claude**, with the harness's reason in the tooltip and a **Sign in**
   button.
2. Sign in opens a dialog with a small terminal running the provider's login command
   (`claude auth login`). The CLI opens the browser itself. A code-paste flow works because
   it is a real pty.
3. The command exits 0: the dialog closes, the toast shows, and every failed message on
   that login goes back to "Not delivered" with Retry (Retry still only on a session's
   latest send, as today). A non-zero exit keeps the dialog open: "Sign-in didn't finish"
   plus Try again.
4. Without Electron (a browser preview) the dialog shows the command to copy instead.

## Flow

```
provider adapter ──EventError{ErrorKind:"auth"}──▶ session runner
   (transcript rows: Message.ErrorKind="auth")        │ PUT /accounts/auth/failed
                                                      ▼
                                              Data Service: account_auth table
                                              appends account.auth_changed on a flip
                                                      │
   UI ◀── GET /sessions/{id}/auth (Router) ◀──────────┘
   UI ── runs login pty (shell host "run") ── exit 0 ──▶ POST /accounts/signed-in (Router → Data)
   runner ── successful turn ──▶ PUT /accounts/auth/ok (clears it from any session)
```

### Provider layer (`providers/provider`)

- `ErrorKindAuth = "auth"`. `Event.ErrorKind` on `EventError` and `Message.ErrorKind` on
  an `IsError` agent row. Empty means unclassified. Only definite auth failures are tagged.
- Optional `Authenticator` interface: `LoginCommand(account string) (LoginCommand, bool)`,
  where `LoginCommand{Argv []string; Env []string}`. Argv[0] is an absolute binary path
  (`binlocate`). Env holds only the overrides that select the login (`CLAUDE_CONFIG_DIR`),
  and only when the account's dir isn't the default. Setting `CLAUDE_CONFIG_DIR` to the
  default path changes which keychain entry claude uses.
- The live auth `EventError` carries `Account` (`Event.Account.Account`) when the adapter
  knows it.

**claude-code:** an assistant line with `"error":"authentication_failed"` (stream and
transcript, `is_api_error_message`/`isApiErrorMessage` true) is an auth failure. Captured
2026-10-22 with an empty `CLAUDE_CONFIG_DIR`: an assistant line "Not logged in · Please run
/login" with `error: authentication_failed`, then `result` with `is_error: true`,
`terminal_reason: api_error`. Expired login reads "Failed to authenticate: OAuth session
expired and could not be refreshed" or "Login expired · Please run /login". Login command:
`claude auth login`.

### Session runner

- On an auth `EventError`: `PUT /accounts/auth/failed {account, provider, session_id, at}`.
  The account comes from the event, else the last `EventAccount.Account` the runner saw,
  else the provider's `DefaultAccount()`.
- On a turn end that reports real token usage (the API answered, so auth works):
  `PUT /accounts/auth/ok {account, at}`. The store only touches a row that is signed out, so
  this is a cheap no-op almost every time.

### Data Service

- Table `account_auth(account PK, provider, failed_at, failed_session, signed_in_at)`.
  Signed out means `failed_at > signed_in_at`.
- `PUT /accounts/auth/failed` upserts. `PUT /accounts/auth/ok` updates an existing
  signed-out row only. `GET /accounts/auth` lists the rows.
- Appends the global event `account.auth_changed {account, provider, signed_out}` only when
  signed-out flips.

### Router

- `GET /sessions/{id}/auth` →
  `{provider, account, signed_out, failed_at?, signed_in_at?, login?: {argv, env}}`.
  The session's account is `billing.account`, else the account of a row whose
  `failed_session` is this session, else the provider's `DefaultAccount()`.
- `POST /accounts/signed-in {account}` → `PUT /accounts/auth/ok`.

### UI

- `TranscriptMessage.error_kind`. `collapseFailedSends` carries `errorKind` on `Delivery`
  and on `FailedAttempt`.
- `useSessionAuth(sessionId)`: fetches `/sessions/{id}/auth` and refetches on
  `account.auth_changed`.
- `SendDelivery`: an auth failure on a signed-out account → "Signed out of {provider}" and
  Sign in. Otherwise unchanged.
- `SignInDialog`: a `TerminalView` over a bridge on the shell host's new `run` request (a
  command pty: argv and env, never listed among the dock's shells, never kept alive after
  exit).
- Failed-attempt lines read "failed at 10:42". The message's own hover stamp sat on the
  same row and made two times look like one (Drew's screenshot, 2026-10-22).

## Known edges

- **Router and Electron on different machines:** the login runs in the window's machine,
  using the Router's binary path. Not supported; mads runs both locally today.
- **A custom-env session whose very first turn fails** has no `billing.account` yet.
  `failed_session` covers it.
- **Hermes** pools credentials per inference provider inside one `HERMES_HOME`. One
  account key can cover several credentials. Its login command is decided in the hermes
  step.
