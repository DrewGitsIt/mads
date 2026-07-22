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
message supplied by the engine — and post-merge stages (verify) run on the
merged tree under the same lock. Before that trunk-side merge, the engine
merges current trunk into the task's own branch, inside its worktree
(rebase-before-merge, Wave 7): if trunk moved since the branch was cut and
git auto-resolves cleanly, a `build`-named stage (if the template defines
one) re-runs as a sanity gate; either way "trunk moved under me" is a
non-event, not a failure. If that in-worktree merge hits a real textual
conflict, the engine does **not** abort it — conflict markers and `MERGE_MSG`
are left exactly as git wrote them, and the merge stage fails with output
naming the conflicted files plus the titles/descriptions of the trunk-side
tasks whose merges introduced the conflicting changes (walked via `Task:`
trailers since the branch's base). With no `loop_back` configured, that's
still "task blocked, worktree and branch kept for inspection" as before; with
a template-level `merge_resolve` stage (see `on_loop` below), that failure
output is exactly the prompt a resolving agent needs.

Both the rebase-before-merge `git merge` and the trunk-side merge go through
[mergiraf](https://mergiraf.org) — a syntax-aware structural merge tool —
when it's available: `ensureSetup` registers it as a git merge driver
(`merge.mergiraf.driver` config + `* merge=mergiraf` in the shared repo's
`.git/info/attributes`, mergiraf's own documented integration, not a bespoke
whole-tree invocation) once per run, so adjacent, non-overlapping edits
between two task branches resolve automatically instead of conflicting on
shared diff context. Mergiraf's own conservative defaults still apply —
genuinely overlapping edits are left conflicted and flow into the same
`attributeConflict` path unchanged, diff3 markers and all. `Config.MergirafPath`
picks the binary explicitly (falling back cleanly if it doesn't resolve to a
real executable); empty falls back to a `PATH` lookup for `mergiraf`; not
found at all means plain `git merge`, silently and without error — either
way, which driver ran is on every `merge.attempted` event's `merge_driver`
field ("mergiraf" or "git"). Pinned version for this machine: **mergiraf
0.18.0**, installed via `cargo install --locked mergiraf` to
`~/.cargo/bin/mergiraf`.

**Wide concurrency, serialized integration.** `Config.Concurrency` (default
5) caps how many tasks run their *agent* stages at once — it is not a
collision-avoidance knob. A template dependency expresses a prerequisite
("this task's work needs that task's output"), not "don't run these two at
the same time"; two tasks with no dependency between them are meant to
overlap. What keeps that safe is the land queue, not the scheduler: every
task's merge stage — gate included, since the post-rebase gate runs inside
the same land attempt — funnels through `landQueue.acquire()`, a single
FIFO gate around `landAttempt` (`runTask`'s `release := e.wt.land.acquire()`
above). Exactly one land attempt is ever in flight, in stable arrival order,
regardless of how many agent stages are running concurrently — the
Not-Rocket-Science Rule (one trunk, one integrator at a time) applied to an
engine instead of a person. Raising `Concurrency` only widens how much agent
work overlaps *before* the land queue; it never lets two merges race each
other, and it never needs to, because a genuine collision already has a
correct outcome: the losing task blocks with the conflicted file and the
trunk-side task that landed first named in its blocker note (spec: real
conflicts come back with attribution, not silently broken trunk). Don't
"optimize" this by sharding the land queue per task or running merges off
the FIFO — that reintroduces exactly the interleaved-integration bugs this
design exists to rule out; if merges ever need to run faster, the answer is
a faster gate command, not more concurrent integrators.

The primary-side abort
in `worktrees.merge` remains as a rare defensive fallback for conflicts that
somehow reach the trunk merge itself. Uncommitted agent work is
auto-committed with the task trailer before merging (never lost, always
linked); merged worktrees are cleaned up, blocked ones kept for inspection
with their path in the note. A post-commit hook installed in the workspace
repo reports every commit — task context read from per-worktree git config —
to the Data Service (§5).

**A post-rebase gate failure is triaged, not taken at face value.** Rebase
conflicts (above) are the land queue's one deterministic failure mode; a gate
("verify") failure this late is exactly as likely to be flake/load
interference from the land queue's own serialization as a real regression —
both the task's own gate at fork and trunk's gate before this attempt were
already green. `triageGateFailure` (`triage.go`) reruns the failing portion
in the same worktree, serially (the land queue already holds exclusivity —
that's the isolation): when the gate output names specific Go packages (go
test's `FAIL` lines), only those packages rerun under `-race -count=1`, up to
3 times, stopping at the first green; otherwise the gate command itself
reruns once. Isolation-green retries the whole land attempt (rebase + full
gate) and emits `land.retried` with the classification and rerun counts,
bounded at 2 retries per land so a stubborn flake still surfaces to a human
rather than looping forever. Isolation-red blocks immediately — a
deterministic failure never costs more than that one triage round, fail fast
to a human — and the blocker note carries the evidence (failing packages,
isolation rerun results, trunk head) appended after the existing suspect-set
wording. Flaky tests are the canonical merge-queue killer; bounded retry with
recorded evidence is the standard countermeasure, never unbounded retry.

**Lint-red never lands, and a whitelisted-mechanical fix beats a block.**
Trunk went lint-red silently once (fixed at 672a6bc) because the land gate ran
build+test but not lint while task acceptance criteria required a green `make
check` — lint-red work landed and poisoned every later task that checked the
full bar. The fix is a three-rung ladder. Rung 1, prevention: the executor/
verify stage gate (any ordinary pre-land stage, e.g. a template's own `check`
stage) runs `make check`, not just build+test, so an in-diff lint error fails
that stage and blocks the task through the plain stage-failure path — same as
any other stage failure, before the task ever reaches the land queue.
Rung 2: the land gate itself runs `make check`, so the one class rung 1 can't
catch — two individually-clean branches combining lint-red post-rebase (stale
imports, formatting drift after a semantic merge) — is still caught here.
`classifyLintFailure` (`lintautofix.go`) recognizes golangci-lint-shaped issue
lines in the gate's output and is deterministic, never routed through
`triageGateFailure`'s flake isolation — a lint failure doesn't get more honest
on a rerun. If every distinct linter named is in the mechanical-autofix
whitelist (`gofmt`, `gofumpt`, `goimports` — purely syntactic, semantics-
preserving reformatting; never `errcheck`/`staticcheck`/`unused`/etc, since
"unused after merge" can mean the merge dropped a caller, which is
information for a human, not noise), `runLintAutofix` runs each available
whitelisted fixer, best-effort, against the merged tree. If the tree actually
changed, it's committed as its own clearly-labeled commit ("land: mechanical
lint fixes", distinct from the task's own work), a `land.autofixed` event
carries the fixer list and diff summary, and the full gate reruns exactly
once — bounded by an `autofixUsed` flag scoped to the whole land attempt, so a
fixer that doesn't converge falls through to a block rather than looping.
Rung 3: still red — whether non-whitelisted linters were named at all, or the
autofix didn't converge — blocks immediately with the lint evidence appended
to the blocker note, the same deterministic-failure treatment as any other
gate failure `triageGateFailure` classifies as non-flaky.
TestLintAutofixLandsAfterGofmtCombination,
TestLintAutofixSkippedForNonWhitelistedLinter,
TestLintAutofixBoundedFallsThroughToBlock,
TestPreLandStageCatchesLintBeforeLandQueue.

**Every block backs up the branch; every done cleans it up.** Whenever a task
blocks and its `mads/<task>` branch exists, the engine force-updates
`backup/<task>` to the branch head before recording the block, and names the
backup branch in the note — salvage-safe by construction, since claiming a
task (worktree creation) deliberately wipes any live worktree/branch left
over from a previous attempt. When a task reaches done, its `backup/<task>`
(if any) is deleted — stale-backup hygiene. The existing convention of
checking `backup/tsk_<id>` before re-deriving a design from scratch now holds
automatically for every block, not just the ones a shepherd happened to back
up by hand. Losing the lease mid-stage gets the same guarantee: before the
engine gives up on a task it no longer owns, it commits any dirty worktree
state and force-updates `backup/<task>` exactly like a block, so a
reaper-blocked task never loses uncommitted work even though the engine
writes no status for it — see "Suspend/wake aborts the orphaned stage" below.

Conventions the engine defines (until the spec §6 grammar grows richer):
- An **agent stage fails** when the provider errors or the reply's last line
  starts with `STAGE: fail` (gate agents are prompted to end with
  `STAGE: pass` / `STAGE: fail — reason`).
- `agent: auto` resolves to the `executor` system agent (quadrant routing is
  a later work item, §9).
- A stage literally named **`build`** with a non-empty `command:` is, by
  convention, the post-rebase sanity gate for isolated runs — symmetric with
  how a `git merge` command prefix (not a name) identifies the merge stage.
  Absent, the gate is simply skipped.
- Immediately before the trunk merge, any file named **`HANDOFF.md`** (any
  depth) still present on the task branch is removed and committed — the
  architect→executor handoff convention says the executor deletes it, but
  that's chronically forgotten; this is the backstop, not the primary path.
- A blocked task's branch is always mirrored to **`backup/<task>`** before
  the block is recorded, and cleaned up on done — see "Every block backs up
  the branch" above.

`when:` grammar (`pkg/template.Cond`, shared with the Data Service so editor
validation == launch validation, M9): `contains(labels, "x")` /
`!contains(labels, "x")`, and `on_loop` / `!on_loop` — true only when the
stage is entered via an `on_fail.loop_back` jump, never on its normal
forward-pass visit. This lets a template add a stage (e.g. a `merge_resolve`
agent) that costs nothing on the happy path and only runs after a failure
sends work back to it.

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

## Out of scope

- **Recency-based fallback for orphaned sessions.** `contracts.SessionActivity`
  is derived state (spec §1): active while an agent process runs. The two
  paths that flip it back to inactive on their own — the Router supervisor's
  `onSessionExit` for Router-driven sessions, and `cmd/mads/run.go`'s deferred
  cleanup for `mads run` sessions — both run only on paths that get a chance
  to execute cleanup code (normal exit, SIGINT/SIGTERM). Neither covers a
  process killed with `SIGKILL` or a host crash mid-run, which would leave a
  session stuck `active` forever. A recency-based fallback (e.g. treat a
  session as inactive if its last event is older than some TTL) would close
  that gap but is unbuilt — left as future work.

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
- **Suspend/wake aborts the orphaned stage** — a machine suspend stalls every
  heartbeat past the TTL at once (CLOCK_MONOTONIC, what the heartbeat ticker
  and the stage-timeout context both run on, pauses for the whole downtime,
  so neither fires on its own); on wake the reaper has already reclaimed the
  lease. The engine no longer silently re-takes it and resumes driving the
  task (that let it burn API usage against work it didn't own, and a
  suspend-during-block once combined with this to lose uncommitted work —
  the second and third Wave-4 runs surfaced this in turn). Instead, lease
  loss of any kind — a proactive wall-clock gap past the TTL since the last
  successful renewal, a renewal rejected outright, or a renewal that
  succeeded by re-taking a lease while the task's own status had already
  moved out from under the engine — kills the stage's agent process tree,
  commits and backs up the worktree the same way a block does, releases the
  lease, and writes no status: the reaper or the user owns the task now. The
  claim loop continues immediately, free to pick up other ready work:
  TestSuspendWakeAbortsTask, TestWallClockGapTriggersAbortWithoutRejectedRenewal,
  TestLeaseLossKillsAgentBacksUpAndContinuesClaiming. The stage-timeout
  watchdog uses the same wall clock, so a suspended machine can't extend an
  agent's budget by the sleep duration: TestStageTimeoutUsesWallClock.
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
- **A stale-but-conflict-free branch merges without blocking** —
  rebase-before-merge auto-resolves trunk having moved, and the build-named
  stage's sanity gate runs as part of it: TestStaleBranchMergesWithoutBlocking.
- **Conflicts are left for resolution, not aborted** — the trunk stays clean
  and holds the winner's version, but the loser's worktree keeps real
  conflict markers (not a `merge --abort`), and the blocker note names both
  the conflicted files and the trunk-side task whose merge introduced the
  conflicting change: TestConflictingPairBlocksSecond (a real same-lines
  conflict needs both branches off a common base — concurrent tasks, not
  sequential ones).
- **Wide concurrency never interleaves integration** — a 3-task batch (two
  racing for the same trunk position, one independent) run at the engine's
  actual default concurrency lands or blocks every task with attribution,
  keeps trunk green, and never opens a second land attempt's window before
  the first one's closes, even with agent stages running fully parallel:
  TestDefaultConcurrencyBatchLandsSerialized.
- **Mergiraf shrinks the conflict surface without softening real conflicts**
  — adjacent, non-overlapping edits to the same Go and TS files across two
  branches rebase clean through the driver: TestMergirafResolvesAdjacentEdits.
  Genuinely overlapping edits still conflict and still produce the
  `merge.conflicted` attribution event even with mergiraf active — its
  cautious defaults never silently resolve a suspicious case:
  TestMergirafStillConflictsOnOverlappingEdits. With the binary unavailable
  (a bad `Config.MergirafPath` or none found on `PATH`), the engine falls
  back to plain `git merge` with no behavior change beyond losing the
  auto-resolution — the same adjacent-edit scenario conflicts instead:
  TestMergirafAbsentFallsBackToPlainGit. All three assert which driver ran
  from the `merge.attempted` event's `merge_driver` field; these tests skip
  (rather than fail) on a machine without mergiraf installed, since it's an
  external, optional binary.
- **`on_loop` validates, skips the happy path, and enters after a loop-back**
  — a template with a `merge_resolve` stage guarded by `when: on_loop` and
  wired as the merge stage's `loop_back` target: TestOnLoopValidatesSkipsThenEntersMergeResolve.
- **HANDOFF.md never reaches trunk** — swept and committed immediately before
  the trunk merge, real work alongside it lands normally:
  TestHandoffMdSweptBeforeMerge.
- **Verify catches quiet conflicts** — two individually-clean merges whose
  combination breaks are caught on the merged tree: TestVerifyCatchesQuietConflict.
- **Commits report themselves** — the post-commit hook posts from a task
  worktree to the Data Service and the trailer linkage resolves:
  TestPostCommitHookReportsCommit (needs curl, as does the hook).
- **Every agent one-shot reports its cost** — the engine posts a
  `usage.recorded` event (model, tokens, `cost_usd`) after each agent stage's
  one-shot, and the Data Service's `GET /metrics/cost` aggregates them
  correctly by day/model and by session: TestUsageRecordedFeedsCostMetrics
  (provider-fake's scenarios script exact usage/cost per stage so the
  aggregates are asserted exactly, not just non-zero). The post is
  best-effort — a failure never fails the stage that already succeeded, only
  the cost metric is missing: TestUsageRecordFailureDoesNotBlockStage.
- **A flaky gate lands with no human intervention** — a gate that fails once
  then passes is isolated, classified `flake`, and the whole land attempt
  retries (`land.retried` carries the classification and rerun counts):
  TestGateFlakeAutoRetriesAndLands. A deterministic failure blocks after
  exactly one triage round, with failing packages, isolation results, and
  trunk head in the blocker note alongside the existing suspect-set wording,
  and never emits `land.retried`: TestGateDeterministicFailureBlocksWithEvidence.
  A gate that keeps looking flaky every time still blocks once the retry
  bound is hit — exactly `maxLandRetries` `land.retried` events, never more:
  TestGateFlakeRetryBoundedThenBlocks.
- **Every block is salvage-safe by construction** — `backup/<task>` exists
  and matches the worktree branch head the moment a task blocks (named in
  the blocker note), and is deleted once the task reaches done:
  TestBlockBacksUpWorktreeBranch, TestDoneDeletesStaleBackupBranch.
- **A gofmt-class post-rebase combination fix-lands itself** — two
  individually-clean branches that combine into a gofmt-dirty tree get a
  mechanical autofix commit, a `land.autofixed` event with the fixer list and
  diff summary, and a green rerun of the full gate, all before the task lands
  with both commits present: TestLintAutofixLandsAfterGofmtCombination. An
  errcheck-class (or any non-whitelisted-linter) gate failure never gets an
  autofix attempt at all and blocks immediately with the lint evidence, never
  flake-retried: TestLintAutofixSkippedForNonWhitelistedLinter. The autofix
  is bounded to exactly one attempt per land attempt — a gate that keeps
  reporting a whitelisted-linter failure after the one rerun falls through to
  a block rather than looping, proven by an exact gate-invocation count:
  TestLintAutofixBoundedFallsThroughToBlock. A lint-shaped failure in an
  ordinary pre-land stage fails that stage and blocks before the task ever
  reaches the land queue — the autofix machinery is scoped to the land gate
  alone and never engages there: TestPreLandStageCatchesLintBeforeLandQueue.
