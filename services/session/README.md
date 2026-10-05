# Session Service

One process per active session, hosting that session's provider session (spec §15): starts
or resumes it, relays user turns onto it, streams its events back out, and keeps the Data
Service's session record (`Activity`, `ProviderRef`) in sync. Spawned and supervised by the
Router on an ephemeral port. It does NOT persist state directly — every write goes through
the Data Service. It owns worktree-per-session isolation and the live session's Commit and
Land (spec §6, §17; docs/SESSION-WORKTREES-DESIGN.md),
tracks the files the agent's tools touch, and installs the mads post-commit hook (spec §5)
in its working tree, so interactive commits report to the Data Service with task linkage
the same way pipeline task commits do — see "File observation & commit linkage" below. It
never talks to other sessions.

## Run it alone

```sh
go run . -session-id <id> -data-url http://localhost:17374   # -addr to pin the port
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
- `POST /messages` — `{"text": "...", "queue": bool}`. 202 `{"status":"accepted"}` once
  relayed to the provider; 503 if the provider session isn't up yet (`Start` still running
  or failed). A turn already in flight (one turn at a time) is a 409 — unless `queue` is
  set, in which case the message waits in the Runner's queue behind that turn, FIFO with
  any linked-session injections, and the answer is 202 `{"status":"queued","queued":
  {"id","text","queued_at"}}`. The turn-end drain delivers it as an ordinary user turn.
  Every queue change appends `session.queue_changed` carrying the whole remaining queue
  (plus `delivered`, or `failed` + `error`). The queue is in-memory: it outlives the UI
  pane that queued it, not this process.
- `GET /queue` — `{"queued": [...]}`, the queued user messages in delivery order.
- `DELETE /queue/{id}` — drop one before it's delivered; 204, or 404 if it isn't queued
  (it may have just gone out).
- `POST /interrupt` — stops the in-flight turn, if any. 503 if not ready.
- `GET /worktree`, `POST /commit`, `POST /commit/undo`, `POST /land` — the session's
  Commit and Land while it is live (docs/SESSION-WORKTREES-DESIGN.md D3, D5, D6), forwarded
  by the Router, which adds `?pipeline=1` when a pipeline run is active. Bodies are the
  `contracts.WorktreeStatus` / `CommitRequest` / `CommitResult` / `UndoCommitRequest` /
  `LandResult` shapes; the git work is `pkg/sessionwt.Ops`, the same code the Router runs
  for an inactive session. A commit refusal is a `contracts.CommitError` with its status
  (400/409/422); an undo refusal is a 409 `UndoCommitResult`; Land answers 200 for landed,
  conflicted, refused and auto_resolved alike, each a card in the conversation. Land is refused mid-turn,
  holds user messages and injections in the queue while it runs, and `Shutdown` waits for
  it (bounded by the shutdown grace).
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

## Worktree isolation, Commit and Land (spec §6, §17)

The design is docs/SESSION-WORKTREES-DESIGN.md; the git mechanics live in `pkg/sessionwt`,
shared with the Router (inactive sessions, delete cleanup) and the pipeline service (task
merges into a session folder). The post-commit hook is `pkg/githook`, a wire contract with
the Data Service. For an *un-isolated* session, hook installation only fires once `Cwd` has
been pointed at an actual session-specific git repo (i.e. isn't left at its default `"."`),
and never in another session's worktree (a linked session that joined it) — see
`Runner.installHook`.

- **Where** — `$MADS_HOME/session-worktrees/<id>` on branch `mads/session/<id>`, derived
  from `-mads-home` when `-worktree-root` is empty (`"off"` disables isolation). `Start`
  isolates a session on its first start only (no `ProviderRef` yet), when `Isolation` isn't
  `"current_dir"` and `Cwd` is a git repo on a branch; that branch is recorded as
  `Session.WorktreeBase`, the Land target. The worktree fields are persisted before the
  provider starts, and every start re-ensures the folder (`sessionwt.EnsureWithLock`).
- **Commit** — commits the selected session files with the user's own identity and a
  `Session:` trailer, and records the commit itself rather than relying on the hook.
- **Land** — merges the base into the session branch in its folder, then `git merge
  --no-ff` the branch into the user's checkout (in its own process group, so a group kill of
  this process can't stop it mid-write), aborting on any failure. The folder stays; the
  branch fast-forwards to the landed base when it can. When mergiraf (`pkg/mergedriver`,
  registered by every ensure if installed) resolved files plain git would have conflicted
  on, Land keeps that merge in the folder and stops as `auto_resolved`, before touching the
  user's checkout; the next Land carries on.
- **Events** — `commit.created`, `commit.undone`, `merge.attempted`, then `merge.completed`,
  `merge.conflicted`, `merge.refused` or `merge.auto_resolved` (payload `kind: "land"`), and `worktree.ensured`
  when a start repaired or couldn't create the folder; the header counts
  (`uncommitted_count`, `unlanded_count`) are republished after each change.
