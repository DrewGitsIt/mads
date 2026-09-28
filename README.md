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
| `providers/` | Provider layer: the abstraction + conformance suite, claude-code adapter, provider-fake, fast-model runner |
| `cmd/mads` | The bootstrap CLI — see **mads builds mads** below |
| `ui/` | Electron desktop app — wave 6 |
| `scripts/` | `install.sh` (curl-to-shell installer) + its smoke test, `app.sh` (local dev loop) — see [scripts/README.md](scripts/README.md) |

## Working on mads

```sh
make check         # lint + test with -race; identical to CI
make test          # tests only
make build         # compiles everything and puts the mads CLI at bin/mads
make e2e-smoke     # full topology (data + router + session children) over a
                   # scripted fake-provider pipeline run; not part of `make
                   # check` — real OS processes, takes longer than a unit test
make release-gate  # cross-platform build matrix + install/update smokes
                   # against the native candidate; not part of `make check`
                   # — a multi-minute cross-compile
```

### Rebuilding one piece while mads is running

`make build` is the all-or-nothing gate. For the inner loop there is a target
per component; all of them skip `npm ci` and are safe to run against a live
instance (`go build` replaces the output file by unlink+write, so running
processes keep the binary they started with).

```sh
make ui         # no restart at all — the Router serves ui/dist off disk and
                # re-checks it per request; just reload the window (Ctrl+R)
make session    # next session spawn picks it up (-session-cmd is exec'd
                # fresh); to move a live session onto it:
                #   curl -sX POST localhost:17373/sessions/<id>/bounce
make pipeline   # next pipeline run picks it up, same mechanism
make data       # needs a restart, but a survivable one: state is in
                # ~/.mads/mads.db and the Router's watchers reconnect
make router     # needs a restart, and it terminates every session and
                # pipeline child. SIGTERM only:
                #   kill "$(cat ~/.mads/router.pid)"
                # then relaunch from the repo root (-ui-dist is cwd-relative)
make services   # all five Go binaries, no UI (~2s warm)
```

Each service also has `make -C services/<name> build`, alongside its existing
`run` and `test`.

## mads builds mads (the dogfood loop)

From Wave 4 on, work items are executed as mads tasks by the pipeline itself.
This is **the** way mads builds mads:

```sh
make -C services/data run          # terminal 1: the Data Service (state + events)

make build                         # terminal 2:
SES=$(bin/mads new-session -name "Wave 4")
bin/mads seed -session $SES -file wave4-tasks.yaml   # titles + depends_on graph
bin/mads run  -session $SES -workspace .             # claude-code agents, worktrees
```

`mads run` freezes the `default-implementation` template (architect → executor
→ build → code review → merge → verify), gives every task its own worktree
and `mads/<task>` branch, runs agent stages as `claude -p` one-shots in that
worktree, loops review/build failures back to the executor with feedback,
serializes merges back to the trunk (conflicts block loudly and keep the
worktree), and tails every `pipeline.*`/`stage.*` event to the terminal.
Commits report themselves to the Data Service through the installed
post-commit hook and link to their tasks via the `Task:` trailer (spec §5).

`-provider fake -fake-scenario file.yaml` rehearses a run with scripted
agents — no tokens spent; that is also how the CLI's own end-to-end test runs.

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
