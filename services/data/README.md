# Data Service

The sole owner of persistent state (spec §15): tasks, sessions, file tracking, registries
(agents, capabilities, workspaces, templates), and the **event log** — the append-only
`events` table that is the system's control plane and its metrics store (§8). Every state
mutation commits together with its event in one SQLite transaction (transactional outbox).
It does NOT talk to providers, spawn processes, or hold business logic about pipelines or
sessions — it stores, validates, and announces.

**Status: Wave 1 COMPLETE (W1.1–W1.7)**, plus **W5.7 (metrics query views)**, the
retro store (W5.5), and **W7.5's boot-time schema compatibility gate** — SQLite store + migrations, event log + transactional outbox + SSE
subscriptions, task store, write leases, registries (six system agents seeded), pipeline
templates (launch-equivalent validation via `pkg/template`, system default seeded),
session records with §5 file-change attribution and the commit-linkage chain, `GET
/metrics/*` dashboard views computed as pure queries over the event log, and `/retros`
CRUD plus per-DPA accept/reject/edit and follow-up-session linkage (spec §7) for the
Router's retro engine (`services/router`'s `job.GenerateRetro`).

## Run it alone

```sh
make run        # listens on localhost:8081 (-addr to change)
curl localhost:8081/health
```

Data lives in `~/.mads/mads.db` by default; override with `-data-dir` or
`MADS_DATA_DIR`. Migrations apply automatically at startup.

## Test it

```sh
make test       # -race always; no external dependencies, temp-dir databases
```

`internal/store` covers open pragmas (WAL, foreign keys), migration up/down
round-trips, atomic failure rollback, and that the production migration set
always round-trips. `internal/api` covers `/health` including the unreachable-
database path (503).

## API contract

Wire types live in `pkg/contracts` (Envelope, Task, ValidationError) with golden fixtures
in `pkg/contracts/testdata/`. Counterparties: the Router (reads/subscribes on behalf of
the UI) and the other services (writes via lease, event append via outbox).

- `GET /health` — `{"service":"data","status":"ok"}`; 503 when the db is unreachable.
- `POST /events` — append a standalone event (envelope minus seq/timestamp); 201 with
  the assigned seq; 422 `ValidationError` listing every problem at once.
- `GET /events?since=<seq>&session_id=<id>&types=task.*,note.created` — filtered,
  seq-ordered. Plain request: one JSON batch (capped at 500; page with `since`).
  With `Accept: text/event-stream`: history replay from the cursor, then live pushes;
  each SSE message's `id` is the event seq, so clients resume with `since=<last id>`.

Task routes (spec §3–§4; mutations carry `X-Mads-Actor`, defaulting to `user`):

- `POST /tasks`, `GET /tasks?session_id=&status=&draft=`, `GET /tasks/{id}`,
  `PUT /tasks/{id}` (full replace — every field is mutable)
- `POST /tasks/{id}/confirm` / `discard` — draft lifecycle; discard is drafts-only
  (cancel real tasks with status `wont_do`)
