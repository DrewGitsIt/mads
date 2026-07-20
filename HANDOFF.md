# HANDOFF: Pipeline Start button drives the real engine

## What was done

**services/pipeline is now a real engine child, not a Wave-0 stub.**
- `pkg/pipelinerun` (new) extracts the config-bootstrap + `engine.Run` call
  that `cmd/mads run` used inline, as `LoadTemplate` + `Run(ctx, Options)`.
  `cmd/mads/run.go` now calls this package instead of duplicating the logic.
- `services/pipeline/main.go` no longer just opens a listener and blocks: it
  requires `-run-id -session-id -template` (passed by `SpawnPipeline`),
  loads the session, resolves workspace (`Workspace.PrimaryRepo()`, new
  helper on `pkg/contracts.Workspace`), template, provider, and concurrency,
  then calls `pipelinerun.Run` — the exact engine `mads run` drives. SIGTERM/
  SIGINT cancel the run's context; the run's own completion or a context
  cancellation both trigger a graceful HTTP shutdown before the process
  exits, so exit code reflects success/failure through the existing
  `onPipelineExit` path.
- `SpawnPipeline` (`supervisor.go`) now passes `-run-id -session-id
  -template` as argv (previously argv/env carried none of this — the root
  cause of "opens a listener and does nothing"). `onPipelineExit` also
  stopped treating a clean exit (nil error, e.g. the run completed) as an
  unconditional non-failure-report case that happened to already be silently
  correct but for the wrong reason — now explicit.

**POST /pipelines validates before spawn** (`pipelines.go`): no workspace →
400, workspace has no primary repo → 400, invalid template
(`pipelinerun.LoadTemplate`) → 400, no ready (non-draft) tasks → 400. A
request that would have left an orphan child listening for nothing now
never spawns one. The UI already subscribes to `pipeline.*` events emitted
by the engine itself (`pipeline.started`/`pipeline.completed`/etc, verified
in `TestRunDrainsSeededBatchLikeCLI`), so "201 + silence" is no longer
possible on the success path either — I did not touch the UI, since no
existing surfacing gap was found on that side (see "Not done" below).

**Cross-engine workspace lock** (new `workspacelock*.go` in
`services/pipeline/engine`): `acquireWorkspaceLock` takes a blocking,
OS-level `flock` on `<git-common-dir>/mads-land.lock`, resolved the same way
across every worktree of a workspace. It's taken around every land attempt
in `engine.go`'s `runTask` (alongside the pre-existing in-process
`landQueue`, which only serializes within one engine), so a CLI `mads run`
and a Router-spawned pipeline child on the same workspace never interleave a
land. It's also taken around `worktree.go`'s `ensureSetup` (git-config
writes to the shared repo, e.g. registering the mergiraf driver) — this
one-time setup was racing across engine processes without it, and was the
root cause of one real flake in `TestCrossEngineWorkspaceLockSerializesLandAttempts`
(see below). Linux gets a real `syscall.Flock`; other platforms get a no-op
stub (`workspacelock_other.go`) — cross-platform correctness for non-Linux
was out of scope for this task and is called out as a gap, not silently
assumed away.

**Launchpad workspace=null root cause found and fixed**
(`sessiondefaults.go`): `resolveSessionDefaultsSourced` resolves a workspace
from either `sess.Workspace` (explicit) or `Config.DefaultWorkspace`
(fallback), and uses it to fill in model/isolation/agent defaults — but
never wrote the *resolved* workspace back onto `sess.Workspace` itself. A
session created via the Launchpad with no explicit pill click, on a
Config with a default workspace configured, would inherit all the
workspace's *other* defaults but persist `workspace: null` on the session
record itself — exactly the reported symptom. Fixed by setting
`sess.Workspace = ws.ID` on successful resolution. The Launchpad frontend
itself (`Launchpad.tsx`) was checked and is not at fault: it does correctly
send `workspace: staged.workspace` (the name) on explicit pill selection,
and that value round-trips correctly today (the data service's generic
registry `Get` falls back to `GetByName`, so a name-valued `Workspace`
field resolves fine everywhere it's read). No frontend changes were made.

## Tests added/verified

- `services/pipeline/main_test.go`: `TestRunDrainsSeededBatchLikeCLI` — child
  mode drains a seeded task to done and emits `pipeline.started`/
  `pipeline.completed`, same as `mads run`.
- `services/router/internal/api/api_test.go`: no-workspace / no-ready-tasks /
  invalid-template 400s for `POST /pipelines`, no orphan child left behind.
- `services/pipeline/engine/workspacelock_test.go` (new):
  `TestAcquireWorkspaceFlockBlocksUntilReleased` (OS-level semantics) and
  `TestCrossEngineWorkspaceLockSerializesLandAttempts` (two real engine
  processes, overlapping tasks, one shared workspace — asserts no
  interleaved land). This test was flaky (1/10 failures) before the
  `ensureSetup` fix; 20/20 passing after.
- `services/pipeline/engine/lease_test.go`:
  `TestCancelDuringRateLimitPauseReleasesLease` — a cancelled `Run` releases
  its lease promptly (not just abandons it to TTL expiry), tested via the
  engine's own ctx-aware rate-limit backoff pause rather than the fake
  provider's non-ctx-aware `OneShot` sleep (which would not have proven
  cancellation propagation).
