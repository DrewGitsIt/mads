# tools/perf: the local perf harness

Measures a baseline build against a candidate on hermetic stacks: event-bus
replay, connection starvation, the git routes and the chat view's cost per
streamed batch, with correctness checks so a faster design can't win by
dropping state. Local dev tooling only: never
in CI, never in a release (see "Guard rails").

Design, metrics and the settle rule: [docs/PLAN.md](docs/PLAN.md).
Completed record of the event-bus work: [docs/EVENT-BUS-RESULTS.md](docs/EVENT-BUS-RESULTS.md).
Git routes: [docs/GIT-STATE-DESIGN.md](../../docs/GIT-STATE-DESIGN.md) and
[docs/git-routes-overview.html](../../docs/git-routes-overview.html).

## Run it

From a worktree that is not a live checkout (the runner refuses
`~/repo/mads` and `~/repo/mads-mesh`; `PERF_FORBIDDEN_DIRS` overrides):

```sh
make ui-perf BASE=origin/main PROFILE=S N=1 SCENARIOS=1,9c CHECKS=a   # quick smoke
make ui-perf BASE=origin/main CAND=../mads-feature PROFILE=L N=5      # a comparison
make ui-perf BASE=main CAND=../mads-chat-fix PROFILE=S N=3 SCENARIOS=chat CHECKS=none   # the chat stream
```

- `BASE`, `CAND`: a ref (checked out into a temp worktree) or a worktree dir
  (built in place). `CAND` is optional.
- `PROFILE=S|M|L|XL`, `N` repetitions, `BROWSER=chromium|electron`.
- `SCENARIOS=1,3b | bus | git | chat | all | none`, `MODES=on,off` (proxy delays),
  `CHECKS=a,b | none`, `REALISM=1`, `GITFX=auto|1|0`, `GITFX_SRC`,
  `PERF_DELAYS`, `PERF_HOLD_OUTPUT=0`, `PERF_PROBE=0`, `PERF_PROBE_CAND=0`,
  `CHAT_SRC=synthetic|<transcript.jsonl>`, `CHAT_TURNS`, `PERF_CHAT_*`.
  All documented at the top of [run.sh](run.sh) and in PLAN.md §6.
- Results go to `tools/perf/results/<timestamp>/` (`report.md`,
  `results.json`, `raw/`, `stack-logs/`). That dir is gitignored except
  [results/CITED.txt](results/CITED.txt), the runs the docs cite.

Unit tests:

```sh
make perf-tools-test                                    # Go: perfseed + git shim (-tags perf)
npx --prefix ui vitest run --config e2e/perf/vitest.perf.config.ts   # report, scenario aliases
npx --prefix ui tsc -p ui/e2e/perf                      # type-check the specs
```

## What lives where

| Piece | Where | What |
|---|---|---|
| runner | [run.sh](run.sh) | `make ui-perf`: validates, builds each side, seeds one workload, runs Playwright, reaps every process, writes results |
| scenario aliases | [scenarioSets.sh](scenarioSets.sh) | `bus`, `git`, `all` → ids (sourced by run.sh; tested by `ui/e2e/perf/scenarioSets.test.ts`) |
| isolation watch | [isolationWatch.sh](isolationWatch.sh) | realism runs: watches stack processes for git/agent children, files outside the run's temp tree, non-loopback connections |
| git shim | [gitshim/](gitshim/) | Go, `-tags perf`: first on a fixture stack's PATH, runs the real git and logs each call with its CPU |
| profiles | [profiles/](profiles/) | S/M/L/XL workloads for `perfseed seed`; `gitfx.shape.json` for `perfseed gitfx` |
| docs | [docs/](docs/) | PLAN.md (design), EVENT-BUS-RESULTS.md (record) |
| results | `results/` | output, gitignored (CITED.txt kept) |
| seeder | [services/data/cmd/perfseed](../../services/data/cmd/perfseed) | Go, `-tags perf`: `seed`, `calibrate`, `realism`, `gitfx`, `append`, `chat`. Stays there: it imports `services/data/internal/store` |
| specs | [ui/e2e/perf/](../../ui/e2e/perf) | Playwright/TypeScript: probe, delay proxy, collectors, scenarios, checks, report. Stays there: it needs ui's node_modules and `ui/e2e/lib/topology.ts` |
| fake provider dialects | [providers/fake/perf.go](../../providers/fake/perf.go) | `permission`, `perf-stream`, `MADS_FAKE_START_DELAY_MS`, `MADS_FAKE_PERF_HISTORY_DIR` (`-tags perf`); overlaid on every build the runner makes |
| chat scenarios | [ui/e2e/perf/chat.ts](../../ui/e2e/perf/chat.ts) | 10x: the in-page recorder, the trace split, the checks (PLAN.md, "Chat stream") |
| make targets | [Makefile](../../Makefile) | `ui-perf`, `perf-tools-test` |

