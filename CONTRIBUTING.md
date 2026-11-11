# Contributing to mads

mads is designed so that **agents can build and maintain it**: small,
independently runnable services with clear boundaries, and a test loop that
never needs a live LLM. Most changes land through mads's own pipeline (see
[mads builds mads](#mads-builds-mads-the-dogfood-loop)), but everything
here works just as well by hand.

## Setup

You'll need:

- **Go** at the version in `go.mod`
- **Node** at the version in `.nvmrc` (e.g. `nvm install`)
- **git**
- The **`claude`** CLI, for claude-code sessions and `mads run` agents
- Optionally, other agent CLIs (`codex`, `goose`, `hermes`, `kiro-cli`) for
  their sessions

**macOS:** mads builds and runs on macOS as well as Linux. Install the
Xcode Command Line Tools (`xcode-select --install`) first; they provide
`git` and the C++ compiler `npm ci` needs to build `node-pty`. Platform
differences and the port's open items are in
[docs/MACOS-PORT-DESIGN.md](docs/MACOS-PORT-DESIGN.md).

```sh
make build        # compiles everything and puts the mads CLI at bin/mads
scripts/app.sh    # starts the Data Service + Router, then the Electron app
```

`scripts/app.sh --restart` stops whatever is running and starts fresh
services from this checkout. See `scripts/app.sh --help` and
[scripts/README.md](scripts/README.md).

## Repo layout

| Path | What lives there |
|---|---|
| `docs/` | The canonical design docs: [REBUILD-SPEC.md](docs/REBUILD-SPEC.md) (the spec; §-references everywhere point here), [MADS-UX.md](docs/MADS-UX.md) (UX + data contracts), [MADS-WORK-ITEMS.md](docs/MADS-WORK-ITEMS.md) (the build plan), plus per-feature design docs and HTML mockups |
| `pkg/contracts` | Shared boundary contracts: event envelope + type registry, task schema, API error shape. Both sides of every service boundary consume this package and its goldens. |
| `pkg/golden` | Golden-file test helper (`-update` flag to regenerate) |
| `pkg/…` | Other shared packages used across services |
| `services/data` | Data Service: sole owner of persistent state and the event log (spec §15) |
| `services/router` | Router: always-on coordinator; supervision, SSE fan-out to the UI |
| `services/session` | Session Service: one process per active session, owns the agent |
| `services/pipeline` | Pipeline Engine: template execution over task graphs (spec §6) |
| `providers/` | Provider layer: the abstraction + conformance suite, one adapter per agent CLI, provider-fake, fast-model runner (see [providers/README.md](providers/README.md)) |
| `cmd/mads` | The `mads` CLI |
| `ui/` | Electron + TypeScript/React desktop app (see [ui/README.md](ui/README.md)) |
| `scripts/` | Installer, `app.sh` (local dev loop), smoke tests, release gate (see [scripts/README.md](scripts/README.md)) |

Every service directory has its own `README.md` (how to run it alone, how
to test it, its API contract, its critical paths) and its own `Makefile`,
so it can be built and tested in isolation. Many modules also have a
`HANDOFF.md`: the design doc an agent wrote before building it. Read it
before changing that module.

## Build and test

```sh
make check            # lint + test with -race; identical to CI
make test             # tests only
make build            # compiles everything and puts the mads CLI at bin/mads
make e2e-smoke        # full topology (data + router + session children) over a
                      # scripted fake-provider pipeline run; real OS processes,
                      # so it takes longer than a unit test
make ui-smoke         # Playwright journey suite: real binaries, real UI build,
                      # chromium only (see "Test layers" in ui/README.md)
make release-gate     # cross-platform build matrix + install/update smokes
                      # against the native candidate; a multi-minute cross-compile
make check-migrations # data-store migrations must number above latest main's
```

`make check` is the gate for every change. CI also runs `check-migrations`,
`e2e-smoke`, `ui-smoke`, and `release-gate`, plus a macOS job on pushes to
`main`.

## Rebuilding one piece while mads is running

`make build` is the all-or-nothing gate. For the inner loop there is a
target per component. None of them run `npm ci` (run `make ui-deps` once
after a `package-lock.json` change), and all are safe to run against a live
instance: `go build` replaces the output file by unlink+write, so running
processes keep the binary they started with.

```sh
make ui         # no restart at all: the Router serves ui/dist off disk and
                # re-checks it per request; just reload the window
                # (Ctrl+R / Cmd+R)
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

Each service also has `make -C services/<name> build`, alongside its `run`
and `test` targets.

> A rebuild doesn't restart anything that's already running. If behavior
> doesn't match the code, check how old the running Data Service, Router,
> and Electron processes are before assuming a bug.

## mads builds mads (the dogfood loop)

Work items are executed as mads tasks by mads's own pipeline:

```sh
make -C services/data run          # terminal 1: the Data Service (state + events)

make build                         # terminal 2:
SES=$(bin/mads new-session -name "My change")
bin/mads seed -session $SES -file tasks.yaml   # titles + depends_on graph
bin/mads run  -session $SES -workspace .       # claude-code agents, worktrees
```

`mads run` freezes the `default-implementation` template (architect →
executor → build → code review → merge → verify), gives every task its own
worktree and `mads/<task>` branch, runs agent stages as `claude -p`
one-shots in that worktree, loops review/build failures back to the
executor with feedback, serializes merges back to the trunk (conflicts
block loudly and keep the worktree), and tails every `pipeline.*`/`stage.*`
event to the terminal. Commits report themselves to the Data Service
through the installed post-commit hook and link to their tasks via the
`Task:` trailer (spec §5).

`-provider fake -fake-scenario file.yaml` rehearses a run with scripted
agents and spends no tokens. That is also how the CLI's own end-to-end test
runs.

The build plan lives in [docs/MADS-WORK-ITEMS.md](docs/MADS-WORK-ITEMS.md),
ordered by dependency waves rather than priority labels; the `*-tasks.yaml`
files under `docs/` are seed files for individual features.

## Rules that hold everywhere

These come from spec §15 and apply to every change:

- **No peer-to-peer service calls.** Services communicate through the Data
  Service event log (control plane) or stream ephemeral output through the
  Router (data plane).
- **State mutations and their events commit in one transaction**
  (transactional outbox).
- **Tests are deterministic.** Agent behavior in tests comes from
  `provider-fake`, never a live LLM. A flaky test is a defect: fix it or
  delete it within the week.
- **`-race` is always on.**
- **New providers pass the conformance suite.** Every adapter declares each
  behavior as native, emulated, or unsupported, and
  `conformance.Run` holds it to that claim in both directions.
