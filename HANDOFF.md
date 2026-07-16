# W7.7 Full E2E smoke & release gate — HANDOFF

Architect stage output for W7.7 (spec §15, `docs/MADS-WORK-ITEMS.md`). Read this
before writing code. Delete this file as part of the PR that lands the work
item (repo convention — see `git log --all --diff-filter=A -- '*HANDOFF.md'`;
root/`scripts/`-level HANDOFF.md files are transient).

## Two important corrections to the work item's premise

Both were verified against the actual code, not assumed from the spec/work-item
prose — do not skip re-verifying these if the code has moved since this was
written.

### 1. No W4-era headless smoke exists to extend

The work item says "the W4-era headless smoke may partially exist — find and
extend rather than duplicate." It does not exist. Searched for `headless`,
`TestMain`, and every directory containing a `_test.go` — the closest
relatives are:
- `scripts/smoketest` (W7.2 install/first-run/uninstall) — spawns real `data`
  and `mads` binaries, but never a Router or a pipeline run.
- `services/router/internal/api/api_test.go` — spawns a *fake* `testchild`
  binary via the Supervisor, not the real `session`/`pipeline` binaries, and
  drives everything through `httptest.NewServer` in-process. Good pattern
  reference (see `testleakcheck` below) but not the thing to extend.
- `services/pipeline/engine/engine_test.go`'s `harness` — the closest thing to
  a real pipeline run (real Data Service via `datatest`, real git repo, real
  `fake` provider), but entirely in-process (`engine.Run` called directly, no
  child processes, no Router).

So this work item is building the full-topology smoke from scratch, closely
modeled on `scripts/smoketest`'s binary-spawning conventions and
`engine_test.go`'s workspace-fixture conventions. That satisfies "reuse
datatest/harness patterns" — there's no existing full-topology test to extend.

### 2. The `pipeline` binary does not run pipelines — a real, pre-existing gap

`services/pipeline/main.go` is, verbatim, a "Wave-0 skeleton" that serves only
`GET /health`. It never calls `services/pipeline/engine.Run`. The Router's
`POST /pipelines` handler (`services/router/internal/api/pipelines.go`) spawns
this stub as a child process and returns its address for SSE forwarding
(`stream_forward.go`) — but nothing ever tells the spawned process to actually
execute a template. **A pipeline started via the Router's HTTP API today does
nothing.**

The actual, working, tested way to run a pipeline in this codebase is
`cmd/mads run` (`cmd/mads/run.go`): it calls `engine.Run` **in-process**,
against a real Data Service over HTTP, and explicitly supports
`-provider fake` for scripted rehearsal runs. The root README says as much:
*"`-provider fake -fake-scenario file.yaml` rehearses a run with scripted
agents — no tokens spent; that is also how the CLI's own end-to-end test
runs."* This is the wave-4 dogfood path, not a test-only shim.

**Decision (do not silently re-scope around this):** the E2E smoke drives the
pipeline-run leg via a real subprocess of the built `mads` binary
(`mads run -provider fake ...`), not via `POST /pipelines`. This is a genuine,
already-wired, already-documented execution path using a real built binary —
it satisfies "pipeline engine ... from built binaries" honestly, just not via
the Router endpoint of the same name. Wiring `services/pipeline/main.go` to
actually invoke `engine.Run` over HTTP is a real missing feature, but it's a
Pipeline Engine / Router feature change, not test-gate scaffolding — out of
scope for this item. **Flag it to Drew explicitly** (same judgment call
pattern as the MADS-UX spec gaps: verify against code, decide (a) cheap/in-
scope vs (b) real gap/flag, don't silently build around it or silently expand
scope). Router still spawns a *real* `session` child in this design (via
`POST /sessions`), so the "router → session children" half of the topology
claim is genuinely exercised; only the pipeline-engine-as-a-spawned-child half
is substituted.

## Scope decisions already made for you (don't relitigate)

- **No Playwright / browser automation, anywhere.** This is explicit in the
  task and confirmed absent from the repo (`grep -rl playwright` → nothing,
  `ui/` has no e2e directory). The UI test strategy is a pending decision with
  Drew.
