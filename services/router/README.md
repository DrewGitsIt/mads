# Router

The always-running coordinator and the UI's single entry point (spec §15). Spawns and
supervises Session Service and Pipeline Engine child processes (supervision is the primary
liveness signal — on child exit it releases leases and emits the failure event), proxies
control-plane event subscriptions to the Data Service, and forwards ephemeral data-plane
agent-output streams to the UI. It does NOT persist anything and holds no business logic
about what sessions or pipelines do — all state access goes through `pkg/dataclient`
against the Data Service; it never calls a sibling service peer-to-peer.

**Status: W4.1, W4.2, W4.6, W5.3, W5.4, W5.5, and W7.5 complete.** Child process supervision
(`internal/child`, `internal/supervisor`), the lifecycle HTTP API, and stream fan-out
(`internal/api`) are built and tested against real spawned processes, real SSE
connections, and a real Data Service. `internal/job` adds the spawn-run-exit pattern for
transient Fast Model work (see below); `POST /agents/generate` (W5.3, spec §9.5–9.6) is
its first caller, spawning `pkg/agentgen` draft generation (task-context or NL-description
driven) and delivering the result as an `agent.draft_generated` event. `internal/supervisor`
also spawns a `session_summary` job (W5.4, spec §16) on every session-child exit, deriving
the session's compressed narrative from task data and writing it to `session.summary`.
`POST /retros` (W5.5, spec §7) spawns the retro engine — a cancellable job that generates
DPAs (Durable Preventative Actions) over user-selected sessions via `pkg/retro`, persisted
by the Data Service; `POST /jobs/{id}/cancel` cancels any running job generically, and
`POST /retros/{id}/follow-up-session` turns a retro's accepted DPAs into a new session's
tasks.

## Run it alone

```sh
make run        # listens on localhost:8080 (-addr to change)
curl localhost:8080/health
```

Flags: `-data-url` (default `http://localhost:8081`) points at the Data Service;
`-session-cmd`/`-pipeline-cmd` are the commands used to spawn children, defaulting to a
same-directory sibling binary (`session`/`pipeline`) of the running router executable if
one exists — the layout `make build` produces — else falling back to `go run
./services/session` / `go run ./services/pipeline` for the dev loop. `-ui-dist` points at
the built UI (see "Serving the UI" below), defaulting to the cwd-relative `ui/dist` —
unlike `-session-cmd`/`-pipeline-cmd` this is not resolved relative to the router
executable, so it's only correct when launched from the repo root (`scripts/app.sh` and
`make run` both do this). See "Critical paths" below.

## Test it

```sh
make test       # -race always
```

`internal/child` and `internal/supervisor` spawn a real OS process — a throwaway
`internal/testchild` binary that follows the same `-addr`/"listening on `<addr>`"
readiness contract as the real services, built once in `TestMain` — and kill it directly
(bypassing `Terminate`) to prove supervision, not just the code path a graceful shutdown
would take. `internal/api` runs the full HTTP surface against a real Data Service via
`services/data/datatest`. Nothing here is mocked.

Every test-constructed `Supervisor`/`child.Process` registers a `t.Cleanup` that
calls `Shutdown`/`Terminate` as a backstop, so a test that returns early still
reaps its spawned `testchild`. `TestMain` in `internal/child`, `internal/supervisor`,
and `internal/api` then fails the package if any spawned `testchild` process is
still alive after the run (via `internal/testleakcheck`), turning a silent leak
into a test failure.

## API contract

Wire types live in `pkg/contracts`. Counterparties: the UI (HTTP, and eventually SSE) and
the Data Service (via `pkg/dataclient`); children (Session/Pipeline) are spawned processes,
never called peer-to-peer.

- `GET /health` — `{"service":"router","status":"ok"}`
- `GET /version` — `{"version": "..."}`, the linker-set build version (`-ldflags
  -X main.version=...`, `"dev"` in local builds). `cmd/mads update apply`'s health check
  after a binary swap calls the *binary* directly (`mads --version`), not this route — this
  one is what the Settings panel reads to show "you're on vX.Y.Z" without a network call.
