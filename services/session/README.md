# Session Service

One process per active session, hosting that session's provider session (spec §15): starts
or resumes it, relays user turns onto it, streams its events back out, and keeps the Data
Service's session record (`Activity`, `ProviderRef`) in sync. Spawned and supervised by the
Router on an ephemeral port. It does NOT persist state directly — every write goes through
the Data Service. It owns worktree-per-session isolation and merge-back (spec §6, §17),
tracks the files the agent's tools touch, and installs the mads post-commit hook (spec §5)
in its working tree, so interactive commits report to the Data Service with task linkage
the same way pipeline task commits do — see "File observation & commit linkage" below. It
never talks to other sessions.

## Run it alone

```sh
go run . -session-id <id> -data-url http://localhost:8081   # -addr to pin the port
curl localhost:<logged port>/health
```

`-fast-model` names the registered provider backing the synthesis engine's Fast
Model calls (default `fake`; real model selection is a later settings-service
concern, spec §10).

`-mads-home` (env `MADS_HOME`, default `~/.mads`) roots the local credential store
(`credentials.json`) and the system-wide MCP server registry (`mcp/*.pid`) — see
"Capability runtime" below.

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
- `POST /merge` — `{"target_branch": "..."}` (target_branch optional; empty merges into
  whatever's currently checked out on the primary). 409 if the session has no isolated
  worktree (`runner.ErrNoWorktree`). Otherwise always 200 with a
  `contracts.MergeResult`: `{"conflicted": bool, "branch": "...", "files": ["..."]}`.
  A conflict is a structured, success-shaped response for the UI's resolution flow — not a
  failure status; the UI branches on `conflicted`, not the HTTP status.
- `GET /output` — SSE feed of `provider.Event` (`id: <seq>\nevent: <kind>\ndata: <json>`).
  Ephemeral for chat events: no replay/cursor, just whatever the connected UI catches live.
  Durable history (transcript, activity) is the Data Service's job, not this stream's.
  Renamed from `/stream`: this channel now carries both chat events and terminal pty output
  (see `/transport` below), so the name shouldn't imply chat-only. One exception to
  "ephemeral": terminal (PTY) output (`event: pty_output`) is buffered server-side in a
  bounded 4 MiB ring (`runner/hub.go`, tmux-attach semantics) and replayed as a single
  leading `pty_output` frame on every new attach, before any live events — this is what
  lets a UI reattaching to a session mid-turn (or after missing PTY output entirely) restore
  terminal scrollback instead of losing it. Chat events (`text`, `tool_use`, `turn_end`,
  `error`) never go through this buffer and stay purely ephemeral.
- `POST /transport` — switches this session between its `chat` and `terminal` transports.
  Body `{"mode": "chat" | "terminal"}`. Chat and Terminal are two transports over one
  provider conversation, not simultaneous mirrors: the old transport's provider process is
  fully terminated and reaped before the new one starts with `--resume <ref>` — enforced
  deterministically by `providers/fake`'s double-attach guard in tests, not by timing.
  Responses:
  - `200 {"mode": "...", "ref": "..."}` on success.
  - `409 {"error": "...", "code": "busy"}` if a turn is in flight (mid-turn switches are
    refused, matching `/messages`' single-turn-at-a-time rule).
  - `502 {"error": "...", "code": "resume_failed", "retryable": true}` if resuming the
    provider on the new transport fails after the old transport has already closed — the
    session is left with no active transport until a retry succeeds.
  - `503` if `Start` hasn't completed yet.
- `POST /input` — raw body bytes, written directly to the terminal transport
  (`Runner.TerminalWrite`). 400 if the session isn't currently in `terminal` mode
  (`ErrNotPTYMode`); 503 if not ready.
- `POST /resize` — `{"cols": int, "rows": int}`, resizes the terminal transport
  (`Runner.TerminalResize`). Same error mapping as `/input`.
- `GET /files` — every file this session has touched, straight through from the Data
  Service (`[]contracts.FileChange`). See "File observation & commit linkage" below.
- `GET /commits` — every commit this session has produced, with task linkage resolved by
  the Data Service (`[]contracts.Commit`). See "File observation & commit linkage" below.
- `GET /transcript` — this session's live conversation (`[]contracts.TranscriptMessage`),
  read straight from the bound provider instance this process owns (`Runner.Transcript`).
  The Router proxies here for any session with a running child — its own
  `provider.Registry` has no visibility into this process's provider session, so this is
  the only place a live turn's history can be read from. Never errors outward: degrades to
  `[]` the same way the Router's own durable-transcript fallback does.
- `POST /skills/{name}/inject` — sends a declared skill capability's content as a turn
  (spec §12.4); 202 once relayed. The system-directed and `/command` injection paths both
  resolve to this one endpoint; user-directed injection needs no separate route (asking the
  agent in free text is a UI/agent concern, not this service's).
- `GET /capabilities/{name}/diagnose` — runs a live health check for a capability (spec
  §12.7, "guided diagnostics") and returns `{"healthy": bool, "reason": "...",
  "suggestions": ["..."]}`. Not a cached read: it self-heals the recorded status as a side
  effect via the same `capability.health_changed` transition rule as session start.

Counterparties (spec §15: exactly two):

- **Router** — spawns this process (`-addr`, `-session-id` argv) and supervises it: SIGTERM
  for a clean stop, and on unexpected process exit it releases this session's leases and
  emits the crash event itself. This service's own clean-shutdown path releases leases and
  flips `Activity` proactively so the common case doesn't wait on the Router noticing.
- **Data Service** — session state (`Session`, `UpdateSession`), lifecycle events (`Append`
  `session.provisioning` / `session.activity_changed`), lease release (`ReleaseAll`), file
  changes and commits (`RecordFileChange`/`FileChanges`, `RecordCommit`/`Commits`), agent and
  capability registry reads (`Agent`, `Capability`) and health reporting
  (`SetCapabilityHealth`), all through `pkg/dataclient`.

## Critical paths

- **Live transcript reads cross the process boundary at the child, not the Router** — a
  live turn's history only ever exists inside the provider session this process owns; the
  Router's own `provider.Registry` is a separate instance in a separate process with no
  visibility into it. `GET /transcript` (`Runner.Transcript`) is what makes a live session's
  conversation readable at all — the Router proxies to it (mirroring `/output`) whenever
  `SessionAddr` finds a running child, falling back to its own durable-provider read (keyed
  by the persisted `ProviderRef`) only once this process has exited. See
  `internal/runner.TestTranscript` and the Router-level topology test
  `services/router/internal/api.TestSessionTranscriptProxiesLiveChild`.
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
- **Synthesis engine** (spec §4, `internal/synth`) — the note-taker: watches the
  transcript delta since its last pass and, on trigger, spawns a Fast Model call
  drafting tasks and notes, written through `pkg/dataclient` under the actor
  `"synthesizer"` with draft provenance (`Task.Draft`/`Note.Status`). Mode
  (`off`/`light`/`full`) is `Session.SynthesisMode`, read once in `Runner.Start` —
  a mode change mid-session has no effect until the session is restarted. Off never
  constructs an `Engine` at all (nil-safe no-ops throughout `runner.go`), so cost and
  behavior are structurally guaranteed, not just policy. Triggers are classified
  structurally, no LLM judgment: a write-tool use or a user confirmation phrase
  fires immediately in any mode; three-or-more tool uses in one turn fires in Full
  mode only, subject to a debounce (elapsed time or turn count since the last fire,
  whichever comes first). Full mode keeps every drafted task/note; Light mode keeps
  only those above a confidence threshold. See the golden trigger/mode tests in
  `internal/synth/engine_test.go` and the Runner-level
  `internal/runner.TestSynthesisEndToEnd`.

- **Capability runtime** (spec §12, §13, `internal/runner/capability.go`, `pkg/capability`,
  `pkg/credstore`) — `Start` calls `setupCapabilities` right after the worktree/hook setup
  and before the provider session starts, resolving `Session.Agent`'s declared capabilities:
  - **Skills** — installed into the session's `cwd` (`.claude/skills/<name>` symlink,
    `capability.InstallSkill`) and health-checked (directory exists). Their resolved
    credential env vars are the *only* thing `setupCapabilities` returns — this return value
    becomes `StartOptions.Env`, the agent's own process environment.
  - **MCP servers** — started system-wide via `MCPRegistry.EnsureStarted` (a local
    `~/.mads/mcp/<name>.pid` file, `O_CREATE|O_EXCL`-claimed so any Session Service process
    can be the one that starts it first; stays running across sessions until explicitly
    stopped — no daemon, no flock, see `pkg/capability`'s package doc for the documented,
    accepted race). Their resolved credential env vars go *only* to `EnsureStarted`'s own
    `env` argument (the spawned server's process), never into the returned agent env.
  - **The security boundary is structural, not filtered**: `setupMCP`'s env variable and
    `setupSkill`'s returned env are two separate code paths that are never merged — an agent
    can only ever observe a credential belonging to a capability it declared as a *skill*.
    A capability declared only as an MCP server, or not declared at all, cannot appear in the
    agent's environment by construction. See
    `internal/runner.TestSetupCapabilitiesScopesEnvByDeclaration`, which inspects both the
    returned agent env and a real spawned MCP child process's actual environment (via
    `pkg/capability/testdata/envdump`) to prove this in both directions.
  - **Missing credentials degrade, never crash** — `CredStore.ResolveEnv` splits requested
    names into resolved pairs and a `missing` list; any capability with a missing credential
    is reported unhealthy (`"missing credential: <name>"`, never a value) via
    `dataclient.SetCapabilityHealth` and simply left out of the session, which starts
    normally regardless. See `internal/runner.TestSetupCapabilitiesMissingCredentialDegradesGracefully`,
    which also asserts a resolved credential's *value* never appears in any event on the log.
  - **`capability.health_changed` fires only on a genuine transition** (spec §12.6) — the
    Data Service only emits when a capability's stored healthy/unhealthy flips, so repeated
    reports of the same status (the common case, once per session start) don't flood the
    event log. See `internal/runner.TestSetupCapabilitiesHealthChangedFiresOnTransition`.
  - Both `CredStore` and `MCPRegistry` are nil-safe: unset (as in every pre-existing test),
    `setupCapabilities` is a no-op and returns `nil` — existing callers are unaffected.

## Worktree isolation & merge-back (spec §6, §17)

A session gets its own git worktree and branch on demand, mirroring the pipeline engine's
per-task isolation (`services/pipeline/engine/worktree.go`) via a separate, copy-adapted
package (`internal/worktree`) rather than a shared one — the two services' release timing
isn't coupled. The exception is the post-commit hook: that is a wire contract with the
Data Service, so both services install the one true copy from `pkg/githook` rather than
carrying their own. For an *un-isolated* session (no `-worktree-root`), hook installation
only fires once `Cwd` has been pointed at an actual session-specific git repo (i.e. isn't
left at its default `"."`) — see `Runner.installHook`.

- **Opt-in via `-worktree-root`** — empty (the default) disables isolation entirely, so
  every existing deployment and test is unaffected until the Router is updated to pass it.
  When set, `Start` isolates into a worktree under it the first time a session with
  `Isolation` unset or `"worktree"` starts, unless `Cwd` isn't a git repo (isolation is
  primary-repo-only; a session opened on a plain document folder just runs in `Cwd`
  directly, silently) or `Isolation` is `"current_dir"` (explicit opt-out).
- **`Session.WorktreeDir`/`WorktreeBranch`** — set once isolation kicks in, persisted
  alongside `ProviderRef`. A resumed (bounced) session reuses the existing `WorktreeDir`
  rather than recreating it. Non-empty `WorktreeBranch` is the UI's signal that the
  session — active or inactive — has isolated work not yet merged back; there's no separate
  bool, since one would only ever drift out of sync with it.
- **`POST /merge`** — attempts the merge (see API contract above). A clean merge commits any
  stray uncommitted changes first, lands the branch on the primary with `git merge --no-ff`,
  removes the worktree and branch, and clears the session's worktree fields. A conflicted
  merge aborts (`git merge --abort`) leaving the primary untouched, and keeps the worktree
  and branch for a retry or manual resolution.
- **Events** — `merge.attempted` (before the attempt), then either `merge.completed` (clean)
  or `merge.conflicted` (aborted, includes the conflicting file list), scoped to the session.
- **Known gap, not solved here** — a clean merge removes the worktree directory that the
  live provider process may still have as its cwd; if the agent is mid-turn when merge-back
  runs, later relative-path operations in that process could fail. Restarting the provider
  session pointed at the primary tree post-merge would fix this properly but is out of scope
  for W4.5.

