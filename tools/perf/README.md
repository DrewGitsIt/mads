# tools/perf: the local perf harness

Measures a baseline build against a candidate on hermetic stacks: event-bus
replay, connection starvation and the git routes, with correctness checks so
a faster design can't win by dropping state. Local dev tooling only: never
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
```

- `BASE`, `CAND`: a ref (checked out into a temp worktree) or a worktree dir
  (built in place). `CAND` is optional.
- `PROFILE=S|M|L|XL`, `N` repetitions, `BROWSER=chromium|electron`.
- `SCENARIOS=1,3b | bus | git | all | none`, `MODES=on,off` (proxy delays),
  `CHECKS=a,b | none`, `REALISM=1`, `GITFX=auto|1|0`, `GITFX_SRC`,
  `PERF_DELAYS`, `PERF_HOLD_OUTPUT=0`, `PERF_PROBE=0`, `PERF_PROBE_CAND=0`.
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
| seeder | [services/data/cmd/perfseed](../../services/data/cmd/perfseed) | Go, `-tags perf`: `seed`, `calibrate`, `realism`, `gitfx`, `append`. Stays there: it imports `services/data/internal/store` |
| specs | [ui/e2e/perf/](../../ui/e2e/perf) | Playwright/TypeScript: probe, delay proxy, collectors, scenarios, checks, report. Stays there: it needs ui's node_modules and `ui/e2e/lib/topology.ts` |
| fake provider dialects | [providers/fake](../../providers/fake) | `permission`, `MADS_FAKE_START_DELAY_MS`; overlaid on every build the runner makes |
| make targets | [Makefile](../../Makefile) | `ui-perf`, `perf-tools-test` |

## Safety rules

- Never run, build or `npm` in the live checkouts; the runner enforces it.
- Every stack is hermetic: temp `HOME`/`MADS_HOME`, ephemeral ports (never
  17373/17374/17380), an empty `MADS_AGENT_BIN_DIR`, UIs built with
  `VITE_ROUTER_URL=""` (a bundle naming a live port is refused).
- Real data is read only (`sqlite3 -readonly`); realism uses a backup copy
  in the run's temp dir, deleted at the end, and reports name nothing.
- The git fixture only clones FROM `GITFX_SRC`; all its git runs inside the
  run's temp tree with global config and hooks off.
- `node_modules` is installed (`npm ci`) or copy-on-write cloned, never
  symlinked.

## Guard rails (none of this can ship)

1. **Build tag.** Every Go file of perfseed and the git shim (tests too)
   starts with `//go:build perf`. Plain `go build ./...`, `go test ./...`,
   `go vet ./...`, golangci-lint, `make build`, the install script and the
   release gate never compile them; the runner and `make perf-tools-test`
   pass `-tags perf`.
2. **Dependency check.** `scripts/release-gate.sh` fails loudly if
   `go list -deps` of any release program (its `RELEASE_PKGS`, plus
   `services/mesh`) contains a package under `tools/`,
   `services/data/cmd/perfseed` or `ui/e2e`. The same check runs untagged in
   every `go test ./...` (`scripts/releasegate/devtools_test.go`), so CI
   catches it first.
3. **Source archives.** `.gitattributes` marks `tools/` and `ui/e2e/perf/`
   `export-ignore`; `TestSourceArchiveOmitsDevTools` proves `git archive`
   leaves them out.
