# Session Service

One process per active session, hosting that session's provider session (spec §15): starts
or resumes it, relays user turns onto it, streams its events back out, and keeps the Data
Service's session record (`Activity`, `ProviderRef`) in sync. Spawned and supervised by the
Router on an ephemeral port. It does NOT persist state directly — every write goes through
the Data Service. It tracks the files the agent's tools touch and installs the mads
post-commit hook (spec §5) in its working tree, so interactive commits report to the Data
Service with task linkage the same way pipeline task commits do — see "File observation &
commit linkage" below. It does not yet own worktree *setup* (later work item); it also
never talks to other sessions.

## Run it alone

```sh
go run . -session-id <id> -data-url http://localhost:8081   # -addr to pin the port
curl localhost:<logged port>/health
```

`-session-id` is required and fails fast if empty — the Router always supplies it
(`services/router/internal/supervisor.SpawnSession`). The session must already exist in the
Data Service (`POST /sessions`) before this process can start it. `make run` (`go run .`
with no args) will exit immediately for the same reason; use the command above instead.

## Test it

```sh
make test       # -race always; agent behavior comes from providers/fake, never a live LLM
```

Tests run against a real Data Service (`services/data/datatest`, temp-dir SQLite) and
`providers/fake` — never mocks, never a live LLM.

## API contract

One process, one session — no `:id` in any path.

- `GET /health` — `{"service":"session","status":"ok"}`, always 200 regardless of runner
  state (process-liveness probe, not session-readiness).
- `POST /messages` — `{"text": "..."}`. 202 once relayed to the provider; 503 if the
  provider session isn't up yet (`Start` still running or failed); 409 if a turn is already
  in flight (one turn at a time).
- `POST /interrupt` — stops the in-flight turn, if any. 503 if not ready.
- `GET /stream` — SSE feed of `provider.Event` (`id: <seq>\nevent: <kind>\ndata: <json>`).
  Ephemeral: no replay/cursor, just whatever the connected UI catches live. Durable history
  (transcript, activity) is the Data Service's job, not this stream's.
- `GET /files` — every file this session has touched, straight through from the Data
  Service (`[]contracts.FileChange`). See "File observation & commit linkage" below.
- `GET /commits` — every commit this session has produced, with task linkage resolved by
  the Data Service (`[]contracts.Commit`). See "File observation & commit linkage" below.

Counterparties (spec §15: exactly two):

- **Router** — spawns this process (`-addr`, `-session-id` argv) and supervises it: SIGTERM
  for a clean stop, and on unexpected process exit it releases this session's leases and
  emits the crash event itself. This service's own clean-shutdown path releases leases and
  flips `Activity` proactively so the common case doesn't wait on the Router noticing.
- **Data Service** — session state (`Session`, `UpdateSession`), lifecycle events (`Append`
  `session.provisioning` / `session.activity_changed`), lease release (`ReleaseAll`), file
  changes and commits (`RecordFileChange`/`FileChanges`, `RecordCommit`/`Commits`), all
  through `pkg/dataclient`.

## Critical paths

- **Bounce and resume** — the provider session survives this process dying. `Start`
  persists `ProviderRef` (the provider-native resume handle) to the Data Service before
  returning success; a fresh process reads it back and passes it as `StartOptions.Resume`.
  See `internal/runner.TestBounceAndResume` — two `Runner`s sharing one `*fake.Fake`, second
  resumes purely from the `ProviderRef` the first one persisted, and the fake's `"recall"`
  script proves the conversation actually continued rather than just restarting cleanly.
- **Clean shutdown releases leases** — `Runner.Shutdown` closes the provider session, marks
  `Activity` inactive, and calls `dataclient.ReleaseAll` before returning, so a lease this
  session held is claimable by someone else immediately after shutdown, not just after the
  Router's independent crash-path cleanup. See `internal/runner.TestShutdownReleasesLeases`.
- **Crash vs. clean-shutdown detection** — `Runner`'s event pump distinguishes "provider
  process died mid-turn" (the provider's event channel closed while a turn was in flight and
  we didn't initiate the close) from "turn finished, channel closed normally" or
  "we're shutting down on purpose." Only the former closes `Runner.Died`, which `main.go`
  selects on to exit immediately — without releasing leases or touching `Activity` itself,
  since that's the Router's crash-supervision job once this OS process exits. See the
  `busy`/`closing` flags and `pump` in `internal/runner/runner.go`.
- **One turn at a time** — `Send` returns `ErrTurnInFlight` (409) while a turn is in
  progress, matching the composer-disabled chat UX; see `internal/api.TestHTTPRoundTrip` and
  the `busy` flag in `internal/runner/runner.go`.
- **File observation & commit linkage** (spec §5) — `Runner.pump` watches provider
  `EventToolUse` events for a `Tool.Path` and reports each one to the Data Service
  (`dataclient.RecordFileChange`), deduping "added" (first sighting this run) from
  "modified" (`touched` map). `Runner.Start` installs the mads post-commit hook (`pkg/githook`
  — the same package and reporting contract `services/pipeline/engine` uses for task
  worktrees) into `Cwd` and stamps `mads.sessionId`/`mads.dataUrl` into its per-worktree git
  config, so a commit made there reports itself to `POST /sessions/{id}/commits` with the
  session's `X-Mads-Actor` identity; task linkage (trailer → session context → heuristic →
  orphan) is resolved server-side, not here. `GET /files` and `GET /commits` read the
  results straight back from the Data Service — there is no local cache to go stale. See
  `internal/runner.TestFilesTracksToolUsePaths` and
  `internal/runner.TestCommitInSessionWorktreeReportsLinkage`.

## Known gaps

- **Working directory** — `Runner.Cwd` (from `-cwd`) is passed straight to the provider as
  `StartOptions.Cwd`. There's no worktree resolution yet; `contracts.Session` has no
  workdir field beyond `Workspace`. Isolation-mode worktree setup is later work. Because of
  this, hook installation only fires once `Cwd` has been pointed at an actual
  session-specific git repo (i.e. isn't left at its default `"."`) — see
  `Runner.installHook`.
