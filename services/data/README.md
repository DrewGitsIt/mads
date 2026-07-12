# Data Service

The sole owner of persistent state (spec §15): tasks, sessions, file tracking, registries
(agents, capabilities, workspaces, templates), and the **event log** — the append-only
`events` table that is the system's control plane and its metrics store (§8). Every state
mutation commits together with its event in one SQLite transaction (transactional outbox).
It does NOT talk to providers, spawn processes, or hold business logic about pipelines or
sessions — it stores, validates, and announces.

**Status: W1.1 done** — SQLite (WAL, CGO-free via modernc.org/sqlite), versioned
up/down migrations, `/health` with db ping. Next: W1.2 event log & outbox.

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

## Critical paths

- **Migrations are atomic** — a failing migration leaves no partial schema and records
  no version: `internal/store/migrate_test.go` (TestMigrateFailureRollsBackAtomically).
- **The production migration set always round-trips** — up → down → up must stay clean
  forever, or the update lifecycle (§14) breaks: TestProductionMigrations.

Coming with W1.2+: outbox atomicity, cursor-resume exactness (no gaps, no duplicates),
lease TTL expiry → blocked-with-event.
