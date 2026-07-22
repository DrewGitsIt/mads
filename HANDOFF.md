# HANDOFF: Engine survives suspend/lease loss

## What changed

- `services/pipeline/engine/clock.go` (new): `Clock` interface + `realClock`,
  which strips the monotonic reading off `time.Now()` (`Round(0)`) so `Sub`
  between two readings is always wall-clock (CLOCK_REALTIME) elapsed time —
  correct across a suspend, unlike `context.WithTimeout`/`time.Ticker`
  (CLOCK_MONOTONIC, paused for the whole suspend).
- `services/pipeline/engine/engine.go`:
  - `Config.Clock` (defaults to `realClock{}` in `Run`); `engine.clock` field.
  - `runTask`'s heartbeat goroutine rewritten around one `abort()` closure
    with three triggers, all treated identically: (1) proactive wall-clock
    gap since the last successful renewal exceeding the TTL, checked before
    every renewal attempt; (2) renewal rejected (`ErrConflict`/`ErrNotFound`);
    (3) renewal succeeded but the task's own status has moved off
    `InProgress` without the engine itself doing it (`terminal` guards the
    engine's own legitimate block/done writes). `abort()` kills the stage
    (`cancelTask`), snapshots+backs up the worktree
    (`abortLeaseLoss`, new method — reuses the existing unmodified
    `commitStray`/`backupBranch`), releases the lease best-effort, and writes
    no task status. This **replaces** the old behavior of restoring
    `Blocked → InProgress` and resuming, which was the zombie-state bug.
  - An `aborted` channel, closed at the end of `abort()`, is awaited at
    every `lost.Load()` exit point in `runTask` so the task never returns
    (and Run/Summary never observe it as done) while the snapshot/backup/
    release work is still in flight.
  - A `snapshotDir` string, guarded by a small mutex-closure pair, tracks
    "the worktree dir to snapshot" across the heartbeat/main goroutines; set
    at worktree creation, cleared once a merge stage lands the branch (since
    the existing `taskRun` struct doesn't clear `worktree`/`branch` on
    merge).
  - `runStage` gained a wall-clock watchdog: a real 200ms-poll ticker races
    alongside the existing `context.WithTimeout(ctx, StageTimeout)` and calls
    `cancel()` early once `e.clock.Now().Sub(start) >= StageTimeout` — so a
    suspended machine can't extend a stage's budget by the sleep duration.
- `services/pipeline/engine/lease_test.go`: `TestSuspendWakeRestoresTask`
  rewritten to `TestSuspendWakeAbortsTask` (asserts the task stays `Blocked`
  and is absent from `sum.Done`, not silently finished). Added
  `TestWallClockGapTriggersAbortWithoutRejectedRenewal` (fake clock jumped
  past the TTL with no lease theft at all — the gap alone must abort).
- `services/pipeline/engine/clock_test.go` (new): `fakeClock` test double.
- `services/pipeline/engine/stagetimeout_test.go` (new):
  `TestStageTimeoutUsesWallClock` — a real `sleep 5` command stage with an
  hour-long `StageTimeout`, fake clock jumped 2 hours ahead; asserts `Run`
  returns in well under 5s (must be canceled long before either the real
  sleep or the real hour elapses).
- `services/pipeline/engine/lease_isolated_test.go` (new):
  `TestLeaseLossKillsAgentBacksUpAndContinuesClaiming` — the full integration
  path with a real git worktree and a `killableProvider` that runs one task
  as a real subprocess (`Setpgid` + process-group `SIGKILL` on cancel,
  modeled on `runCommand`). Steals the lease via a thief actor (same pattern
  as `TestLostLeaseAbandonsTask`), then asserts: the subprocess pid is dead,
  `backup/<id>` matches `mads/<id>`'s new auto-commit head (the worktree had
  a dirty file written before backgrounding), the orphaned task's status is
  untouched (still `InProgress`, thief's lease undisturbed), and the claim
  loop went on to finish a second ready task.
- `services/pipeline/README.md`: rewrote the "Suspend/wake self-heals"
  critical-path bullet to describe the new abort behavior and name the new
  tests; added a note to "Every block backs up the branch" that lease loss
  now gets the same backup guarantee.

## What I did not do

- `worktree.go` is untouched, as scoped — `abortLeaseLoss` only calls the
  two pre-existing, unmodified methods (`commitStray`, `backupBranch`).
- No changes to `reap.go` (launch-time reap-on-launch path) — out of scope,
  confirmed distinct from this task's during-run lease-loss path.
- Did not add a production `pidFileProvider`; the subprocess-backed provider
  (`killableProvider`) is test-only, in the new `lease_isolated_test.go`.

## What a reviewer should scrutinize

- The three-trigger `abort()` design in `runTask`'s heartbeat goroutine
  (`engine.go`, the block starting `ctx, cancelTask := context.WithCancel(ctx)`
  through the closing `defer func() { if !lost.Load() ... }()`) is the
  densest part of this change — worth reading closely against the acceptance
  criteria's three triggers.
- The `aborted`-channel wait pattern at the three `lost.Load()` sites is
  what prevents a race between the heartbeat goroutine's in-flight
  snapshot/backup and the main goroutine returning early; there isn't a
  test that directly exercises "the race would have happened without the
  wait" (i.e. no test was written to fail against a version without the
  channel) — the integration test's assertions (backup branch exists,
  pid is dead) are what would catch a regression here, but only if run
  enough times to surface a timing-dependent bug.
- `make check`'s `lint` target fails on two pre-existing issues unrelated to
  this task (`services/router/internal/supervisor/supervisor_test.go:89`
  errcheck, `services/session/internal/api/api_test.go:431` staticcheck) —
  confirmed present on `main` with no diff from this branch. `go build ./...`
  and `go test -race ./...` are both fully green.

## Test plan run

- `go build ./...`, `go vet ./services/pipeline/...` — clean.
- `go test -race ./services/pipeline/...` — green, including the full
  existing lease/block/backup/land suite unchanged in assertions.
- New/rewritten tests re-run at `-count=5 -race` for flake-checking — stable.
- `go test -race ./...` (whole repo) — green.
