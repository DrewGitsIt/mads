# Router

The always-running coordinator and the UI's single entry point (spec §15). Spawns and
supervises Session Service and Pipeline Engine child processes (supervision is the primary
liveness signal — on child exit it releases leases and emits the failure event), proxies
control-plane event subscriptions to the Data Service, and forwards ephemeral data-plane
agent-output streams to the UI. It does NOT persist anything and holds no business logic
about what sessions or pipelines do.

**Status: wave-0 skeleton.** `/health` only. Wave-4 work items W4.1–W4.2, W4.6
(docs/MADS-WORK-ITEMS.md) build it out.

## Run it alone

```sh
make run        # listens on localhost:8080 (-addr to change)
curl localhost:8080/health
```

## Test it

```sh
make test       # -race always; no external dependencies
```

## API contract

Wire types live in `pkg/contracts`. Counterparties: the UI (HTTP + SSE) and the Data
Service (event subscriptions, lease release); children (Session/Pipeline) are spawned,
not called peer-to-peer.

## Critical paths

To be populated as W4.1+ land. The first entries will be: child crash → immediate lease
release, subscription reconnect resumes from cursor without gaps.
