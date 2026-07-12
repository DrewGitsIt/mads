# Pipeline Engine

Executes pipeline templates over task graphs (spec §6): stage progression for agent and
command stages, `when:` conditions, `on_fail` loop-backs with max-loop caps, worktree-per-
task isolation, dependency-ordered merges, and the live work queue that picks up
same-session tasks created mid-run. Spawned per run by the Router. It does NOT define
templates (Data Service stores them; validation logic is shared) and does NOT talk to the
UI directly — progress is announced as `pipeline.*`/`stage.*` events.

**Status: W3.1 done — the stage machine lives.** `internal/engine` executes a
frozen template over a session's task graph: launch checks (shared
`pkg/template` validation, agent resolution frozen at start, cycle check per
§3.6), a continuous dependency-ordered scheduler under a concurrency cap,
agent stages as provider one-shots, command stages via `sh -c`, `when:`
guards (grammar in `pkg/template` so the editor rejects exactly what launch
rejects), and `on_fail` loop-backs that feed failure output back into the
next agent attempt before blocking at max_loops. Every running task is
leased; foreign-leased tasks are left untouched. Next: W3.2 worktrees +
merge/verify, W3.3–W3.4 `mads run`.

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
- **`when:` guards skip, visibly** — stage.skipped events, not silence:
  TestWhenGuardSkips.
