# Session Service

One process per active session, owning that session's agent through the provider layer
(spec §1, §11, §15): message send, output streaming to the Router, interrupt, restart,
resume, model switch, plus the session's worktree, file observation, and commit linkage.
Spawned and supervised by the Router on an ephemeral port. It does NOT persist state
directly — all writes go through the Data Service — and it never talks to other sessions.

**Status: wave-0 skeleton.** `/health` only. Wave-4 work items W4.3–W4.5
(docs/MADS-WORK-ITEMS.md) build it out.

## Run it alone

```sh
make run        # binds an ephemeral localhost port and logs it (-addr to pin)
curl localhost:<logged port>/health
```

## Test it

```sh
make test       # -race always; agent behavior comes from provider-fake, never a live LLM
```

## API contract

Wire types live in `pkg/contracts`. Counterparties: the Router (lifecycle + stream
forwarding) and the Data Service (state writes via lease, events via outbox API).

## Critical paths

To be populated as W4.3+ land. The first entries will be: provisioning event ordering,
attribution rules per isolation mode (§5), transcript normalization goldens.
