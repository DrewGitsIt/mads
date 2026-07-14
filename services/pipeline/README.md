# Pipeline Engine

Executes pipeline templates over task graphs (spec §6): stage progression for agent and
command stages, `when:` conditions, `on_fail` loop-backs with max-loop caps, worktree-per-
task isolation, dependency-ordered merges, and the live work queue that picks up
same-session tasks created mid-run. Spawned per run by the Router. It does NOT define
templates (Data Service stores them; validation logic is shared) and does NOT talk to the
UI directly — progress is announced as `pipeline.*`/`stage.*` events.

**Status: Wave 3 complete (W3.1–W3.4).** The engine is exported at
`services/pipeline/engine` — the bootstrap CLI (`cmd/mads`) drives it
directly, and the queue is live: the scheduler wakes on the session's
`task.*` events, mid-run inserts join behind their dependencies, and
cycle-introducing inserts are rejected loudly (blocked + note + event). `internal/engine` executes a
frozen template over a session's task graph: launch checks (shared
`pkg/template` validation, agent resolution frozen at start, cycle check per
§3.6), a continuous dependency-ordered scheduler under a concurrency cap,
agent stages as provider one-shots, command stages via `sh -c`, `when:`
guards (grammar in `pkg/template` so the editor rejects exactly what launch
rejects), and `on_fail` loop-backs that feed failure output back into the
next agent attempt before blocking at max_loops. A provider rate limit is
not a stage failure: the engine pauses and retries the same attempt (no
loop_back consumed, no rate-limit text reaching the prompt) until the
provider recovers or a retry cap is hit, per spec §11.9. Every running task
is leased; foreign-leased tasks are left untouched.

With `Isolated` on (W3.2), each task gets its own worktree and `mads/<task>`
branch: pre-merge stages run there, a stage whose command starts `git merge`
is the merge-back — serialized (one at a time), branch and trailer-carrying
message supplied by the engine, conflicts aborted loudly into a blocker note
naming the files — and post-merge stages (verify) run on the merged tree
under the same lock. Uncommitted agent work is auto-committed with the task
trailer before merging (never lost, always linked); merged worktrees are
cleaned up, blocked ones kept for inspection with their path in the note. A
post-commit hook installed in the workspace repo reports every commit —
task context read from per-worktree git config — to the Data Service (§5).

Conventions the engine defines (until the spec §6 grammar grows richer):
- An **agent stage fails** when the provider errors or the reply's last line
  starts with `STAGE: fail` (gate agents are prompted to end with
  `STAGE: pass` / `STAGE: fail — reason`).
- `agent: auto` resolves to the `executor` system agent (quadrant routing is
  a later work item, §9).

## Run it alone

```sh
make run        # binds an ephemeral localhost port and logs it (-addr to pin)
curl localhost:<logged port>/health
```

## Test it

```sh
make test       # -race always; stage machines run against provider-fake, never a live LLM
```

## API contract

Wire types live in `pkg/contracts`; all state access goes through
`pkg/dataclient` (the engine holds no state of its own). Counterparties: the
Router (run lifecycle) and the Data Service (task claims via lease, template
reads, `pipeline.*`/`stage.*`/`task.*` events). Engine tests run against a
REAL Data Service via `services/data/datatest` and against provider-fake —
never a mock of either contract.

## Critical paths

- **Loop-backs terminate** — a persistently failing gate loops back exactly
  `max_loops` times, then blocks with a blocker note naming the stage:
  TestDefaultTemplateReviewLoopBack (the W3.1 done criterion, run against the
  seeded default-implementation template).
- **Feedback reaches the retry** — the loop-back's failure output lands in
  the next agent attempt's prompt: TestCommandFailureFeedbackLoop (the fake
  executor only fixes the build when its prompt carries the feedback).
- **The concurrency cap holds and is actually used** — TestConcurrencyLimit
  (≤ limit simultaneous agent stages, and ≥ 2 with independent tasks).
- **Cycles block launch, before any event** — TestCyclesBlockLaunch.
- **Foreign leases are respected** — a task another actor holds is skipped
  untouched: TestForeignLeaseSkipsTask.
- **Leases survive silent stages** — writes renew the lease meter, but an
  agent one-shot can be silent for longer than any TTL (the first Wave-4
  dogfood run lost a task to the reaper this way); a heartbeat renews at a
  third of the TTL for the life of the task, and a renewal refused as a
  conflict abandons the task without another write:
  TestLeaseHeartbeatOutlivesSilentStage, TestLostLeaseAbandonsTask.
- **Suspend/wake self-heals** — a machine suspend stalls every heartbeat past
  the TTL at once; on wake the reaper has blocked the task but nobody else
  claimed it, so the engine re-takes the lease and puts the status back to
  in_progress (the second Wave-4 run surfaced this): TestSuspendWakeRestoresTask.
- **Sleep is inhibited while a run is active** — the engine holds a
  logind `sleep:idle` block inhibitor for the run's duration, plus an
  `org.freedesktop.ScreenSaver` inhibit on the session bus where one
  exists (COSMIC's automatic idle-suspend only honors the latter — a
  logind-only inhibitor was held throughout the Wave-5 run and the
  machine still suspended at 01:14 that day); both release on every exit
  path — completion, cancellation, and launch failure. A missing/refused
  D-Bus service logs a warning and the run proceeds unaffected; this is
  a polite block, so critical-battery and forced lid-close policies still
  override it, and the suspend/wake self-heal above stays as the
  backstop: TestInhibitorHeldForRun, TestInhibitorReleasedOnLaunchFailure,
  TestInhibitorReleasedOnCancel, TestInhibitorDisabled,
  TestInhibitorDefaultsGracefully.
- **Rate limits pause instead of failing** — a stage hit with
  `*provider.RateLimitError` retries the same attempt after
  `RateLimitBackoff`, emitting `stage.rate_limited`/`stage.rate_limit_cleared`
  around the pause, with zero loop_back iterations consumed and no
  rate-limit text ever reaching an agent prompt; a provider that never
  recovers within `RateLimitMaxRetries` blocks the task with a note naming
  the rate limit, not a generic stage failure; and the lease heartbeat holds
  the task through the whole pause (proven with a sub-second TTL):
  TestRateLimitPausesThenSucceeds, TestRateLimitNeverRecoversBlocks,
  TestRateLimitPauseHoldsLease.
- **`when:` guards skip, visibly** — stage.skipped events, not silence:
  TestWhenGuardSkips.
- **Conflicts block loudly and abort cleanly** — the trunk stays clean, the
  conflicted files are named in the blocker note, the loser's branch and
  worktree survive: TestConflictingPairBlocksSecond (a real same-lines
  conflict needs both branches off a common base — concurrent tasks, not
  sequential ones).
- **Verify catches quiet conflicts** — two individually-clean merges whose
  combination breaks are caught on the merged tree: TestVerifyCatchesQuietConflict.
- **Commits report themselves** — the post-commit hook posts from a task
  worktree to the Data Service and the trailer linkage resolves:
  TestPostCommitHookReportsCommit (needs curl, as does the hook).
