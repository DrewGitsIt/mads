# Signing back in when an agent's login expires

**Status:** Done on `auth-signin` (2026-10-22): claude-code, codex and hermes.
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

**codex:** a failed turn (`turn/completed` status `failed`) whose `codexErrorInfo` is
`"unauthorized"`, or a variant carrying `httpStatusCode` 401, or `"other"` with codex's own
"unexpected status 401 …" message, is an auth failure. 403 is not: it can be policy.
Captured 2026-10-22 with a scratch `CODEX_HOME`: an expired login fails with
`unauthorized` ("Your access token could not be refreshed. Please log out and sign in
again."); no login at all retries on `responseStreamDisconnected {httpStatusCode: 401}`,
then fails with `other` and "unexpected status 401 Unauthorized: …". Both get as far as the
turn: initialize and `thread/start` succeed signed out. Stored, a failed turn has no item;
its error lives on the turn (`thread/turns/list`), so the transcript adds an `IsError` agent
row after the turn's items, classified the same way. The account is
`codex:<$CODEX_HOME, else ~/.codex>`. Login command: `codex login`, with `CODEX_HOME` only
for a non-default home.

**hermes:** hermes never fails a prompt on the ACP wire: a refused turn resolves
`end_turn`, with hermes's own sentence ("Nous Portal rejected your sign-in … Sign in
again: `hermes portal`") as the reply. What it keeps is the failed-turn row it writes to
`state.db` first: `display_kind` `failed_turn`, `display_metadata {error, error_surface}`.
A row whose `error_surface.layer` is `auth` and whose error starts "HTTP 401: " is an auth
failure. hermes's auth layer takes 403s too; they are not: the one real failure in Drew's
store (2026-10-20) is "HTTP 403: unauthorized: not licensed to use Copilot". At a turn end
that spent no tokens, the session reads that row and emits the auth `EventError` the wire
never carried; the stored row gets the same `ErrorKind`. ACP's reserved `-32000` is tagged
auth in `acprpc` for any ACP harness (hermes never sends it). The account is
`hermes:<$HERMES_HOME, else ~/.hermes>`, but one home pools credentials for several
inference providers, so the login command re-signs the provider whose refusal signed the
home out: the newest 401 auth row in its `state.db`. Only an OAuth provider gets one:
`hermes auth add <provider> --type oauth` (nous, anthropic, openai-codex, …), with
`HERMES_HOME` only for a non-default home. Not `hermes portal`: that is the same login
plus onboarding (model pick, Tool Gateway). A refused API key needs a new key, not a
sign-in (copilot's may come from `gh` itself), so there Sign in stays hidden; the failure
is still tracked. The command is withheld if launching `hermes` in that home would start
its dependency install.

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
  account key covers them all: signing in to one provider clears the home, and a turn
  answered by another provider clears it too. `hermes auth add` for a pooled OAuth
  provider other than nous adds a fresh credential beside the refused one rather than
  replacing it (hermes's own advice for a refused grant).
