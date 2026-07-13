# Router

The always-running coordinator and the UI's single entry point (spec §15). Spawns and
supervises Session Service and Pipeline Engine child processes (supervision is the primary
liveness signal — on child exit it releases leases and emits the failure event), proxies
control-plane event subscriptions to the Data Service, and forwards ephemeral data-plane
agent-output streams to the UI. It does NOT persist anything and holds no business logic
about what sessions or pipelines do — all state access goes through `pkg/dataclient`
against the Data Service; it never calls a sibling service peer-to-peer.

**Status: W4.1 and W4.2 complete.** Child process supervision (`internal/child`,
`internal/supervisor`), the lifecycle HTTP API, and stream fan-out (`internal/api`) are
built and tested against real spawned processes, real SSE connections, and a real Data
Service. Not yet built: whatever W4.6 turns out to be.

## Run it alone

```sh
make run        # listens on localhost:8080 (-addr to change)
curl localhost:8080/health
```

Flags: `-data-url` (default `http://localhost:8081`) points at the Data Service;
`-session-cmd`/`-pipeline-cmd` (default `go run ./services/session` / `go run
./services/pipeline`) are the commands used to spawn children — override to point at
compiled binaries once spec §14 packaging ships.

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

## API contract

Wire types live in `pkg/contracts`. Counterparties: the UI (HTTP, and eventually SSE) and
the Data Service (via `pkg/dataclient`); children (Session/Pipeline) are spawned processes,
never called peer-to-peer.

- `GET /health` — `{"service":"router","status":"ok"}`
- `POST /sessions` — create a session (proxies `dataclient.CreateSession`) and spawn its
  Session Service child; 201 with `{"session": ..., "addr": ...}`. On spawn failure, the
  session record is rolled back (best-effort delete) so a failed create doesn't leave an
  orphaned record with nothing backing it.
- `POST /sessions/{id}/terminate` — stop the child (SIGTERM, grace, SIGKILL) and delete the
  session record; 204. The Data Service's own `session.deleted` event covers the "why";
  the Router doesn't emit anything itself here.
- `POST /sessions/{id}/bounce` — terminate and respawn under the same session ID/lease
  holder; 200 with the new `{"addr": ...}`.
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

Errors: `dataclient.ErrNotFound` → 404, `dataclient.ErrConflict` → 409,
`*contracts.ValidationError` → 400 (this is a deliberate divergence from the Data
Service's own 422-for-validation convention — the Router treats a rejected request the
same whether the rejection came from its own decoding or a proxied dataclient call).

## Critical paths

- **Supervision beats the lease TTL backstop, not just a graceful path** —
  `TestExternalKillReleasesLeasesAndEmitsCrashEvent` (`internal/supervisor`) spawns a real
  child, claims a task's lease with a 30s TTL, kills the child's OS process directly (not
  via `Terminate`), and asserts the task is blocked and a `session.crashed` event lands —
  in well under a second, proving it's supervision catching the exit and not the TTL
  expiring underneath it. `TestPipelineExternalKillReleasesLeasesAndEmitsFailedEvent`
  covers the same for Pipeline Engine children and `pipeline.failed`.
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