## Safety rules

- Never run, build or `npm` in the live checkouts; the runner enforces it.
- Every stack is hermetic: temp `HOME`/`MADS_HOME`, ephemeral ports (never
  17373/17374/17380), an empty `MADS_AGENT_BIN_DIR`, UIs built with
  `VITE_ROUTER_URL=""` (a bundle naming a live port is refused).
- Real data is read only (`sqlite3 -readonly`); realism uses a backup copy
  in the run's temp dir, deleted at the end, and reports name nothing. A
  real `CHAT_SRC` transcript is copied into the run's temp dir and only
  read; what is made from it is deleted with the run, and reports say
  "real" and its shape, never its name or content.
- The git fixture only clones FROM `GITFX_SRC`; all its git runs inside the
  run's temp tree with global config and hooks off.
- `node_modules` is installed (`npm ci`) or copy-on-write cloned, never
  symlinked.

## Guard rails

What is guaranteed:
- **No release binary or UI bundle contains perf code.** The Go side is
  behind the `perf` build tag (below); the UI bundle is built from
  `ui/src`, which imports nothing from `ui/e2e`.
- **Source archives omit it.** `git archive` (and GitHub's source tarballs)
  leave out `tools/` and `ui/e2e/perf/`.
- **A git clone still has it on disk.** `scripts/install.sh` currently
  installs from a clone, so the source is there, as every test is; it is
  never compiled into what gets installed.

Not changed here (an open question for Drew): the fake provider itself
(`providers/fake`, which predates the harness) is linked into the release
binaries. Only the harness's additions to it are gated.

How:
1. **Build tag.** Every Go file of perfseed, the git shim (tests too) and
   `providers/fake/perf.go` / `perf_test.go` (the `permission` and
   `perf-stream` dialects, `MADS_FAKE_START_DELAY_MS`,
   `MADS_FAKE_PERF_HISTORY_DIR`) starts with `//go:build perf`; the untagged
   fake keeps only no-op hooks. Plain `go build/test/vet ./...`,
   golangci-lint, `make build`, the install script and the release gate
   never compile them. The runner builds perfseed, the git shim and the
   services with `-tags perf`; `make perf-tools-test` vets, lints and tests
   them with the tag. `TestPerfCodeIsTagged` fails on an untagged perf
   file; `providers/fake`'s `TestNoPerfDialectsUntagged` and
   `TestNoPerfStreamOrHistoryUntagged` prove an untagged build has none of
   the additions.
2. **Dependency and binary checks.** `scripts/release-gate.sh` fails loudly
   if `go list -deps` of any release program (its `RELEASE_PKGS`, plus
   `services/mesh`) contains a package under `tools/`,
   `services/data/cmd/perfseed` or `ui/e2e`, and if a built release binary
   contains the fake's perf-only strings. The deps check also runs untagged
   in every `go test ./...` (`scripts/releasegate/devtools_test.go`), so CI
   catches it first.
3. **Source archives.** `.gitattributes` marks `tools/` and `ui/e2e/perf/`
   `export-ignore`; `TestSourceArchiveOmitsDevTools` proves `git archive`
   leaves them out. `ui/e2e/playwright.config.ts` ignores `perf/**`, so
   `make ui-smoke` never collects the harness.
