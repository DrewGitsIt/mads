# Data Service

The sole owner of persistent state (spec §15): tasks, sessions, file tracking, registries
(agents, capabilities, workspaces, templates), and the **event log** — the append-only
`events` table that is the system's control plane and its metrics store (§8). Every state
mutation commits together with its event in one SQLite transaction (transactional outbox).
It does NOT talk to providers, spawn processes, or hold business logic about pipelines or
sessions — it stores, validates, and announces.

**Status: Wave 1 COMPLETE (W1.1–W1.7)** — SQLite store + migrations, event log +
transactional outbox + SSE subscriptions, task store, write leases, registries
(six system agents seeded), pipeline templates (launch-equivalent validation via
`pkg/template`, system default seeded), and session records with §5 file-change
attribution and the commit-linkage chain. Next: Wave 2, the provider layer.

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

Internally, every mutating route runs inside `store.Log.WithTx` — the transactional
outbox. State change and events commit atomically (`task.created|updated|
status_changed|confirmed|discarded`, `note.created`); subscribers are woken
post-commit.

## Critical paths

- **Migrations are atomic** — a failing migration leaves no partial schema and records
  no version: `internal/store/migrate_test.go` (TestMigrateFailureRollsBackAtomically).
- **The production migration set always round-trips** — up → down → up must stay clean
  forever, or the update lifecycle (§14) breaks: TestProductionMigrations.
- **Outbox atomicity** — a state mutation and its events commit or roll back together:
  `internal/store/log_test.go` (TestOutboxAtomicity).
- **Cursor resume is exact** — a reader resuming by cursor sees every matching event
  once, in order, no gaps, no duplicates: TestCursorResumeProperty (store) and
  TestSSEStreamAndCursorResume (api, across a real disconnect).
- **Subscribers wake only after commit** — TestWaitWakesOnCommit; the SSE loop arms its
  wait before querying so no event can land unseen between the two.

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
