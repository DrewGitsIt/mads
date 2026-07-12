# <service name>

<!--
The agent-maintainability contract (spec §15): every service README answers these four
questions so an agent (or engineer) can work on the service without reading the rest of
the system first. Keep every section current — a stale README here is a defect.
-->

One-paragraph statement of what this service owns and does. Name what it explicitly
does NOT do (its counterparties' jobs).

## Run it alone

```sh
make run        # exact command, required env/flags, default port
```

What you can poke at once it's up (e.g. `curl localhost:PORT/health`).

## Test it

```sh
make test       # always -race; must pass with no external dependencies
```

What the suite covers, where the goldens live, how to regenerate them (`-update`).

## API contract

The endpoints/streams this service exposes, and the contract tests that pin them
(pointer to `pkg/contracts` types and golden fixtures). List the service's
counterparties — per spec §15 every service has exactly two.

## Critical paths

The invariants that must not break, each with a pointer to the test that guards it
(e.g. "outbox atomicity — state change and event commit together: see
`store/outbox_test.go`").
