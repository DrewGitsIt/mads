# Engine: deflake TestMidRunCycleRejected — executor notes

Implemented exactly as designed by the architect handoff (see git history
for the full root-cause writeup); no deviations.

## What was done

- Added `(h *harness) waitStatus(t, id, status, timeout)` to
  `engine_test.go`: polls `Data.Task` every 10ms until the target status is
  reached or the deadline expires, replacing fixed sleeps used to
  synchronize with the engine's real scheduling latency.
- `TestMidRunTaskPickup` (`queue_test.go`): capture the seeded task's ID and
  replace `time.Sleep(200 * time.Millisecond)` with
  `h.waitStatus(t, seeded[0].ID, contracts.StatusInProgress, 5*time.Second)`.
- `TestMidRunCycleRejected` (`queue_test.go`): same sleep→waitStatus swap,
  plus the more serious fix — import the mutually-dependent "chicken"/"egg"
  pair as `Draft: true`, wire the two dependency edges via `UpdateTask` (as
  before), then call `ConfirmTask` on both only after both edges are
  committed. Drafts are excluded from `engine.go`'s `runnable()` regardless
  of dependency state, so the scheduler can never observe (and wrongly
  execute) either task mid-construction, before both `Dependencies` writes
  land.

## What was not done

- `engine.go` / `Cycles()` / cycle-rejection logic: untouched, per the
  architect's "why not fix engine.go instead" — the half-committed-cycle
  window is inherent to per-field-mutable dependencies (spec §3), and
  fixing the test via draft/confirm works entirely within existing,
  supported mechanisms (spec §4).

## Verification performed

1. `go test ./services/pipeline/engine/... -run TestMidRunCycleRejected -race -count=20 -v` — 20/20 PASS.
2. Artificial load: 4 concurrent `go test ./services/pipeline/engine/... -race -count=20` processes on the same machine — all 4 PASS (~295-297s each).
3. Assertion-still-bites check (temporary, reverted, not committed): changed
   `engine.go`'s cycle-rejection condition from
   `if cycles, err := e.cfg.Data.Cycles(ctx, e.cfg.SessionID); err == nil {`
   to `... ; false && err == nil {`, reran `TestMidRunCycleRejected`
   standalone — it failed as expected
   (`summary: &{Done:[...] Blocked:[] Skipped:[...]}`), confirming the test
   still guards real cycle-rejection behavior. Reverted immediately;
   `git diff` on `engine.go` is clean.
4. `make check` — green (Go lint+test, UI test suite all pass; the
   ECONNREFUSED lines in output are expected stderr from tests exercising
   connection-failure paths, not real failures — full run exits 0).

## Reviewer scrutiny

- The `waitStatus` timeout (5s) and poll interval (10ms) are unchanged from
  the architect's spec — reasonable given `DelayMs: 300` per chunk on the
  long-runner fixture, but worth a glance if this flakes again under much
  heavier load.
- No changes to `engine.go`, `Cycles()`, or any non-test file.