- `POST /update/check` — the only place a release feed is ever contacted (spec §14.3): no
  polling, no auto-phone-home, this route only runs when the UI/CLI calls it. Reads
  `Config.UpdateFeedURL`; empty (the default — no release feed exists yet) short-circuits
  to `{"current_version", "detail": "no update feed configured"}` without any outbound
  request. Otherwise fetches and parses the feed via `pkg/update.Check`; a fetch/parse
  failure never turns into an HTTP error — same "never a hard error, always data"
  philosophy as `/preflight` — it comes back 200 with a `detail` explaining why. Success:
  `{"current_version", "latest_version", "update_available", "notes"}` (`pkg/update.CheckResult`).
- `GET /preflight` — per-check environment health (git, Data Service reachability, disk
  space, registered provider binaries + versions against `pkg/preflight`'s compatibility
  matrix), each with `{name, healthy, detail?, fix_hint?}` (spec §14.8-9); no request body,
  always 200 unless the checker itself errors (never does, today).
- `POST /sessions` — create a session (proxies `dataclient.CreateSession`) and spawn its
  Session Service child; 201 with `{"session": ..., "addr": ...}`. On spawn failure, the
  session record is rolled back (best-effort delete) so a failed create doesn't leave an
  orphaned record with nothing backing it. Before either step, an empty
  provider/model/agent/isolation/trust is resolved (spec §10's inheritance chain: session
  → named workspace's `Defaults` → global `Config`) and the resolved values are what gets
  persisted and spawned with — the UI sees what actually ran, not what it left blank. An
  unknown/still-empty provider after resolution is rejected with 400 naming it, before any
  session record exists or any child is spawned — never a spawned-then-crashed child
  surfaced as a bare 502. `POST /retros/{id}/follow-up-session` resolves and validates the
  same way for the same reason.
- `POST /sessions/{id}/terminate` — stop the child (SIGTERM, grace, SIGKILL); 204. The
  session record survives, left inactive, with a summary generated shortly after (session
  summaries are triggered by the child-exit hook — see "Critical paths" below). This is
  what the UI calls "Close" ("I'm done for now"). If no child is tracked under the session
  (e.g. it was orphaned by a prior Router restart — Supervisor state is in-memory only),
  this is truth-enforcement, not a no-op: a still-`active` record is flipped to `inactive`
  and the same summary job spawned, so Close always resolves an orphaned-active session
  instead of hanging forever waiting for an SSE event that will never come — see "Critical
  paths" below.
- `POST /sessions/{id}/resume` — spawn a fresh child bound to an existing, inactive session
  record; 200 with `{"addr": ...}`. Provider continuity (`--resume`-style) comes from the
  record's `ProviderRef`, handled entirely inside the Session Service child on startup, not
  by this handler. 409 if the session is already active or still has a tracked child.
- `POST /sessions/{id}/bounce` — terminate and respawn under the same session ID/lease
  holder; 200 with the new `{"addr": ...}`.
- `DELETE /sessions/{id}` — hard-delete an inactive session record and its orchestration
  artifacts (tasks and their deps/handoffs/notes, file-change history, commit records —
  see `store.Sessions.Delete`'s doc comment for the exact cascade; provider transcripts and
  the event log are not session-scoped and survive). 204. 409 if the session is still active
  or has a tracked child — close it first.
- `POST /pipelines` — body `{"session_id", "template_id"}`, mints a `run_<hex>` ID, spawns
  a Pipeline Engine child; 201 with `{"run_id", "addr"}`.
- `POST /pipelines/{id}/cancel` — stop the child and append `pipeline.cancelled`; 204.
- `GET /events` — the UI's single control-plane subscription point (spec §15). SSE only
  (`Accept: text/event-stream`, no batch/JSON mode — the Data Service already serves that
  directly for one-shot fetches). Query params `since`/`session_id`/`types` are passed
  straight through to `dataclient.Stream`; each envelope is re-emitted with the same
  `id:`/`event:`/`data:` framing the Data Service uses, so a reconnecting client's
  `since=<last id>` resumes without gaps whether it's talking to the Data Service directly
  or through Router.
- `GET /sessions/{id}/output`, `GET /pipelines/{id}/output` — the UI's data-plane
  subscription point: a byte-transparent reverse proxy to the running child's own
  `GET /output` SSE endpoint. 404 if the session/pipeline isn't currently running.
- `POST /sessions/{id}/transport`, `POST /sessions/{id}/input`, `POST /sessions/{id}/resize`
  — chat/terminal transport handoff for a session's single provider conversation (Chat and
  Terminal are two transports over one ref, not simultaneous mirrors). Pure reverse proxies
  to the running child's own `POST /transport`, `POST /input`, `POST /resize`, same pattern
  as `/messages`/`/interrupt`: 404 if the session isn't running, otherwise the child's
  response passes through untouched, including its 409 (switch refused mid-turn) and 502
  (resume failed after the old transport closed) — the Router does no status-code
  special-casing on this path. `/transport` body is `{"mode": "chat"|"terminal"}`; `/input`
  body is the raw bytes to write to the terminal, not JSON-wrapped; `/resize` body is
  `{"cols", "rows"}`.
