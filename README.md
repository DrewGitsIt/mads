# mads — Making Agents Do Stuff

An agent orchestration platform with three goals: make agents easy for humans to use,
give agents the tools to do their best work, and expose everything agents do through
logging and data collection.

mads is a ground-up rebuild of its predecessor, tao, designed so that **agents can build
and maintain it**: small, independently runnable services with clear boundaries and a
test loop that never requires a live LLM.

## Layout

| Path | What lives there |
|---|---|
| `docs/` | The canonical design docs: [REBUILD-SPEC.md](docs/REBUILD-SPEC.md) (the spec, §-references everywhere point here), [MADS-UX.md](docs/MADS-UX.md) (UX + data contracts), [MADS-WORK-ITEMS.md](docs/MADS-WORK-ITEMS.md) (the build plan), `ux-prototype.html` (clickable mockups 1–9) |
| `pkg/contracts` | Shared boundary contracts: event envelope + type registry, task schema, API error shape. Both sides of every service boundary consume this package and its goldens. |
| `pkg/golden` | Golden-file test helper (`-update` flag to regenerate) |
| `services/data` | Data Service — sole owner of persistent state and the event log (spec §15) |
| `services/router` | Router — always-on coordinator; supervision, SSE fan-out to the UI |
| `services/session` | Session Service — one process per active session, owns the agent |
| `services/pipeline` | Pipeline Engine — template execution over task graphs (spec §6) |
| `providers/` | Provider adapters (claude-code, kiro-cli, provider-fake) — wave 2 |
| `ui/` | Electron desktop app — wave 6 |

## Working on mads

```sh
make check   # lint + test with -race; identical to CI
make test    # tests only
```

Every service directory has its own `README.md` (the agent-maintainability contract:
how to run it alone, how to test it, its API contract, its critical paths) and its own
`Makefile` so it can be built and tested in isolation.

Rules that hold everywhere (spec §15):

- **No peer-to-peer service calls.** Services communicate through the Data Service
  event log (control plane) or stream ephemeral output through the Router (data plane).
- **State mutations and their events commit in one transaction** (transactional outbox).
- **Tests are deterministic.** Agent behavior in tests comes from `provider-fake`,
  never a live LLM. A flaky test is a defect: fix it or delete it within the week.
- **`-race` is always on.**

## How mads gets built

The build order is [docs/MADS-WORK-ITEMS.md](docs/MADS-WORK-ITEMS.md): dependency waves,
never priority labels. Waves 0–3 are built directly; after the dogfood milestone
(`mads run`), remaining work items are imported as mads tasks and executed by mads's own
pipeline.
