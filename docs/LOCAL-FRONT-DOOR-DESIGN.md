# Local front door: lock mads to the user who runs it

**Status:** Design agreed 2026-11-16; to build. **Branch:** `local-front-door`
(from `main`).
**Driver:** Drew. This is step A2 of the mesh auth design (`docs/MESH-AUTH-DESIGN.md`
on the `mesh-mode` branch, D7), and it ships first and on its own. Today any process
that can reach loopback, including **another OS user's**, can drive the Router
(message a full-trust session, start sessions) and read or write the Data Service,
which has no auth at all.

## Context: who calls mads today, and how

| Caller | Reaches | Today |
|---|---|---|
| The UI (Electron renderer, or a plain browser page) | Router | `fetch`, `EventSource`, `<img>`. `EventSource` and `<img>` can't carry custom headers. |
| Electron main | Router | `fetch /config` (`ui/electron/linkOpener.ts`) |
| Session children | Router (orchestrate), Data Service | `-router-url`, `dataclient` |
| Pipeline children | Data Service | `-data-url` |
| `mads internal orchestrate-mcp` | Router | Started by the agent's CLI inside the agent's env (`MADS_ROUTER_URL`, `MADS_SESSION_ID`). Identifies itself with a caller-asserted `X-Mads-Actor: session-<id>`. |
| `mads` CLI (`seed`, `run`, `personas`, `template`, `update`, `uninstall`, `orchestrate`) | Router, Data Service | Default `localhost` URLs |
| Mesh Service (`mesh-mode`, not yet on main) | Router | Loopback forward |
| The git post-commit hook in user repos (`pkg/githook`) | Data Service | `curl`, with the URL from `git config mads.dataUrl` |
| e2e harness, Vite dev server, preflight | Both | Test and dev tooling |
| People and agents, by hand | Router | `curl localhost:17373/...` (README, Makefile notes, dogfooding) |

`pkg/localguard` already keeps out *web pages* (Host and Origin checks). It does
nothing about other programs.

## Principle

**One install token, held only by the user, required on every API call, by every
caller.** On loopback, other OS users can't sniff traffic, so a token-locked door
gives them the same guarantee a Unix socket would, without changing how the UI
talks to the Router. Unix sockets were considered and rejected: browsers can't use
them, and a hybrid adds a second mechanism for no gain against this threat.

**What this protects against:**
- Other OS users on the machine.
- Sandboxed apps (macOS App Sandbox, Flatpak, Snap) that can reach loopback but
  can't read `~/.mads`.
- Containers sharing the host network.
- Web pages (already, through localguard).

**What it does not protect against:** anything running *as the user*, which can read
the token file. That includes a full-trust agent: it can `cat` the file. **Security
against a determined, compromised agent is a separate topic** (Drew, 2026-11-16).
Per-session scoped tokens (D4) are hygiene, not containment.

## Decisions

### D1. The install token
- 32 random bytes, base64url, in `$MADS_HOME/local.token`, mode `0600`, inside the
  `0700` `$MADS_HOME`.
- Created by whichever service starts first, using an exclusive create under the
  existing flock helpers in `pkg/credstore` (or an equivalent). Losers re-read the
  file, so there's never a second token.
- A new package, **`pkg/localauth`**: `LoadOrCreate(madsHome)`, `Load(madsHome)`, a
  constant-time `Check(presented)`, and an HTTP middleware. It checks file mode and
  owner on load and refuses a group- or world-readable token with an actionable error.
- Rotation: `mads token rotate`. It writes a new token, and running services pick it
  up on their next check, which re-reads the file when its mtime changes. Rotation
  logs out plain-browser cookies and invalidates scoped tokens (D4).

### D2. What a caller presents
- **Go callers and scripts:** `Authorization: Bearer <token>`.
- **The UI:** the cookie `mads_local=<token>` with `HttpOnly`, `SameSite=Strict`,
  `Path=/`, scoped to the Router's origin.
  - **Electron:** main reads the token file and sets the cookie on its session's
    cookie jar *before* loading the page. The renderer never sees the token.
  - **A plain browser:** `mads open` asks the Router (with the bearer) for a
    **one-time code**: 60 s, single use. It prints or opens
    `http://localhost:17373/auth/enter?code=…`, which sets the cookie and redirects
    to `/`. This is the Jupyter pattern. It's kept because tests and Playwright rely
    on it (Drew, 2026-11-16).