- `POST /agents/generate` — body `{"session_id", "task_id", "title", "description",
  "labels", "required_tools", "required_mcp_servers", "required_skills"}`; `title` set
  drives task-context generation (the auto-select-found-no-match fallback), `description`
  alone drives NL-description generation (the Library editor's "describe what you want"
  path) — 400 if neither is set. Spawns a `job.Runner` job and returns 202 with
  `{"job_id": ...}` immediately; the draft (plus a near-duplicate warning, if any) is
  delivered asynchronously as an `agent.draft_generated` event, observed via `GET /events`
  like any other job.
- `GET /agents/{id}`, `POST /agents`, `PUT /agents/{id}`, `DELETE /agents/{id}` — pure
  proxies over `pkg/dataclient`'s agent CRUD (W6.8, MADS-UX §3.5, the Library's Agents
  shelf). `id` resolves as ID first, then name, mirroring the Data Service's own `lookup`.
- `POST /agents/describe`, `POST /agents/suggest-capabilities`, `POST /agents/refine` —
  the Library editor's enrichment actions (W6.8, spec §9.5), bodies `{"prompt"}` /
  `{"prompt"}` / `{"prompt", "instruction"}`. Unlike `/agents/generate`, these are
  synchronous request/response — fast, side-effect-free, single Fast Model calls with no
  job/event plumbing, calling `pkg/agentgen.Generator` (built over `s.jobs.FastModel()`)
  directly and returning the result in the response body (`{"result": ...}` for
  describe/refine, the raw `AgentCapabilities` shape for suggest-capabilities).
- `GET /capabilities`, `GET /capabilities/{id}`, `POST /capabilities`, `PUT
  /capabilities/{id}`, `DELETE /capabilities/{id}` — pure proxies over
  `pkg/dataclient`'s capability CRUD (W6.8, the Library's Capabilities shelf). `GET
  /capabilities` incidentally fixes a pre-existing gap: `RouterClient.getSkillCapabilities`
  (the Conversation tab) has called this route since an earlier task even though it didn't
  exist on the Router until now.
- `POST /capabilities/{id}/diagnose` — one-off health check plus troubleshooting
  suggestions (spec §12.7), proxying a new Data Service endpoint of the same shape; 200
  with `{"healthy", "reason", "suggestions"}`. Local-command MCP capabilities always
  diagnose "not running" here — no session context to ask a live `MCPRegistry`, documented
  limitation, not a bug.
- `GET /workspaces/{id}`, `POST /workspaces`, `PUT /workspaces/{id}`, `DELETE
  /workspaces/{id}` — pure proxies over `pkg/dataclient`'s workspace CRUD (W6.8, the
  Library's Workspaces shelf); `GET /workspaces` (list) already existed.
- `GET /templates`, `GET /templates/{id}`, `POST /templates`, `PUT /templates/{id}`,
  `DELETE /templates/{id}`, `POST /templates/validate` — pure proxies over
  `pkg/dataclient`'s template CRUD (W6.8, the Library's Templates shelf, M9). `GET
  /templates/{id}` returns `{"template", "used_by"}` — the workspaces bound to it.
  `POST /templates/validate` runs the same `pkg/template.Check` the Pipeline Engine uses at
  launch, so "Validate" in the editor is literally the launch judge; a 400
  `*contracts.ValidationError` carries every failing check's `{field, message}` together
  (`stages[0].name`, `stages[1].on_fail.loop_back`, etc.), not just the first.
- `POST /retros` — body `{"session_ids", "user_observations"?}`; 400 if `session_ids` is
  empty. Mints the retro ID before spawning (so it can be handed back immediately and so
  every `retro.*` event the job emits carries it for correlation), spawns a
  `job.RetroKind` job, returns 202 with `{"job_id", "retro_id"}`. Progress
  (`retro.started`/`retro.progress`/`retro.completed`/`retro.failed`/`retro.cancelled`) and
  the final DPA set are observed via `GET /events` and `dataclient.Retro`, like any other
  job.
