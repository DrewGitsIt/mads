# Data Service

The sole owner of persistent state (spec §15): tasks, sessions, file tracking, registries
(agents, capabilities, workspaces, templates), and the **event log** — the append-only
`events` table that is the system's control plane and its metrics store (§8). Every state
mutation commits together with its event in one SQLite transaction (transactional outbox).
It does NOT talk to providers, spawn processes, or hold business logic about pipelines or
sessions — it stores, validates, and announces.

**Status: W1.3 done** — store + migrations (W1.1), event log + outbox + SSE
subscriptions (W1.2), and the task store: CRUD, draft confirm/discard, dependency
edges with launch-time cycle detection, handoffs, typed notes, atomic bulk import.
Next: W1.4 write leases.

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

Coming with W1.4+: lease TTL expiry → blocked-with-event.