- **`providers/fake` only, never a live LLM or `-fake-scenario`.** Default
  `fake.New()` (no scenario) replies `"ok"` to any prompt — no scenario is
  needed for a green run: `engine`'s `FailMarker` convention only fails a
  stage when the reply's last line starts with `STAGE: fail`; plain `"ok"`
  passes every stage including `code_review`. Confirmed by
  `engine_test.go`'s `TestConcurrencyLimit`/`TestForeignLeaseSkipsTask`, which
  run the full template against bare `fake.New()`.
- **SQLite driver is pure Go** (`modernc.org/sqlite`, not cgo). Cross-compiling
  the release-gate's build matrix (`GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go
  build ./...`) needs no C toolchain. This makes "build-only for non-native"
  simple — no cross-compiler setup required in CI.
- **The default template and its system agents are already seeded** by
  migration, not something this task needs to create:
  `services/data/internal/store/migrations/0006_templates.up.sql` seeds
  `default-implementation` (architect → executor → build → code_review →
  merge → verify); `0005_registries.up.sql` seeds the `architect`/`executor`/
  `code-reviewer` system agents it references. A real Data Service built from
  `store.Migrate` has everything `mads run -template default-implementation`
  needs out of the box.

## Part 1 — Full-topology E2E smoke (`make e2e-smoke`)

### New package: `scripts/e2esmoke/`

Build-tagged `//go:build e2e_smoke` (new convention — see "Why build tags"
below). One `main_test.go`, structured like `scripts/smoketest/install_test.go`:

**`TestMain`**: build real `mads`, `data`, `router`, `session` binaries via
`go build -o <tmp>/<name> ./cmd/mads` etc. into a temp dir (skip `pipeline` —
per the gap above, nothing spawns it in this design; see "loose end" below).
Run `m.Run()`, then call `testleakcheck.Verify` (see Teardown) before
`os.Exit`.

**Test flow** (one test function driving the whole scenario — the steps are
sequentially dependent, splitting into independent `t.Run` subtests would
just add ceremony):