- `POST /jobs/{id}/cancel` — cancels any running job generically (not retro-specific, so
  any future cancellable job kind gets it for free); 204, or 404 if the job isn't running
  (already finished or never existed).
- `POST /retros/{id}/follow-up-session` — turns a retro's non-rejected DPAs into a new
  session's tasks (spec §7): one task per DPA, `Source: "retro"` and `DPAID` set for
  traceability; the new session is tagged `Tags: ["retro"]` with `RetroID` set (this is
  also how "retro-of-retros" works — it's not a separate mode, just selecting a
  retro-tagged session as an input to another `POST /retros` call). 400 if every DPA is
  rejected (nothing to create). Mirrors `POST /sessions`' spawn-then-rollback-on-failure
  pattern; 201 with `{"session", "addr", "tasks"}`.

- `POST /tasks`, `POST /tasks/import`, `GET /tasks` (`session_id`/`status` query params),
  `GET /tasks/cycles` (`session_id` query param), `GET /tasks/{id}`, `PUT /tasks/{id}`
  (full-replace, no partial PATCH), `POST /tasks/{id}/confirm`, `POST /tasks/{id}/discard`,
  `POST /tasks/{id}/notes`, `POST /tasks/{id}/notes/{noteId}/confirm`, `POST
  /tasks/{id}/notes/{noteId}/discard` — pure proxies over `pkg/dataclient`'s task/note
  methods (W6.4, MADS-UX §3.3). No new business logic or persistence lives here.
- `GET /sessions/{id}/files` — proxies `dataclient.FileChanges` (W6.4, Context Rail's
  Changes tab). Recording a file change (`POST .../files`) is not proxied — that's
  `pkg/githook`/the watcher talking to the Data Service directly, never through the
  Router.
- `GET /sessions/{id}` — the session record (proxies `dataclient.Session`); 404 if unknown.
- `POST /links`, `GET/PUT/DELETE /links/{id}`, `GET /sessions/{id}/links` — thin
  proxies over `pkg/dataclient`'s link methods (`docs/LINKED-SESSIONS-DESIGN.md`).
  All five routes 404 unless `Config.FeatureFlags.LinkedSessions` is set — checked
  per-request (`requireLinkedSessions`), not baked into the mux at startup, so
  toggling the flag takes effect without a restart.
- `GET /sessions/{id}/transcript` — the session's conversation history. Proxied through the
  running child, like `/output`, whenever one exists (`s.sup.SessionAddr`): a live turn's
  history only ever exists in the child's own provider instance, not the Router's own
  `provider.Registry`, so this is the only way a live session's transcript reflects its
  actual conversation. Falls back to a direct read from the bound provider via the
  persisted `ProviderRef` (`Session.ProviderRef`'s documented "transcript pointer") only for
  sessions with no running child. Always 200 with a JSON array (`[]` for a session with no
  interactive conversation, e.g. a pipeline-only run, or a provider that doesn't implement
  transcript reading); 404 only for an unknown session id.

Errors: `dataclient.ErrNotFound` → 404, `dataclient.ErrConflict` → 409,
`*contracts.ValidationError` → 400 (this is a deliberate divergence from the Data
Service's own 422-for-validation convention — the Router treats a rejected request the
same whether the rejection came from its own decoding or a proxied dataclient call).

## CORS

The Router serves the UI's HTTP surface, but the Vite dev server runs on its own
origin (`localhost:5173`) and calling the Router (`localhost:8080`) cross-origin
would otherwise be blocked by the browser (Electron isn't affected — it doesn't
enforce Origin). `internal/api/cors.go` wraps the whole mux with a default-on,
localhost-only CORS policy — no flag/env to toggle it off:

- Matches `http://` or `https://`, host `localhost` or `127.0.0.1`, any port.
  Echoes the specific origin back via `Access-Control-Allow-Origin: <origin>` —
  never a wildcard.
- Non-localhost origins get zero CORS headers.
- Allowed methods: `GET, POST, PUT, DELETE` (plus `OPTIONS` for preflight).
  Allowed headers: `Content-Type`, `X-Mads-Actor`.
- `OPTIONS` is answered directly by the middleware (204, no body) for every
  request, matching origin or not — the mux has no route registered for it.
- `GET /events` (SSE) works cross-origin the same way: the middleware sets
  headers before `next.ServeHTTP` runs, so they're on the wire before
  `handleStreamEvents` starts flushing.

## Serving the UI

The Router serves the built UI (`ui/dist`, or wherever `-ui-dist` points) directly at `/`
(`internal/static`), so the packaged app is same-origin with its own API — Electron loads
`http://localhost:8080` via `win.loadURL`, not `file://`, and no longer needs
`webSecurity: false` (`file://` sends `Origin: null`, which the CORS policy above would
otherwise refuse). `internal/static` is a separate package from `internal/api` on purpose —
it's pure file-serving with no JSON/dataclient dependency — and the UI itself is not
embedded into the Go binary: it rebuilds independently (`npm --prefix ui run build`), and
the Router picks up a new build without a restart.

- `main.go` constructs `static.New(*uiDist)` and passes the resulting `http.Handler` into
  `api.New`, which registers it as a catch-all `"/"` pattern on the same `http.ServeMux` as
  every API route. `net/http`'s enhanced routing always ranks a method+path pattern (e.g.
  `GET /health`) above a bare `"/"`, so API routes take priority with no explicit ordering
  required — `internal/api`'s `TestStaticDoesNotShadowAPIRoutes` pins this. A nil
  `staticHandler` (every pre-existing `internal/api` test call site) defaults to
  `http.NotFoundHandler()`, matching the pre-W7.5 behavior of having no `"/"` route at all.
