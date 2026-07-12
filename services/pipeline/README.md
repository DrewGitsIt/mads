# Pipeline Engine

Executes pipeline templates over task graphs (spec §6): stage progression for agent and
command stages, `when:` conditions, `on_fail` loop-backs with max-loop caps, worktree-per-
task isolation, dependency-ordered merges, and the live work queue that picks up
same-session tasks created mid-run. Spawned per run by the Router. It does NOT define
templates (Data Service stores them; validation logic is shared) and does NOT talk to the
UI directly — progress is announced as `pipeline.*`/`stage.*` events.

**Status: wave-0 skeleton.** `/health` only. Wave-3 work items W3.1–W3.4
(docs/MADS-WORK-ITEMS.md) build it out — this service is the dogfood milestone.

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

Wire types live in `pkg/contracts`. Counterparties: the Router (run lifecycle) and the
Data Service (task claims via lease, template reads, events via outbox API).

## Critical paths

To be populated as W3.1+ land. The first entries will be: loop-back terminates at
max_loops → blocked, merge conflicts block loudly with feedback, cycle re-check on
mid-run task insertion.