- `POST /tasks/{id}/handoffs`, `POST /tasks/{id}/notes`
- `POST /tasks/{id}/notes/{noteId}/confirm` / `discard` — same draft lifecycle,
  note-scoped (the synthesis engine's drafted notes, spec §4); discard is
  drafts-only, same rule as tasks
- `POST /tasks/import` — atomic bulk create; intra-batch dependencies allowed
- `GET /tasks/cycles?session_id=` — dependency cycles (legal at rest, block pipeline
  launch, spec §3.6)

Lease routes (spec §15 — holder = `X-Mads-Actor`):

- `POST /tasks/{id}/claim` `{ttl_seconds?}` (default 600) — take or renew the lease;
  409 with the current holder when live-leased by someone else
- `POST /tasks/{id}/release` — idempotent; only the holder may release
- `POST /leases/release-all` `{holder, reason?}` — the Router's supervision call:
  frees everything a dead child held, blocking its mid-flight tasks with events
- Enforcement: while a task is live-leased, every task mutation by a non-holder
  returns 409; each holder write renews the meter. Unleased tasks are writable by
  anyone. The sweep (2s ticker in main) collects expired leases.

Registry routes (spec §9, §12, §17) — uniform CRUD on `/agents`, `/capabilities`,
`/workspaces`: `POST /<base>`, `GET /<base>`, `GET|PUT|DELETE /<base>/{id-or-name}`
(name resolution is how pipeline `agent:` references and workspace bindings find
their targets). System agents are editable but not deletable. Every mutation emits
its `agent.*`/`capability.*`/`workspace.*` event. `GET /agents` backs
`pkg/agentselect`'s candidate pool (spec §9); its selection decisions and stage
outcomes are recorded as `agent.selected` / `agent.outcome_recorded` events through
the general event log, not through this registry CRUD surface.

- `POST /capabilities/{id-or-name}/health` `{healthy, reason?}` — records a capability's
  last-known health (spec §12.6), called by the Session Service's capability runtime
  (`services/session/internal/runner`) on every session start and guided-diagnostics check.
  Emits `capability.health_changed` **only when `healthy` actually flips** from the
  capability's current stored value — same-status repeats (the common case) update `Reason`/
  `CheckedAt` silently, so the event log isn't flooded by every session start reporting the
  same steady state. `reason` is caller-supplied free text; by convention it never carries a
  credential value (spec §13) — this handler stores and forwards whatever it's given, so that
  discipline is enforced entirely by the caller, not here.

Session routes (spec §1, §5): `POST/GET /sessions` (`?activity=` filter),
`GET/PUT/DELETE /sessions/{id}` — updates emit the specific event
(`session.renamed` / `session.activity_changed` / `session.updated`); delete
hard-cascades tasks, file changes, commits, and leases while the event log keeps
history (spec §8). `POST/GET /sessions/{id}/files` — attribution computed by the
§5 rule (`contracts.AttributedChange`: worktree → everything; non-isolated →
tool-use + commits only, observed changes recorded unattributed).
`POST/GET /sessions/{id}/commits` — linkage resolved through the chain: trailer →
session's active task → heuristic candidates → orphan flag; `commit.created`
always, `commit.linked` when a rung resolves.

Template routes (spec §6, M9): same CRUD shape on `/templates`, plus
`POST /templates/validate` `{source}`. Both validate and save run
`pkg/template.Check` — the identical parse + launch checks the Pipeline Engine
uses (unknown agents, missing/forward loop targets, stage shape), reported all at
once. `GET /templates/{id}` includes `used_by` (workspaces bound via defaults);
deletion refuses while bound or for the system default.

Metrics routes (spec §8, W5.7) — the dashboard's data, computed as pure queries over
the event log plus the task and capability tables; no separate metrics store (§15):

- `GET /metrics/cost?session_id=` — usage.recorded events sliced by day×model, by
  session, and by reconstructed pipeline run
- `GET /metrics/agents` — agent leaderboard: usage count, successes/failures,
  success rate, retries, from `agent.selected` / `agent.outcome_recorded` events
- `GET /metrics/tasks?session_id=` — confirmed-task status distribution and
  completion rate (draft tasks excluded)
- `GET /metrics/capabilities` — every registered capability's invocation count,
  including capabilities with zero `capability.invoked` events
- `GET /metrics/pipelines?session_id=` — reconstructed pipeline run history (span
  between `pipeline.started` and its next terminal event)

`usage.recorded` and `capability.invoked` are registered event types with no
production emitter yet (future wiring: `services/session`'s runner and the
capability runtime's injection paths) — same pattern as `agent.outcome_recorded`.

Retro routes (spec §7, W5.5) — a `Retro`'s DPAs live inside its own JSON blob, not a
separate SQL table, so per-DPA mutations are read-modify-write inside `store.Log.WithTx`:

- `POST /retros`, `GET /retros`, `GET /retros/{id}` — create/list/get; `Create` emits
  `retro.completed` in the same transaction as the row write (one write, one event — the
  Router's job body makes no completion emit of its own).
- `POST /retros/{id}/dpas/{dpaId}/accept` / `reject` — flips a DPA's status, emits
  `retro.dpa_accepted` / `retro.dpa_rejected`.
- `PATCH /retros/{id}/dpas/{dpaId}` — edits any non-empty field, sets status to `edited`,
  emits `retro.dpa_edited`.
- `POST /retros/{id}/follow-up` — links a retro to the session created from its accepted
  DPAs, emits `retro.follow_up_created`. Called by the Router's
  `handleRetroFollowUpSession` after it creates the session and imports the tasks.

Internally, every mutating route runs inside `store.Log.WithTx` — the transactional
outbox. State change and events commit atomically (`task.created|updated|
status_changed|confirmed|discarded`, `note.created|confirmed|discarded`);
subscribers are woken post-commit.

## Critical paths

- **Migrations are atomic** — a failing migration leaves no partial schema and records
  no version: `internal/store/migrate_test.go` (TestMigrateFailureRollsBackAtomically).
- **The production migration set always round-trips** — up → down → up must stay clean
  forever, or the update lifecycle (§14) breaks: TestProductionMigrations.
- **Refuses to start against a newer schema than it knows** (W7.5, §14.5): on boot, after
  applying every migration it does know (`store.Migrate`), `checkSchemaCompatible`
  (`compat.go`) compares the on-disk schema version against `store.MaxVersion` (the highest
  migration compiled into the binary) and `log.Fatal`s if the disk is ahead — an older
  binary pointed at a newer data directory (a rolled-back update, or a downgrade) refuses
  to run instead of silently misreading rows it doesn't understand. This is a version-gate,
  not an automatic down-migration: `Migrations()`'s down.sql files are destructive (they
  drop columns/tables), so nothing here runs one on your behalf — the fix is installing a
  binary that knows the schema, not rolling the data back. `compat_test.go` covers both the
  compatible and refuses-newer-schema paths.
- **Outbox atomicity** — a state mutation and its events commit or roll back together:
  `internal/store/log_test.go` (TestOutboxAtomicity).
- **Cursor resume is exact** — a reader resuming by cursor sees every matching event
  once, in order, no gaps, no duplicates: TestCursorResumeProperty (store) and
  TestSSEStreamAndCursorResume (api, across a real disconnect).
- **Subscribers wake only after commit** — TestWaitWakesOnCommit; the SSE loop arms its
  wait before querying so no event can land unseen between the two.
- **Capability health is transition-only** — `SetCapabilityHealth` compares against the
  capability's currently stored `Healthy` flag inside the same transaction as the update,
  emitting `capability.health_changed` only on a genuine flip. The sharpest case: the very
  first unhealthy report matches the zero-value default and emits nothing.
  `internal/store/capability_health_test.go` (TestSetCapabilityHealthTransitionOnly) and
  `internal/api/registries_test.go` (TestCapabilityHealthRouteOverHTTP) both pin this.

- **Single-connection discipline** — the pool holds one connection (`store.Open`), so
  no query may run while another cursor is still open; `List`/`Get` close before
  hydrating. Violations deadlock instantly under test, which is the guard.
- **Failed writes leave nothing** — a create/import that fails validation mid-
  transaction leaves no rows and no events: TestDependencyValidation,
  TestImportAtomicity.

- **Lease expiry blocks, loudly** — an expired lease on a mid-flight task releases,
  marks the task blocked, and emits task.status_changed with the reason; idle tasks
  keep their status: TestExpireStaleBlocksMidFlightTasks, TestReleaseAllForCrashedHolder.
- **Exactly one claimant wins** — racing claims produce one lease and clean
  LeaseHeldError losses: TestClaimContention (under -race).
- **Pipeline run reconstruction never drops an unterminated run** — a
  `pipeline.started` with no closing event yet still comes back from
  `GET /metrics/pipelines`, with `outcome: "running"` and no `ended_at`, rather than
  being silently excluded: `internal/store/metrics_test.go`
  (TestPipelineRunsUnterminatedStaysRunning). Cancelled/failed runs report
  `tasks_done/blocked/skipped = 0` — that data isn't in those terminal events'
  payloads; this is a documented limitation, not a bug to route around.
- **Never-used capabilities are zero-filled, not omitted** — `GET
  /metrics/capabilities` lists every registered capability, so ones with no
  `capability.invoked` events are visible as removal candidates instead of
  disappearing from the view: `pkg/dataclient/metrics_test.go`
  (TestMetricsCapabilityUsage).
- **DPA transitions never lose the rest of the retro.** Accept/reject/edit each
  read-modify-write the whole `Retro` row inside one `WithTx`, so a DPA mutation can never
  partially apply or clobber sibling DPAs: `internal/store/retros_test.go`
  (TestDPATransitions, TestDPANotFound).