- The fallback only serves `index.html` for a *navigation-shaped* request — GET/HEAD, no
  file extension, and an `Accept` header that prefers `text/html` — so a real browser
  navigating to a client-side SPA route (e.g. `/deep/link`) gets the SPA shell
  (`internal/static`'s `TestUnknownPathFallsBackToIndex`). Everything else that reaches the
  fallback (a bare `fetch()`, whose default `Accept` is `*/*`; an extensioned path with no
  matching file; a non-GET/HEAD method) gets a JSON 404
  (`{"error":"not found","path":...}`) instead of silently 200'ing as HTML — this is what
  stops a missing/typo'd API route from being masked as an SPA page and poisoning JSON
  parsing on the caller side (`TestNonHTMLAcceptGetsJSON404`,
  `TestBareFetchDefaultAcceptGetsJSON404`, `internal/api`'s
  `TestAPIShapedUnregisteredPathGetsJSON404`). `internal/api/route_manifest_test.go`
  separately cross-checks every route `ui/src/router/client.ts` calls against the mux's
  registered patterns, so a missing API route fails a test instead of silently falling back
  to this handler.
- Whether the UI is built is checked on every request, not cached once at construction:
  `scripts/app.sh` starts the Router before running `npm --prefix ui start` (which builds
  `ui/dist`), so a handler that only checked once at construction would keep serving a
  stale placeholder forever once the build actually landed
  (`internal/static`'s `TestPicksUpBuildThatLandsAfterConstruction`).
- A missing/unbuilt dist directory degrades to a plain "UI not built" page instead of
  failing Router startup (`TestPlaceholderWhenDistMissing`, `TestPlaceholderWhenDistPathEmpty`).
- The Vite dev-server loop (`npm run dev`, `localhost:5173`) is unaffected — that's what
  the CORS policy above is for — and Electron's own dev fallback still points at
  `VITE_DEV_SERVER_URL` when set (`ui/electron/main.ts`).

## Critical paths

- **Model-tier mapping is a documented simplification, not real tier selection.**
  `resolveSessionDefaults` (`internal/api/sessiondefaults.go`) derives an empty `Model`
  from the *resolved* provider's discovered default (`ModelDiscoverer.Models`, the
  `Default: true` entry) — `Config.DefaultModelTier` (cheapest/balanced/best) is not
  consulted at all yet. Every tier currently maps to "the default provider's default
  model." A real tier→model mapping is future work.
- **Resolve provider before model, always.** Model derivation in
  `resolveSessionDefaults` depends on the already-resolved provider (to look it up in the
  registry and ask what it discovers) — resolving in the other order would derive from an
  unresolved/empty provider.
- **The provider pre-check is trustworthy because it's the same registry the child uses.**
  `handleCreateSession`/`handleRetroFollowUpSession` call `s.providers.Get` — the identical
  `*provider.Registry` instance `services/session`'s own binary populates (mirrored
  construction, `main.go` in both services) — so a 400 here means the real child would
  have failed the same lookup, not a guess.
