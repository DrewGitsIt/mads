# Data Service

The sole owner of persistent state (spec §15): tasks, sessions, file tracking, registries
(agents, capabilities, workspaces, templates), and the **event log** — the append-only
`events` table that is the system's control plane and its metrics store (§8). Every state
mutation commits together with its event in one SQLite transaction (transactional outbox).
It does NOT talk to providers, spawn processes, or hold business logic about pipelines or
sessions — it stores, validates, and announces.

**Status: wave-0 skeleton.** `/health` only. Wave-1 work items W1.1–W1.7
(docs/MADS-WORK-ITEMS.md) build it out.

## Run it alone

```sh
make run        # listens on localhost:8081 (-addr to change)
curl localhost:8081/health
```

## Test it

```sh
make test       # -race always; no external dependencies
```

## API contract

Wire types live in `pkg/contracts` (Envelope, Task, ValidationError) with golden fixtures
in `pkg/contracts/testdata/`. Counterparties: the Router (reads/subscribes on behalf of
the UI) and the other services (writes via lease, event append via outbox).

## Critical paths

To be populated as W1.2+ land. The first entries will be: outbox atomicity, cursor-resume
exactness (no gaps, no duplicates), lease TTL expiry → blocked-with-event.