1. **Start Data Service** as a real process: `data -addr 127.0.0.1:0
   -data-dir <tmp>`. Discover its listen address the same way
   `install_test.go`'s `startDataService` does — scan stderr for `listening
   on (\S+)` (same regex `services/router/internal/child/child.go` uses; this
   is the whole codebase's log-line convention, not something specific to one
   test). Wait for `GET /health`.

2. **Start Router** as a real process: `router -addr 127.0.0.1:0 -data-url
   <dataAddr> -session-cmd <sessionBinPath> -pipeline-cmd <sessionBinPath>`.
   `-pipeline-cmd` deliberately points at the harmless `session` binary (or
   any binary that logs `listening on`), not a real pipeline binary — since
   nothing in this test flow triggers `POST /pipelines`, and there's no
   working pipeline binary to spawn per the gap above; pointing it somewhere
   valid-but-inert just avoids `flag.Parse` errors if the Router ever tries
   to use it. Same stderr-scan pattern for discovery. Wait for `GET /health`.

3. **Create a session — through the Router**, not `dataclient` directly:
   `POST <router>/sessions` with `{"provider":"fake","workspace":"<tmp
   workspace repo>"}`. **Do this as a raw `net/http` POST, not
   `pkg/dataclient.Client.CreateSession`** — Router's handler
   (`services/router/internal/api/sessions.go`) returns
   `{"session":{...},"addr":"..."}`, but `dataclient.CreateSession` decodes
   the bare `contracts.Session` shape Data Service itself returns; pointed at
   Router it would silently decode into a zero-value session. Unmarshal the
   wrapper by hand, keep the session ID. This call makes the Router spawn a
   real `session` child — this is the "router → session children" proof.

4. **Seed 2–3 tasks**: `POST <router>/tasks/import` with `{"tasks":[...]}`,
   `Source: "user"`, no explicit status (matches `engine_test.go`'s
   `harness.seed` — `ImportTasks` defaults them to ready/confirmed). Router's
   `handleImportTasks` wraps its response the same way `dataclient.ImportTasks`
   expects (`{"tasks":[...]}`), so `dataclient.New(routerURL,
   "e2esmoke").ImportTasks(...)` works fine here — only `CreateSession` needed
   the manual workaround. `Tasks()`, `MetricsCost()`, `Retro()`/`Retros()`
   are likewise bare-shaped and safe to call against the Router base URL
   through `dataclient.Client`.

5. **Set up the workspace repo** the pipeline run needs: same fixture as
   `engine_test.go`'s `newHarness` — a `Makefile` with `build:`/`test:`
   targets that just `@true`, `git init -b main`, commit, and the
   `merge.defaultToUpstream`/`branch.main.*` git config so the template's bare
   `git merge --no-ff` merge stage works without an explicit remote.

6. **Run the pipeline** — the substituted leg: run the built `mads` binary as
   a real subprocess: `mads run -data <dataAddr> -session <id> -workspace
   <repo> -provider fake -stage-timeout 30s`. Wait for it to exit. Exit code
   0 means every task reached `done` (`cmd/mads/run.go` returns a non-nil
   error — `os.Exit(1)` — if `len(Blocked) > 0 || len(Skipped) > 0`). This is
   the primary success signal; optionally double-check with `GET
   <router>/tasks?session_id=<id>&status=done` after, for a belt-and-suspenders
   assertion with a clearer failure message than a bare exit code.

7. **Retro**: raw HTTP `POST <router>/retros` with `{"session_ids":[id]}`
   (this spawns an async `job.Runner` job — no `dataclient` method wraps
   *starting* one, only reading). Poll `dataclient.Retro(ctx, retroID)`
   against the Router URL every ~200ms until it returns without
   `dataclient.ErrNotFound` (bounded timeout, e.g. 30s) — `CreateRetro` is
   the job's only write, so a 200 here means generation actually completed.
   Assert the retro has the session in `SessionIDs`.

8. **Cost metrics**: `dataclient.MetricsCost(ctx, sessionID)` against the
   Router URL — assert non-zero (the engine posts a `usage.recorded` event
   per agent stage one-shot; `fake` always reports non-zero deterministic
   usage, see `providers/fake/fake.go`'s `usage()`).

9. **Teardown, in order**: `POST <router>/sessions/{id}/terminate` (graceful,
   blocks until the Supervisor's exit handling — including lease release —
   completes server-side); SIGTERM the Router process and wait (Router's
   `main.go` handles SIGTERM by draining HTTP and calling
   `sup.Shutdown`, which terminates every tracked child); SIGTERM the Data
   Service and wait. Then `testleakcheck.Verify(sessionBinPath)` — this is
   the "zero orphan processes" proof (mirrors
   `services/router/internal/api/api_test.go`'s `TestMain` exactly). Also
   verify the Router and Data Service PIDs themselves exited cleanly (`Wait()`
   returning is sufficient; no separate `/proc` scan needed since you hold
   their `*exec.Cmd` directly).

**Loose end worth flagging in the PR description, not silently fixing**: this
design never actually spawns the `pipeline` binary (the gap in point 2 above
means there's nothing useful to spawn), so `testleakcheck` on it would be
vacuous. Don't bother building/checking it. If `services/pipeline/main.go`
gets wired to `engine.Run` in a future work item, this smoke should grow a
second variant that drives the run via `POST /pipelines` instead of `mads
run`, to actually prove that path. Leave a `// TODO` comment saying so, not a
silent gap.

### Why build tags, not just "let it run in `make test`"

This test spins up 2–3 real OS processes, does real git operations, and runs
a real (if fast) pipeline — call it low-single-digit seconds to maybe a
minute, not the sub-second unit-test norm elsewhere in the repo. Folding it
into `go test -race ./...` would slow down every `make check`/CI `test` run,
which the work item explicitly doesn't want ("kept to minutes" as its own
gate, separate from the acceptance criterion that `make check` still passes —
implying it stays out of the default sweep).

There's no existing build-tag precedent in this repo (`grep -rl '^//go:build'`
→ nothing) — W7.1's "recorded-fixture smokes opt-in" language suggested one
might exist, but it doesn't yet. This work item establishes the pattern:

```go
//go:build e2e_smoke

package e2esmoke
```

A directory containing only a tagged `_test.go` file is invisible to `go
build ./...`, `go vet ./...`, and `golangci-lint run ./...` when the tag isn't
passed — no error, just an empty package, same as it not existing. `make
e2e-smoke` passes the tag explicitly. `make check`/CI's `test` job doesn't, so
nothing changes for them.

## Part 2 — Release gate (`make release-gate`)

### `scripts/release-gate.sh` (new, modeled on `scripts/install.sh`'s
env-override style)

```sh
MADS_RELEASE_CANDIDATE_DIR=<dir>   # skip the build matrix, test this dir instead
```

Steps:

1. **If `MADS_RELEASE_CANDIDATE_DIR` is unset**: cross-build the matrix —
   `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` (Windows
   explicitly out of scope per the task). For every combo except the runner's
   native `GOOS/GOARCH`: `CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build
   ./cmd/mads` (and the three service binaries) — build-only, discard output,
   just prove it compiles. For the native combo: build all of `mads`, `data`,
   `router`, `session`, `pipeline` into `dist/native/` — this becomes the
   candidate.
   **If it's set**: skip straight to step 2 with that directory as the
   candidate. This override exists *specifically* so the negative test (below)
   doesn't need to redo a multi-minute cross-compile just to prove a broken
   binary fails — same reasoning as `install.sh`'s `MADS_INSTALL_SOURCE`.

2. **Health check**: run `<candidate>/mads --version`; must exit 0. This is
   the exact convention `pkg/update.Apply` already uses to decide
   rollback (see `cmd/mads/update_test.go`'s
   `TestUpdateApplyForcedFailureRollsBack` — "a broken 'new' binary that
   exits non-zero on `--version`"). Reusing it here means the negative test
   (below) needs no new failure convention invented.

3. **Install/first-run/uninstall smoke** (W7.2): `MADS_INSTALL_SOURCE=
   <candidate> go test -count=1 ./scripts/smoketest/...`. This requires a
   small, backward-compatible change to `scripts/smoketest/install_test.go`'s
   `TestMain`: prefer binaries from `$MADS_RELEASE_CANDIDATE_DIR` (or
   whatever env var name you settle on — keep it consistent with step 1) when
   set, falling back to its current self-build behavior when unset, so `go
   test ./scripts/smoketest/...` run standalone (still part of `make
   check`/CI's `test` job, unchanged) keeps working exactly as today. This is
   the "find and extend rather than duplicate" instruction actually applying
   — do this one, not the (nonexistent) W4 headless smoke.

4. **Update smoke** (W7.5): this one already exists and already passes —
   `cmd/mads/update_test.go`'s `TestUpdateApplyForcedFailureRollsBack` and
   `TestUpdateApplyNoRouterRunning` are exactly the "forced mid-update failure
   rolls back cleanly" and happy-path proofs the work item asks for. Just run
   them: `go test -count=1 -run TestUpdateApply ./cmd/mads/...`. Note these
   tests build their own synthetic "old"/"new" binaries (`withFakeBinary`,
   shell scripts served over an httptest feed) rather than exercising the
   actual `dist/native/mads` candidate — that's how W7.5 already built them
   and it's out of this item's scope to change; call this out as a known gap
   in the PR description rather than silently expanding scope to rewire it.

5. Exit non-zero if any step failed. Simple `set -e` plus explicit `|| exit
   1` after each `go test` (so failures aren't masked by later steps still
   running) is enough — no need for anything fancier.

### New package: `scripts/releasegate/`

Build-tagged `//go:build release_gate`, same reasoning as `e2e_smoke` above
(cross-compiling 4 platforms is slow; keep it out of `make check`/CI's fast
jobs).

**One test, the negative proof the work item's done-when requires**:
`TestReleaseGateFailsBrokenCandidate`. Build a tiny temp "candidate" directory
containing only a broken `mads`: a shell script `#!/bin/sh\nexit 1\n` made
executable (no need for real `data`/`router`/`session`/`pipeline` binaries —
step 2's health check fails before the script would ever reach step 3, so the
test stays fast). Run `sh scripts/release-gate.sh` as a subprocess with
`MADS_RELEASE_CANDIDATE_DIR` set to that directory. Assert
`(*exec.Cmd).Run()` returns a non-nil error (non-zero exit) **and** the
combined output names the health-check step specifically — not just "some
test somewhere failed" — so a future regression that breaks the gate for an
unrelated reason doesn't get masked by an assertion that's too loose to tell
the difference.

No positive-path Go test is needed in this package: `make release-gate`
itself succeeding (locally, and as a CI job) *is* the positive proof, the
same way `make e2e-smoke` succeeding is the positive proof for Part 1 — don't
add a redundant meta-test that just re-runs the same script.

## Makefile additions

```make
.PHONY: e2e-smoke release-gate

e2e-smoke:
	go test -tags e2e_smoke -count=1 -v ./scripts/e2esmoke/...

release-gate:
	sh scripts/release-gate.sh
	go test -tags release_gate -count=1 -v ./scripts/releasegate/...
```

(`release-gate`'s Go test runs *after* the shell script so the negative-proof
test's own `MADS_RELEASE_CANDIDATE_DIR` override doesn't fight with the
top-level script's real matrix build — they're independent invocations of the
same script with different env.)

## CI wiring — extend `.github/workflows/ci.yml`

Add two jobs alongside the existing `lint`/`test`, same `on:` trigger block
(push to `main` + every PR — matches the existing jobs and spec §15's "on
every merge" without inventing a different cadence):

```yaml
  e2e-smoke:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make e2e-smoke

  release-gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make release-gate
```

Native platform in CI is `linux/amd64` — matches `scripts/smoketest`'s
existing `requireLocalRunner` skip (darwin execution already skips with a
"pending CI" message; this work item adds darwin/arm64 and darwin/amd64
**build** coverage to the matrix, but doesn't add a macOS runner, so darwin
execution smoke stays pending exactly as `scripts/README.md` already
documents). Don't try to close that gap here — it needs an actual macOS
GitHub Actions runner, a separate, infrastructure-cost decision.

## README updates

- **Root `README.md`**: add `make e2e-smoke` and `make release-gate` to the
  "Working on mads" command block, one line each explaining what they gate
  (topology-through-fake-provider; cross-platform build + install/update
  smoke). Mention they're not part of `make check` and why (runtime).
- **`scripts/README.md`**: add a short section after "Smoke test" describing
  `release-gate.sh`, its `MADS_RELEASE_CANDIDATE_DIR` override, and that it
  now provides the darwin **build** coverage the existing "pending CI" note
  talks about (without closing the darwin **execution** gap — link the two
  notes together so a future reader doesn't think this item silently fixed
  that).

## Risks / things the executor should double-check as they go

- **Timing budget.** Router/Data Service startup + `mads run`'s stage
  sequence + the retro job's poll loop should comfortably fit in "minutes,"
  but actually time it locally before calling `make e2e-smoke` done — if the
  architect stage under/over-estimated, tighten `-stage-timeout` or the
  number of seeded tasks rather than the poll timeouts (those need slack for
  CI-runner variance, not local-dev optimism).
- **`git merge --no-ff` stage** needs the exact git config sequence
  `engine_test.go`'s `newHarness` uses (`merge.defaultToUpstream`,
  `branch.main.remote`/`branch.main.merge` pointing at `.`) — copy it
  precisely, it's fiddly and already debugged once.
- **`-tags` and `golangci-lint`**: `make lint`'s `golangci-lint run ./...`
  won't see the tagged packages at all (by design, see above) — meaning a
  lint issue inside `scripts/e2esmoke`/`scripts/releasegate` won't be caught
  by `make check`. `go test` itself still catches compile errors every time
  the gate runs, which is most of the value; don't over-invest in closing
  this fully (e.g. a second tagged lint invocation) unless it's cheap.
- **Don't let the `pipeline` binary gap turn into scope creep.** It's tempting
  to "just wire it up while you're in there" — resist; it's a real feature
  change to the Pipeline Engine's process contract (needs an HTTP trigger
  endpoint, a way to pass template/session into a spawned process, coordination
  with `stream_forward.go`'s existing SSE assumptions) that deserves its own
  design, not a drive-by inside a test-infra work item.