- **`handleBounceSession` re-spawns an existing record without re-running resolution or
  the provider pre-check** — deliberately: bounce restarts a session that already has
  whatever provider/model it was created with, there is nothing new to resolve. If that
  stored provider has since gone bad (e.g. an operator deregistered it), bounce still
  surfaces the Session Service's crash as a 502, not a 400 — this is a known, accepted gap
  (see the task's HANDOFF for the considered, not-implemented sentinel-propagation
  approach), left for a future change if it proves to matter in practice.

- **Task/note/file-change proxying is a pure pass-through, zero Router-side state** —
  same framing as the rest of this doc's "holds no business logic" claim. `internal/api/tasks.go`
  has no logic of its own: every handler decodes the request, calls straight through to
  `s.data`, and maps the error with the Router's own `writeError` (never the Data
  Service's `writeStoreError`/422 convention). `tasks_test.go` exercises these against a
  real `datatest` Data Service, including the full-replace `PUT` gotcha (a partial update
  would null out unset fields) and the dependency-cycle detector (`GET /tasks/cycles`).

- **Supervision beats the lease TTL backstop, not just a graceful path** —
  `TestExternalKillReleasesLeasesAndEmitsCrashEvent` (`internal/supervisor`) spawns a real
  child, claims a task's lease with a 30s TTL, kills the child's OS process directly (not
  via `Terminate`), and asserts the task is blocked and a `session.crashed` event lands —
  in well under a second, proving it's supervision catching the exit and not the TTL
  expiring underneath it. `TestPipelineExternalKillReleasesLeasesAndEmitsFailedEvent`
  covers the same for Pipeline Engine children and `pipeline.failed`.
- **Orphaned-active sessions are reconciled, not left to rot.** Supervisor state
  (`sessions`/`pipelines` maps) is in-memory only, so every Router restart forgets every
  tracked child while `Session.Activity` in the Data Service still says `active` — nothing
  ever told it otherwise. `reconcileInactiveSession` is the shared fix, called from two
  places: `TerminateSession`'s no-child branch (unconditional — an explicit "Close" click is
  authoritative and always wins, even against a live external `mads run`) and
  `Supervisor.ReconcileOrphanedSessions`, a boot-time sweep `main.go` runs once, synchronously,
  before the server starts accepting connections. The sweep must never fight a genuinely live
  pipeline engine — a session can be legitimately active under a CLI `mads run` that the
  Router never spawned or tracks — so, unlike `TerminateSession`, it gates each orphan on
  `dataclient.SessionActiveLease` (`GET /sessions/{id}/lease-active`, backed by
  `Leases.ActiveForSession`): an unexpired task write-lease under the session is treated as
  proof a live engine is working, and the sweep leaves it active and logs instead of flipping
  it. Task write-leases were chosen over "recent pipeline events" as the liveness signal
  because the pipeline engine heartbeats its lease independently of task writes (see
  `services/pipeline/engine/engine.go`'s `LeaseTTL` comment) — an agent stage can run silently
  for a task's full duration with zero intermediate writes, so absence of recent events does
  not mean absence of a live engine. `TestTerminateReconcilesOrphanedActiveSession` and
  `TestTerminateOrphanedActiveSessionTriggersSessionSummary` prove the Terminate path end to
  end (activity flip, event, summary job); `TestBootSweepReconcilesOrphanedActiveSession`
  proves the sweep heals a true orphan while leaving both a lease-active session and a
  tracked-child session untouched; `TestTerminateOrphanThenResumeEndToEnd`
  (`internal/api`) proves Resume, unmodified, starts working again once Terminate stops
  being a no-op — Resume's existing `Activity == active` / tracked-child 409 guards were the
  reason it looked broken on an orphan, not a bug in Resume itself.
- **Expected exits don't cry wolf** — `Terminate` (and its callers, `TerminateSession` /
  `TerminatePipeline` / `BounceSession`) marks the exit expected *before* signaling, so the
  crash-detection path is skipped: `TestTerminateDoesNotEmitCrashEvent`,
  `TestPipelineCancelDoesNotEmitFailedEvent`.
