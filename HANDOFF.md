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
