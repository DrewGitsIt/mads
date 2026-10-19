# Pipeline leases and resumable runs

Status: **agreed** (2026-10-19). Part A replaces the "every engine is
`pipeline`" lease shortcut (spec §15, reap-on-launch). Part B lets any run
pick an interrupted task up where it stopped.

## Problem

**Leases.** A task lease is held by an *actor string*, and every pipeline
engine uses the same one: `"pipeline"` (`services/pipeline/main.go:271`,
`cmd/mads/run.go:73`). The actor is a self-declared `X-Mads-Actor` header
(`services/data/internal/api/tasks.go:15`). Consequences:

1. Leases can't tell engines apart: anything that says "pipeline" can renew
   or release any engine's lease.
2. The Router's exit cleanup releases nothing. It calls `ReleaseAll(runID)`
   (`supervisor.go:499`), but no lease is held by a run id.
3. A heartbeat renews by calling Claim (`engine.go:645`), which succeeds on
   a *freed* lease, so an engine can silently re-take a lease it lost.
4. `TestLeaseLossKillsAgentBacksUpAndContinuesClaiming` flakes (about 1 in 3
   runs, on main too). The test releases as "pipeline", and the real
   engine's heartbeat re-takes the lease before the test's thief can claim
   it.

**Interruption loses work.** Losing a lease changes the task's status: the
TTL reaper and the Router's exit cleanup mark it **blocked**
(`leases.go:214`), and reclaim-on-launch requeues it to **ready**
(`leases.go:350`). When any later run picks the task up,
`worktrees.create` wipes its worktree and deletes its branch
(`worktree.go:174`), and the task starts again at the first stage. A crash
or reboot mid-run therefore throws away every in-flight task's progress.

## Model

- A **lease** means "this process is working on this task right now". It
  is not the task's progress, and losing one never changes task status.
- A task's **progress** is durable: its status, its worktree and branch
  (`mads/<taskID>`), and its run's stage events, attempt logs and
  `stage.attempt_ref` (the agent's native session id, recorded as soon as
  the provider announces it, `engine.go:1784`).
- Task state is not tied to a run. A task is **Interrupted** when it is
  `in_progress` with no live lease: work started and nobody is doing it
  now. Interrupted is a *pipeline* concept, derived and never stored. It
  shows in the Tasks tab's run views and never in a task's own status.
  Blocked (needs a person) and Needs review are not Interrupted.
- The user decides when work continues by starting a run. A run picks up
  Ready tasks **and** Interrupted ones, and continues each Interrupted task
  where it stopped. There is no run-level "resume".
- A run whose engine is gone without finishing (Stop, crash, reboot) ends
  with outcome **interrupted** (stopped by the user, or not). It stays in
  the history as a record.

## Part A: leases

### A1. Holder = `pipeline:<runID>`

The engine already has a run id on both launch paths (`Config.RunID`; the
Router mints it in `supervisor.NewRunID`, and `mads run` mints one in the
same format). The holder becomes `pipeline:run_3f9a…`.

- The `pipeline:` prefix is the kind: UI and messages can say "pipeline run …".
- The holder is sent in the claim body. The store accepts it only if it
  equals the caller's actor or begins with `<actor>:`. This stops
  accidental cross-kind claims; it is not a security boundary.
- **Attribution is unchanged.** `X-Mads-Actor` stays `"pipeline"`, so
  events, notes and task `source` still read "pipeline". The holder is a
  lease concept only.

### A2. Lease tokens (proof of possession)

- **Claim** (`POST /tasks/{id}/claim`, body `{holder, ttl_seconds,
  identity}`) succeeds only if the task has no live lease. It returns a
  fresh random 128-bit `token`. The store keeps only its SHA-256, so
  reading the SQLite file (agents can) yields nothing usable.
- **Renew** (new: `POST /tasks/{id}/lease/renew`, header `X-Mads-Lease`)
  extends the lease only if it exists, is unexpired and the token matches.
  Otherwise the response is 409 `lease_lost`. **It never creates a lease.**
- **Release** requires the token (a free lease is a no-op).
- **Writes** to a leased task are authorised by token, not actor
  (`checkAndRenew`). A write without the token is rejected with today's
  `LeaseHeldError`, which keeps the read-only detail sheet while a task
  runs. A write with the token renews the lease, as today.
- **ReleaseAll** (supervision) stays holder-matched and tokenless.
  **Reclaim** (reap-on-launch) stays proof-matched and tokenless: the dead
  process's token died with it.
- The token lives only in the claiming process's memory. `dataclient`
  keeps a per-task token map and attaches it to that task's requests, so
  engine call sites don't change. The token is never written to disk, an
  env var, a worktree's git config or a log.