- **A child's exit handling — including its lease release — fully completes before
  `Terminate` returns.** This matters specifically for `BounceSession`, which respawns a
  new child under the same holder ID immediately after terminating the old one; if the old
  child's `ReleaseAll` ran late, it could release leases the *new* child had just claimed.
  `child.go`'s exit goroutine runs `onExit` before closing the "done" channel `Terminate`
  waits on, deliberately ahead of the ordering implied by a naive read of the exit-handling
  pseudocode.
- **A dead-before-ready child doesn't zombie or leak.** `cmd.Wait()` starts unconditionally
  right after `cmd.Start()` in `internal/child`, not after address discovery, so a child
  that crashes before ever logging "listening on \<addr\>" is still reaped and still fires
  `onExit` — the `exitedCh` branch of `Spawn`'s ready-select.
- **Terminate kills the whole process group, not just the immediate child.** `Spawn` sets
  `Setpgid: true`, making every spawned child its own process group leader; anything it
  forks without calling `setpgid` itself — a `go run` wrapper's compiled binary, a `sh -c`
  chain — inherits that same pgid. `Terminate` (and `Spawn`'s own early-kill paths on
  timeout/context-cancel) signal `-pid` (`internal/child`'s `killGroup`), reaching the
  whole tree. Without this, the default dev `-session-cmd "go run ./services/session"`
  orphaned the compiled grandchild on every termination — the immediate child (`go run`)
  died, but its forked binary kept its listener open. `TestTerminateKillsGrandchildProcessGroup`
  (`internal/child`) proves this against a real wrapper-script-spawned grandchild pid, not
  just `testleakcheck`'s backstop scan. `-session-cmd`/`-pipeline-cmd` default to a
  same-directory sibling binary of the running router executable (`defaultChildCmd`,
  keyed off `os.Executable()` rather than cwd, which is ambiguous across invocation
  styles) if `make build`'s `bin/` layout is present, else the unchanged `go run` dev
  default — so a built install execs the compiled binary directly (no wrapper, no
  grandchild) while the dev loop's behavior is untouched.
- **Control plane: per-subscriber proxy, not a shared multiplexer.** `GET /events` opens
  one independent upstream `dataclient.Stream` call per UI connection — there is no
  Router-side fan-out logic and no shared upstream state between subscribers. Filtering is
  entirely the Data Service's job; Router just passes `since`/`session_id`/`types` through.
  `TestStreamEventsFanoutFilters` (`internal/api`) proves two concurrent subscribers with
  different `session_id` filters each see exactly their own events, in order, and nothing
  else. `TestStreamEventsReconnectWithCursorNoGaps` proves a subscriber that disconnects,
  misses events appended while it's gone, and reconnects with `since=<lastSeq>` resumes
  with exactly the missed events — no gap, no duplicate.
- **Data plane: forwarded, never persisted.** `handleSessionOutput`/`handlePipelineOutput`
  reverse-proxy straight to the child's `GET /output`; the handler never touches `s.data`
  or any dataclient call on this path. `TestSessionOutputForwarding` (`internal/api`)
  spawns a real session child, streams its `/output` through Router, and then asserts the
  Data Service's event log is byte-for-byte unchanged — proving the forwarded tokens never
  reached the Data Service. Multi-viewer fan-out on this plane (two tabs watching the same
  session) is out of scope here: each UI connection gets its own upstream proxy connection
  to the child, so N viewers means N upstream connections, not one shared one. The child
  contract — any Session Service / Pipeline Engine process must expose `GET /output` (SSE)
  on its supervised address once it starts emitting live agent output — is relevant to
  W4.3+.
- **Transient jobs: goroutine-hosted, not a spawned process.** `internal/job` runs Fast
  Model work (spec §15) as an in-process goroutine, not a `child.Spawn`'d binary — nothing
  calls into a job over HTTP the way the UI dials a session's `/output`, so there's no
  readiness contract for `child` to supervise. `Runner.Spawn(ctx, kind, sessionID, fn)`
  mints a job ID, appends `job.queued` synchronously, then runs `fn` on its own goroutine
  and returns immediately; callers observe progress via the event log
  (`job.started`/`job.completed`/`job.failed`), not via `Spawn`'s return. A recovered panic
  in the job body becomes a `job.failed` event — the goroutine equivalent of a crashed
  child being contained and reported rather than taking the process down. A job body's
  only two ways to reach the outside world are `Deps.Data` (`*dataclient.Client`) and
  `Deps.FastModel` (`*providers/fastmodel.Runner`) — never raw storage, never a live
  provider API directly. `job.Noop` is the sample job proving the contract end to end: it
  round-trips both legs (a `Deps.Data.Events` call and a `Deps.FastModel.DoText` call) so
  the test proves the wiring, not just the runner's own bookkeeping. `GET /events` already
  proxies every event type transparently (see above), so job lifecycle events are
  observable over the existing SSE stream with zero Router API changes. `main.go`
  constructs the `job.Runner` over a `providers/fastmodel.Runner`; `-fastmodel-base-url`
  selects an OpenAI-compatible backend, and an unset (default empty) value falls back to
  `providers/fake` with a log message, so the binary still starts and answers requests
  without a real Fast Model endpoint configured.
- **Agent generation is spawn-then-observe, and drafts are never silently persisted.**
  `POST /agents/generate` (`internal/api/agents.go`) is the first caller of
  `job.Runner.Spawn`: it builds a `job.Func` from `pkg/agentgen` (task-context or
  NL-description generation, per spec §9.5–9.6), spawns it under
  `job.AgentGenerationKind`, and returns 202 immediately. The job body calls
  `agentgen.Generator`, which drafts via the Fast Model and then checks the draft against
  every existing agent with `agentselect.FindSimilar` (Jaccard text/capability overlap
  plus a quadrant bonus, thresholded) before delivering `{"draft": ..., "similar": ...}`
  as a single `agent.draft_generated` event — `similar` is populated (not silently
  dropped) when a near-duplicate exists, so a caller can warn instead of minting a
  redundant agent. The draft itself is never written to the agent registry; only an
  explicit later `POST /agents` against the Data Service persists it.
- **Session summaries are triggered by a child-exit hook, not an event subscription.**
  `contracts.Session.Activity` only ever flips to `Inactive` in three places: the Session
  Service child's own graceful `Shutdown` (before it exits), `onSessionExit`'s
  unexpected-exit branch (the child crashed before it could do that itself), and
  `reconcileInactiveSession` (the orphan-reconciliation path above, reached from
  `TerminateSession`'s no-child branch and the boot sweep). Since every session-child exit —
  expected or crash — leaves `Activity == Inactive` by the time `onSessionExit` runs, that
  callback is the trigger point (spec §16: "generated on session deactivation"): it
  unconditionally spawns a `job.SessionSummaryKind` job via
  `job.GenerateSessionSummary`, whose body (`pkg/sessionsummary`) derives the three-section
  narrative from the session's task data — titles, statuses, handoffs, and confirmed
  `decision`-type notes, never transcripts — and overwrites `session.summary` via
  `dataclient.UpdateSession`. Watching `session.activity_changed` over SSE instead would
  need a replay/cursor strategy across Router restarts; the exit hook sidesteps that for
  free. `Supervisor.jobs` may be `nil` (existing tests that don't exercise summaries keep
  passing `nil`); production (`main.go`) always constructs a real one.
  `TestTerminateSessionTriggersSessionSummary` (`internal/supervisor`) proves the graceful
  path end to end against a real `job.Runner`; `job.GenerateSessionSummary`'s own tests
  (`internal/job`) prove the write and, explicitly, that a second run overwrites the first
  rather than appending.
- **Cancelling a retro mid-generation leaves no partial state.** `job.GenerateRetro`
  (`internal/job/retro.go`) makes exactly one Data Service write —
  `deps.Data.CreateRetro` — and only after `pkg/retro.Generator.Generate` (which is
  read-only) fully succeeds, so a cancel at any earlier point guarantees nothing was
  persisted. `Runner.Cancel(jobID)` cancels that job's own `context.CancelFunc`;
  `Runner.run` distinguishes a cancelled job from a genuinely failed one via
  `errors.Is(err, context.Canceled)` and reports terminal events on a fresh
  `context.Background()`-derived context (the job's own ctx is already cancelled and
  can't carry its own cancellation report).
  `TestRetroJobCancelLeavesNoPartialState` (`internal/job`) gates the Fast Model's HTTP
  backend on a channel, waits for a `retro.progress{step:"generating"}` event (proving
  generation reached the model call), cancels, and then inspects the Data Service
  directly: no `Retro` row exists (`dataclient.ErrNotFound`), and no
  `retro.completed`/`job.completed` events were ever emitted.