- `services/router/internal/api/sessions_test.go`:
  `TestCreateSessionWithNoExplicitWorkspacePersistsConfigDefault` — verified
  it fails without the `sessiondefaults.go` fix and passes with it.

Full suite (`go test ./...`) passes; `go build ./...` and `go vet ./...`
clean.

## Not done / reviewer should scrutinize

- **Lock-release-on-cancel has no dedicated test.** I reasoned that
  `landAttempt`'s git operations all run via `exec.CommandContext(ctx, ...)`,
  so a cancelled context kills any in-flight subprocess promptly and the
  lock-release always runs synchronously right after `landAttempt` returns
  — and judged the existing `TestAcquireWorkspaceFlockBlocksUntilReleased` +
  `TestCrossEngineWorkspaceLockSerializesLandAttempts` sufficient coverage
  without a dedicated "cancel mid-land releases the flock" test. This is a
  judgment call, not a verified fact — worth a second look, and an easy gap
  to close with a follow-up test if the reviewer disagrees.
- **Non-Linux workspace lock is a no-op** (`workspacelock_other.go`). Cross-
  engine land safety is real only on Linux today; this matches the existing
  platform scope of this codebase's other OS-specific pieces but is worth
  flagging explicitly since the acceptance criteria don't mention a platform
  restriction.
- **Provider default in `services/pipeline/main.go`'s `run()`** falls back to
  `"claude-code"` if neither the session nor Config specify one. This mirrors
  a reasonable default but wasn't in the task brief; flagging in case a
  different fallback (or a hard error) is preferred.
- No UI/frontend changes were made. I verified the pipeline tab's existing
  event subscription and the Launchpad's create-session payload both already
  behave correctly once the two backend fixes above land; I did not run the
  dev server / exercise this in a browser (backend-only task, no UI code
  touched).

## Review-fix pass: cancel didn't actually release the lease

Code review correctly flagged that `services/pipeline/main.go`'s
`ctx.Done()` branch didn't wait for the `run()` goroutine before returning,
so the process could exit mid-cleanup without releasing the task lease.
Fixed by adding an inner `select` that waits on `run()`'s result channel
(bounded by the existing `shutdownGrace`, with a log line if that bound is
hit) before shutting down the HTTP server.

**That fix alone was not sufficient** — while writing the regression test
(`TestChildReleasesLeaseOnCancel`, spawns the real compiled binary and sends
a real SIGTERM, then checks the lease is gone via the `"pipeline"` actor's
own `Leases()` view), I found `run()` itself didn't return promptly on
cancellation either, so the wait above was just timing out every time
instead of catching anything. Root cause was one level deeper, in
`engine.runCommand` (`services/pipeline/engine/engine.go`): `sh -c "<cmd>"`
doesn't always exec-optimize into a single process — under this
environment it forked a real child for a bare `sleep 30`. `exec.CommandContext`
only kills the direct `sh` process on cancel, not that grandchild; since the
grandchild inherited `CombinedOutput`'s stdout/stderr pipe, the pipe never
saw EOF and `CombinedOutput()` blocked until the orphaned process finished
on its own (up to the full command duration) — well past the point where
`runTask`'s deferred lease-release could ever run. Fixed by running `sh` as
its own process group leader (`Setpgid: true`) and overriding `cmd.Cancel`
to `SIGKILL` the whole group (`-pid`) instead of just the one process, with
`cmd.WaitDelay` as a backstop that force-closes the pipes if some process
still won't die. `TestChildReleasesLeaseOnCancel` now passes in ~0.08s
(previously hung for the full 10s `shutdownGrace` and then some).

**Scrutinize:**
- The same "grandchild survives, pipe never closes" failure mode is
  possible in `engine.runAgent` → `provider.OneShot` (agent stages, not
  command stages) if the underlying provider CLI (claude-code, kiro-cli)
  ever forks and outlives its own direct child on cancel. I did not audit
  or change the provider implementations — out of scope for the specific
  reviewer-flagged bug, which was reproduced and fixed via a `command:`
  stage. Worth a follow-up check if cancel-during-agent-stage is a
  path this task's acceptance criteria are also meant to cover.
- `runCommand`'s process-group kill (`syscall.Setpgid`/`syscall.Kill`) is
  Linux/POSIX-only, same platform scope as the rest of this task's
  Linux-only `workspacelock.go` — not a new restriction, but compounds the
  existing non-Linux gap already flagged above.
- The pre-existing `"pipeline"`-vs-per-run lease-holder-identity question
  (both `cmd/mads/run.go` and `services/pipeline/main.go`'s `run()`
  authenticate every dataclient call as the literal actor string
  `"pipeline"`, not a per-run ID, in seeming tension with
  `services/session/main.go`'s per-session actor pattern and the
  Supervisor's `ReleaseAll(runID, ...)` backstop) came up again while
  tracing this bug but is unrelated to it and was not touched.