**After a restart** the old tokens are gone, and that's intended: the
process that owned them is gone. The next run's engine proves the old
holder dead (A4), or waits out the TTL, then claims a fresh lease and
token. Continuity comes from durable progress, never from the lease.

### A3. Lease loss changes no status

The TTL reaper, `ReleaseAll` and `Reclaim` only delete leases. A task that
was `in_progress` stays `in_progress`. An in-progress task with no live
lease is Interrupted, and Part B is how it continues.

- The Router's exit cleanup calls `ReleaseAll("pipeline:" + runID)`, which
  now matches, and ends the run as interrupted (B1).
- A live engine whose heartbeat gets `lease_lost` aborts the task as
  today: it kills the agent and stops writing. It snapshots the worktree
  (commits stray work, backs up the branch) **only if the task's lease is
  still free**. If another run already holds it, that run owns the
  worktree now and commits stray work itself on pickup (B2), so two
  processes never write one worktree. (This is the woken-laptop case: the
  lease expired during sleep and a new run took the task.)
- The post-renewal "did my task's status move?" check (`engine.go:654`)
  goes away, because a re-take can no longer happen. The wall-clock gap
  check stays.

### A4. Reclaim-on-launch

A starting engine lists leases by kind (`GET /leases?kind=pipeline`)
instead of by the shared name (`reap.go:27`). If it can prove the holder's
process is dead (`procid.Evaluate`: machine, boot, pid, start ticks; a
reboot changes the boot id), it deletes the lease. The task becomes
Interrupted rather than going back to `ready`.

### A5. No legacy path

There has been no release, so the migration (`ADD COLUMN token_hash`) also
deletes existing lease rows. Their holders are by definition pre-token
engines.

## Part B: picking up interrupted tasks

### B1. Stop, and interrupted runs

- The run bar's **Cancel** becomes **Stop**. It terminates the engine as
  today. The engine's cancellation path releases its leases, and the
  Router's `ReleaseAll` catches any stragglers. Nothing about a task
  changes: in-flight tasks become Interrupted, and their worktrees, logs
  and agent sessions stay.
- The end events already exist; nothing new is needed for Stop or a crash.
  Stop (and Ctrl-C on `mads run`) appends `pipeline.cancelled`
  (`pipelines.go:142`, `run.go:272`). A crash appends `pipeline.failed`
  (`supervisor.go:539`). The one gap is a run lost to a reboot: no process
  was left to say anything. At startup the Router scans for runs with
  `pipeline.started`, no end event and no live child, and appends
  `pipeline.failed {reason: "lost"}`.
- The Tasks tab reads all three (cancelled, failed, lost) as an
  **interrupted** run: `Run N · stopped` for cancelled and
  `Run N · interrupted` otherwise, with **New run** as for any finished
  run. (The unused `pipeline.paused`/`pipeline.resumed` event types stay
  unused.)

### B2. Picking up an Interrupted task

Every run's scheduler treats an Interrupted task like a Ready one, with the
same dependency rules and the same exclude box. When it claims one, it
continues the task's **progress record**: the task's last run, that run's
template version, its completed stages, and the interrupted stage's last
attempt and `stage.attempt_ref`, all from existing events.