### D3. Where the door is checked
- **Router:** every API route requires the token (header or cookie), or a scoped
  token where D4 allows one. **Exempt:**
  - The static UI (the mux's `/` pattern). It's public code and holds no data.
  - `GET /health`, which `app.sh` uses to identify a running checkout. Its fields
    move behind the door except `service` and `status`, and `app.sh` sends the
    bearer to read the rest.
  - `GET /auth/enter`.
- **Middleware:** it sits next to localguard (`guard.Wrap(auth.Wrap(...))`) and
  classifies requests with the same ServeMux-pattern lookup the Mesh Service uses,
  so API routes and the static UI can't be confused.
- **Data Service:** every route except `GET /health` requires the token. This closes
  the "no auth at all" gap.
- **Failures:** `401` with `{"code":"unauthorized","error":"…run mads open, or send
  the token in $MADS_HOME/local.token"}`. Refusals are logged with the method and
  path, never the presented value.

### D4. Children and agents: scoped tokens (Drew: "b, for hygiene")
- **Session and pipeline children** get the install token through the env
  (`MADS_LOCAL_TOKEN`, not argv). They use it for the Data Service, and remove it
  from their own env before starting anything else. The agent's environment
  (`pkg/agentenv`, both inherit and isolated modes) must never contain it, and a
  test pins that.
- **The orchestrate MCP server** (which runs inside the agent's env) gets a
  **session-scoped token** instead, `MADS_ROUTER_TOKEN`, minted by the Router when
  the child spawns. It grants only:
  - `GET/POST /sessions/{own id}/orchestrate/*`
  - `POST /links/{id}/orchestrate/message`, where the link's granting session is
    the token's session.
- **The Router sets the actor from the token** (`session-<id>`) and ignores any
  `X-Mads-Actor` the caller sends. So one session can no longer pose as another, a
  hole that exists today.
- Scoped tokens are held in memory by the Router. They're revoked when the session
  ends or is deleted, on rotation, and on a Router restart; a respawned child gets a
  fresh one.

### D5. The CLI and hand-typed calls
- Every `mads` subcommand loads the token from `$MADS_HOME` and sends it, through
  `dataclient.New` and the Router clients.
- **`mads api METHOD PATH [-d body]`** calls the Router (or the Data Service with
  `--data`) with the token. It's the documented replacement for
  `curl localhost:17373/...`.
- The README, the Makefile notes and the service READMEs move to `mads api`, or show
  `curl -K -` with the header on stdin. Never put the token in argv (a `$(cat …)`
  argument is visible to other users through `ps`).

### D6. The git hook (Drew: "a + a one-time refresh")
- New hook script: it reads `git config mads.tokenFile`, a **path** (never the token
  itself, since repo config can be readable by others), and sends the bearer. A
  missing or unreadable file stays silent (`|| true`), as today.
- Hooks are re-installed whenever mads touches a repo, as today.
- **`mads hooks refresh`** re-installs across every repo mads knows (workspaces,
  plus each session's dir and worktree), once, and prints what it updated and what
  it skipped.
- Old hooks in repos never refreshed stop reporting commits; they still never break
  a commit. Release notes say so.

### D7. Dev and test tooling
- **`scripts/app.sh`:** ensures the token exists before starting services. Electron
  reads it per D2.
- **Vite dev server:** proxies the Router same-origin, adding the bearer from the
  token file. The dev page then needs no cookie, and nothing token-shaped reaches
  the browser bundle.
- **e2e topology:** each topology has its own `$MADS_HOME`, so its own token.
  - The Playwright `page` fixture sets the cookie before the first `goto`.
  - `lib/seed.ts` helpers send the bearer.
  - The mesh-mode spec's two topologies each have their own token.
- **`-insecure-no-auth`:** exists only for unit tests that construct a mux directly.
  It's refused unless the binary is a `go test` build, so it can't be turned on in
  production.

### D9. Session and pipeline children listen on Unix sockets, not TCP (added 2026-11-16)

**The gap (found while building F1–F8):** D3 locked the Router and the Data Service,
but every session and pipeline child serves its own HTTP API on a random loopback
**TCP** port with no check. Any local user who scans for it can `POST /messages` to
a full-trust agent, going around the front door.

No browser ever talks to a child; only the Router does. So the constraint that ruled
out Unix sockets for the Router (D2) doesn't apply here.

Precedent: the incidents behind unauthenticated TCP admin APIs (Docker's port 2375,
Ray's Jobs API / ShadowRay) were answered by permission-protected sockets (Docker's
default socket, `ssh-agent`, PostgreSQL's peer auth). A second per-child secret (the
Jupyter / go-plugin pattern) was rejected as an extra layer of secrets to mint, pass,
scrub and review.

**Design:**
- **Where:** children listen on `$MADS_HOME/run/<id>.sock`. `run/` is `0700`.
- **Path length:** macOS caps socket paths at 104 bytes. If the path would be too
  long (e.g. deep e2e temp dirs), the Supervisor uses a short `0700` directory under
  `/tmp` named for the install, and a path that still won't fit fails with a clear
  error, never a bare `bind: invalid argument`.
- **The Router** reaches children through an `http.Transport` that dials the socket.
  `proxyTo`, `forwardToChild` and the supervisor's address handling change; their
  callers don't. Streaming (SSE) and WebSocket upgrades pass unchanged.
- **Same-user check:** on every accepted connection the child verifies the peer's
  uid equals its own (`SO_PEERCRED` on Linux, `getpeereid` on macOS) and closes
  anything else. That's defense in depth under the directory permissions.
- **No TCP port when spawned by the Router.** `-addr localhost:PORT` stays only for
  running a service by hand (the README workflow). It logs a loud warning and is
  never what the Supervisor passes.
- The `addr` field in Router responses (create, bounce, resume, retros, pipelines)
  becomes the socket path. Nothing in the UI uses it.

**Debugging (accepted 2026-11-16), shipped in the same change:**
- **`mads api --session <id> METHOD PATH`** talks to a child's socket directly.
- **`mads ps`** lists live session and pipeline children: id, pid, socket, state,
  uptime. It's the `lsof -i` replacement, and it also exposes leftover processes.
- **Hand-run TCP mode** as above.
- **Docs for people *and agents*:** the service READMEs, Makefile notes, and any
  agent instruction file move from `lsof`/`curl localhost:PORT` to `mads api
  --session` / `mads ps`. The e2e smoke harness's "listening on" parsing follows the
  new address form.

**Later, not in A2:**
- **Request tracing** (interested, 2026-11-16): Router-side per-call logs of child
  traffic (session, method, path, status, duration, bytes), with a request id the
  child also logs. It replaces packet capture.
- **The Data Service on a socket**, so the token becomes defense in depth.
- **A traffic tap** (`mads debug tap`): skipped.

### D8. Compatibility
- **A UI on an old build against a new Router** gets `401`. Electron always gets the
  cookie from main, so the only casualty is an old plain-browser tab, which shows
  "run mads open".
- **New UI, old Router:** the cookie is simply ignored, so it works as before.
- **CLI and services from mixed checkouts:** they share `$MADS_HOME`, so they share
  the token. `app.sh`'s existing checkout check still applies.

## Round-1 hardening decisions (2026-11-16)

Adversarial review and testing found leaks to *other* OS users that the design had
left open. Decisions (Drew):

- **H1. The cookie is a browser-session token, never the install token.**
  `/auth/enter` sets a Router-minted, short-lived (hours), revocable token that only
  the Router accepts. The CLI and services never accept it. The cookie name includes
  the Router's port (`mads_local_<port>`), so two installs in one browser don't
  clobber each other.
  **The plain-browser cookie path is for testing mads only:** a dev activity, not a
  consumer feature. Electron is the product path. This supersedes D2's "cookie =
  install token".
- **H2. `mads open` writes an owner-only (`0600`) redirect file and opens that file**
  (Jupyter's approach). The one-time code never appears in any process's argv.
- **H3. The Vite dev proxy authenticates nothing itself.** The dev page signs in
  through the same one-time-code flow; the proxy only forwards the credential the
  request already carries. This supersedes D7's "proxy adds the bearer".
- **H4. Session and pipeline children read `$MADS_HOME/local.token` themselves** and
  re-read it when it changes, like the services do. The Router passes only the
  *path*. No install token in any child's env, argv or pipe, and running sessions
  survive a rotation. (An inherited pipe was considered and rejected: new plumbing
  for every non-Router spawn path, and it doesn't solve rotation.)
  - **The scoped orchestrate token stays in env.** The orchestrate MCP server needs
    it there for the provider's config. **Accepted risk:** visible to the *same*
    user via `ps eww`; its scope is one session's orchestrate routes.
  - **No provider may put a token in argv.** Goose passes it by env or an owner-only
    file, and a test asserts this for every provider.
- **Straightforward fixes:**
  - strip `Cookie` (and `Authorization`) before proxying to children;
  - `childsock.Find` and `mads ps` apply the same owner and mode checks as `Path`;
  - streams opened with a credential are closed when that credential is revoked
    (rotation, session-token expiry);
  - enforce `0700` on `$MADS_HOME`;
  - `/auth/code` accepts the bearer only, and `HEAD` doesn't redeem a code;
  - Electron's token read applies the mode and owner checks;
  - the docs never put the token in argv (`mads api`, or `curl -K -`);
  - the actor test pins the real protection (session and link ownership) or goes;
  - build outputs (`/router`, `/data`, `/testchild`) are removed from the branch's
    history and ignored.

## Round-2 hardening decisions (2026-11-17)

- **H5. Browser sessions last 1 hour, not 8** (Drew). Round-2 testing showed a
  captured browser cookie gives a plain program the whole Router API (except minting
  sign-in codes) for its lifetime. Electron renews automatically before expiry, so
  users don't notice.
- **H6. After a rotation, running sessions' orchestrate tools stop until each
  session restarts** (Drew: document, don't re-issue). Rotation revokes every scoped
  token. Install-token users (the services, the CLI, children via their token file)
  carry on. `mads token rotate` says so.
- **Must-fixes from round 2:**
  - **`/auth/enter`.** localguard lets a top-level GET navigation through (the
    one-time code is the credential, so there's nothing to protect against
    cross-site). It answers with a same-origin page that moves on to `/`, so the
    `file://` redirect from `mads open` (H2) works in a real browser.
  - **Router background watchers** (synthesis, digest, merge, signal) loop around
    subscribe-and-consume, resuming from the last seq, so a rotation that ends their
    Data Service stream doesn't stop them.
  - **Electron signs in again after a Router restart:** on any 401 and on Retry
    (renderer → main over IPC), with backoff when a mint fails.
- **Also:**
  - the door tries every cookie of its name (cookie tossing);
  - the git hook sends the token only to loopback `mads.dataUrl`s;
  - tightening `$MADS_HOME` is logged, with a clear error where chmod is ignored;
  - the provider argv test spawns every harness, each transport separately, and
    exercises MCP over stdio;
  - stale comments are fixed;
  - D5 never shows the token in argv.

## Review decision: no cookie, no plain-browser access (2026-11-17)

**H7.** In architecture review, the browser cookie (1-hour sessions, a per-port
name, `/auth/enter`, one-time codes, `mads open`, a localguard exception, Electron's
sign-in manager) was the odd one out. It existed only because `EventSource` and
`<img>` can't carry headers, but both browsers mads needs can add headers below the
page (Drew):
- **Electron:** the main process adds `Authorization: Bearer <install token>` to the
  window's requests to the Router with `webRequest`, filtered to the Router's URL
  only. Page code can't read injected headers. It re-reads the token file on change,
  as today.
- **Playwright:** each context sets `extraHTTPHeaders` with the topology's token.
- **Removed:** browser sessions, the cookie, `/auth/code`, `/auth/enter`, `mads open`
  and its redirect files, localguard's `/auth/enter` exception, the sign-in manager.
  H1, H2 and H5 are superseded, and the sign-in parts of D2/D3/D7 go with them.
- **Kept:** localguard itself, which still blocks DNS rebinding and cross-site
  requests from web pages.
- **Credential kinds drop to two:** the install token and scoped orchestrate tokens.
  The one-time code returns with mesh pairing (MESH-AUTH A6).
- **No plain-browser access at all** (Chrome etc.). The Vite dev page works inside
  Electron (`VITE_DEV_SERVER_URL`), and the Vite proxy still adds nothing.
- **Latency:** the `webRequest` hook routes matching requests through Electron's
  main process. Scope it to the Router's URL and measure page-load and stream
  timings before and after.

## Risks accepted
- **Browser cookies aren't port-scoped** (no browser cookie is), so another user's
  localhost server could capture the browser-session cookie (H1). That gives a
  plain program the Router API for up to 1 hour (H5), never the install token. Accepted: the
  plain-browser path is for testing mads only.
- **The scoped orchestrate token is visible to the same user via `ps eww`** (H4).
- **Same-user processes can still reach child sockets.** This is the same ceiling as
  the token.
- Anything running as the user (including full-trust agents) can read the token.
  This is the ceiling of a no-login local design, and the same as SSH keys.
- Old git hooks in repos mads never revisits stop reporting commits until refreshed.

## Non-goals
- Device keys, pairing, the mesh (A1, A3–A7).
- Windows.
- Unix-socket transport.
- Containment of a compromised agent (separate topic).

## Decomposition
1. **F1** `pkg/localauth`: token file, mode and owner checks, flock, constant-time
   check, middleware, scoped-token registry.
2. **F2** Data Service behind the door; `dataclient` and every Data caller send the
   token.
3. **F3** Router behind the door: static UI and `/health` exempt; `/auth/enter` plus
   one-time codes.
4. **F4** Children: `MADS_LOCAL_TOKEN` in, removed from their env; agent env pinned
   clean; scoped `MADS_ROUTER_TOKEN` for orchestrate; actor taken from the token.
5. **F5** CLI: every subcommand, plus `mads api`, `mads open`, `mads token rotate`,
   `mads hooks refresh`.
6. **F6** Git hook v2 plus a refresh.
7. **F7** UI: the Electron cookie, `linkOpener`'s bearer, a 401 screen, the Vite
   proxy.
8. **F8** e2e harness, `app.sh`, docs.
9. **F9** Children on Unix sockets with the same-user check (D9); Router transport;
   `mads api --session`; `mads ps`; hand-run TCP dev mode; docs and the smoke
   harness.

Tests:
- Every refusal path (no token, wrong token, wrong owner or mode, a scoped token off
  its routes, a spoofed `X-Mads-Actor`).
- The agent env never holds either token (inherit and isolated).
- A cookie journey, and a one-time-code journey in Playwright.
- The hook reports with the token.
- Rotation logs out old tokens.

## Decisions log
- 2026-11-16: Option 1: one install token, bearer or cookie; Unix sockets rejected
  as a false choice for this threat (Drew).
- 2026-11-16: Per-session scoped tokens for orchestrate (hygiene); keep plain-browser
  access via one-time URL; `mads api`; hook v2 plus a one-time refresh; branch from
  main (Drew).
- 2026-11-16: Security against a determined agent is a separate conversation (Drew).
- 2026-11-16: Round-1 hardening H1–H4 (see that section); the plain-browser cookie
  is a dev-only testing path (Drew).
- 2026-11-16: Children move to Unix sockets with a same-user check, plus `mads api
  --session`, `mads ps`, a hand-run TCP mode and agent-facing docs. Tracing later;
  the Data Service socket later; no tap. The cookie stays as the testing path (Drew).
- 2026-11-17: H5 (1h browser sessions) and H6 (document orchestrate after
  rotation) — then superseded in review by H7: no cookie, no plain-browser access;
  Electron and Playwright inject the bearer below the page (Drew).