1. **Keep the worktree.** `worktrees.create` no longer wipes an existing
   `mads/<taskID>` worktree or branch for an Interrupted task. It reuses
   them and commits stray dirty work first ("auto-commit: interrupted
   work"). Wiping stays only for a task with no progress (Ready), where any
   leftover worktree is stale.
2. **Same template and version as the last run** → resume exactly:
   - Skip stages already completed. Loop-back state (loop counts) is
     restored from events, too.
   - **Agent stage:** if the provider declares `resume` and the attempt
     has a ref, resume that agent session in the same worktree (new
     `OneShotRequest.Resume`, e.g. `claude -p --resume <ref>`) with
     "You were interrupted; continue where you left off". It is the same
     attempt, so its log and turns continue. Otherwise it falls back to a
     new attempt in the same worktree, told it is resuming and given the
     previous attempt's output.
   - **Command stage:** rerun as a new attempt, with the log noting "rerun
     after interruption". It may not be idempotent; templates are the
     user's.
   - **Merge stage:** check before rerunning. If the primary repo has a
     merge in progress, abort it. If the task branch is already in trunk,
     mark the stage complete and move on. Otherwise merge as normal.
3. **Different template, or the same template since edited** → restart
   the new template from its first stage, keeping the worktree and branch
   (the code work survives). No agent-session resume (a different
   stage/agent/prompt). The first agent stage is told there is prior work
   in the tree. Stage names aren't matched across templates: a name can
   mean different work, the order can differ, and a stage can be missing.

Not sandboxed, so worth saying plainly: agents run with
`--dangerously-skip-permissions`, and command stages are user-authored.
Either can touch things outside the worktree. Picking up again is safe for
the worktree's contents; anything outside it is the template's
responsibility. A session in current-directory mode (no per-task
worktrees) shares one directory, so its pickup is less isolated.

### B3. UI

- **Run bar:** Cancel → Stop. Cancelled runs show `· stopped`, and
  failed or lost runs show `· interrupted`.
- **Plan** (the pre-run view): Interrupted tasks are in "Will run",
  included by default and excludable. Each shows where it picks up:
  "Interrupted · resumes at code_review", or "Interrupted · restarts
  under <template>, keeps its work". In current-directory mode the
  line adds "shared directory".
- **Live/past run views:** a task that was in flight when the run ended
  reads **Interrupted** (in `RunStatus`, `RUN_GROUPS`), not Running or
  Blocked. When a later run picks it up, the lens timeline marks the
  pickup and shows the earlier run's stages as done.
- `runStatusOf`'s Plan rule "in_progress = leftover, won't be picked up"
  (`runStatus.ts:38`) becomes "interrupted, will be picked up".

## Test plan

- **Store (A):**
  - Claim on a free task returns a token; Claim on a leased task (any
    holder) is 409.
  - Renew: right token extends; a wrong, missing or expired token, or a
    deleted lease, gives `lease_lost`; Renew never creates a lease.
  - Writes: no token on a leased task gives LeaseHeldError; the right
    token passes and renews.
  - Reaper, ReleaseAll and Reclaim delete the lease and leave status alone.
  - The holder/actor prefix rule.
- **Engine (A):**
  - A heartbeat after a release aborts and never re-takes.
  - Abort skips the worktree snapshot when another run holds the lease.
  - **The flaky test is rewritten** to lose the lease the real way
    (`ReleaseAll("pipeline:"+runID)`, or a short TTL plus the reaper)
    before the thief claims. This is deterministic once renewals can't
    re-take. Verify with `-count=50`, with and without `-race`.
- **Pickup (B):**
  - SIGKILL the engine mid agent stage, then start a new run with the same
    template: same worktree, same agent session resumed (the fake provider
    records resume refs), completed stages not rerun, same attempt.
  - The same mid command stage: rerun as a new attempt.
  - Mid merge stage: no double merge; a half merge is aborted, then redone.
  - A provider without resume: a new attempt with context.
  - Different template: starts at stage 1 with the worktree kept.
  - Ready tasks still get a fresh worktree.
  - Blocked tasks are never picked up.
  - Router: Stop → cancelled, crash → failed, and a Router restart finds
    a lost run and marks it failed/"lost", once.
- **e2e:** start, Stop mid-run, check the run bar shows interrupted and
  Plan shows "resumes at …", then start a new run: the task finishes, and
  the lens shows the earlier stages done plus the pickup marker.

## Work breakdown

| # | Part | Depends on |
|---|------|-----------|
| A-L1 | Data: migration (token_hash, drop old leases); holder in claim body plus prefix rule; tokens; Renew; token-checked writes and release; `?kind=`; reaper/ReleaseAll/Reclaim stop changing status | — |
| A-L2 | `dataclient`: per-task token map, `X-Mads-Lease`, `Renew` | A-L1 |
| A-L3 | Engine: holder `pipeline:<runID>`; heartbeat → Renew; drop the re-take check; snapshot only if the lease is free; reap by kind; rewrite the flaky test | A-L2 |
| A-L4 | Router: `ReleaseAll("pipeline:"+runID)` on exit; `mads run` holder | A-L3 |
| B-R1 | Router: at startup, `pipeline.failed {reason: "lost"}` for started runs with no end event and no live child | A-L4 |
| B-R2 | Provider: `OneShotRequest.Resume`; claude-code and fake implement it; matrix conformance | — |
| B-R3 | Engine: Interrupted tasks are runnable; progress record from events; keep worktree; same-template resume (skip stages, loop state, agent resume / command rerun / merge check); different-template restart | A-L3, B-R2 |
| B-R4 | UI: Stop; interrupted outcome; Interrupted status and Plan lines; lens pickup marker | B-R1 |
| B-R5 | e2e Stop-and-pick-up journey; spec §15, reap-on-launch, TASKS-PIPELINE-DESIGN updated | all |

Part A ships first and stands alone: it fixes the flaky test, the Router's
cleanup and the status churn.

## Decisions (2026-10-19)

- Attribution stays `"pipeline"`.
- Losing a lease never changes task status.
- No legacy tokenless path (no release yet).
- Cancel becomes **Stop**: it keeps all state and returns the leases.
- Task state isn't tied to a run: any new run picks up Interrupted tasks.
  Interrupted = `in_progress` with no live lease, a pipeline-only concept.
- Agent stages resume the same agent session where possible; command
  stages rerun.
- A different template restarts from its first stage and keeps the work.
