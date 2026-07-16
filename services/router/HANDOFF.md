# HANDOFF: Router — kill child process groups; default to built binaries

## What shipped

Implemented the architect's plan as designed (the original design doc that shaped this
task's HANDOFF is quoted in full in this task's conversation transcript, not separately
recoverable from git history — this branch has a single implementation commit, no
separate architect commit).

- **`services/router/internal/child/child.go`**: `Spawn` now sets `cmd.SysProcAttr =
  &syscall.SysProcAttr{Setpgid: true}` before starting the child, making it its own
  process group leader; anything it forks without calling `setpgid` itself (a `go run`
  wrapper's compiled binary, a `sh -c` chain) inherits that pgid. A new `killGroup(pid,
  sig)` helper signals `-pid` (the whole group) via `syscall.Kill`, tolerating `ESRCH`
  (group already gone) as success. All four kill/signal call sites — `Terminate`'s
  SIGTERM, `Terminate`'s post-grace SIGKILL, and `Spawn`'s two early-kill paths
  (`readyTimeout` and `ctx.Done()`) — now go through `killGroup` instead of
  `cmd.Process.Kill()`/`cmd.Process.Signal()`. Dropped the now-unused `os` import and the
  `os.ErrProcessDone` tolerance it supported (superseded by `killGroup`'s `ESRCH` check).
  `SessionPID`/`PipelinePID` and the tests that kill a child directly via
  `os.FindProcess(pid).Kill()` are untouched — they deliberately exercise the
  unsupervised single-process-death path, not `Terminate`.
- **`services/router/internal/child/child_test.go`**: added
  `TestTerminateKillsGrandchildProcessGroup` — writes a `sh` wrapper script that
  backgrounds `testChildBin` as a distinct grandchild pid (writing that pid to a file)
  and `wait`s, spawns it through `Spawn` (proving readiness discovery works through the
  wrapper's inherited stderr, not just direct spawns), polls the pidfile for the
  grandchild pid, confirms it's alive (`syscall.Kill(pid, 0)`), calls `Terminate`, then
  polls until the grandchild pid returns `ESRCH`. Verified this test actually exercises
  the fix by temporarily reverting `child.go` and confirming both this test and the
  existing `testleakcheck.Verify` backstop fail together (grandchild survives) — then
  restored the fix.
- **`services/router/main.go`**: added `defaultChildCmd(name, goRunFallback string)
  string` — resolves `os.Executable()`, checks for an executable sibling file named
  `name` next to it, and returns that path if present, else `goRunFallback`. Keyed off
  `os.Executable()` rather than cwd because the router's cwd is ambiguous across
  invocation styles (`cd services/router && go run .` vs. `go run ./services/router`
  from repo root give different cwds but the same resolved executable path).
  `-session-cmd`/`-pipeline-cmd` flag defaults now call `defaultChildCmd("session", "go
  run ./services/session")` / `defaultChildCmd("pipeline", "go run ./services/pipeline")`
  instead of hardcoding the `go run` string.
- **Root `Makefile`**: `build` target now also produces `bin/router`, `bin/session`,
  `bin/pipeline` alongside the existing `bin/mads`, so a built install's `bin/router`
  finds its siblings automatically and spawns the compiled binaries directly (no `go
  run` wrapper, no grandchild-orphaning class of bug at all in that path).
- **`services/router/README.md`**: replaced the "Flags:" paragraph's description of
  `-session-cmd`/`-pipeline-cmd` defaults with the sibling-binary-if-present/else-`go
  run` behavior, and added a "Critical paths" bullet documenting the process-group fix,
  why it doesn't regress the dev Ctrl-C flow (each spawned child now gets its own pgid
  separate from the router's, but `main.go`'s existing `signal.NotifyContext` +
  `sup.Shutdown` → `Terminate` path already reaps children on SIGINT/SIGTERM), and the
  `defaultChildCmd` sibling-resolution behavior.

## Tests

- `TestTerminateKillsGrandchildProcessGroup` (`internal/child`) — the acceptance-criteria
  test: real process tree via a wrapper script, terminated through `Terminate`, explicit
  poll-until-gone assertion on the grandchild pid specifically.
- All pre-existing `internal/child`, `internal/supervisor`, `internal/api` tests pass
  unchanged, including their `TestMain`s' `testleakcheck.Verify` backstop scans.
- Manually confirmed `make build` produces `bin/{mads,router,session,pipeline}` and that
  `./bin/router -help` resolves `-session-cmd`/`-pipeline-cmd` to the absolute sibling
  binary paths (not the `go run` fallback) — proving `defaultChildCmd` is live, not just
  unit-tested in isolation.

## Not done

Nothing scoped was skipped. The architect's plan's three pieces (process groups, the
grandchild test, flag defaults) are all implemented as designed.

## Verification

- `go build ./...` clean.
- `go test ./services/router/...` — all green.
- `go test -race -v -run TestTerminateKillsGrandchildProcessGroup ./services/router/internal/child/...` — green.
- Reverted `child.go` only and reran the new test: it failed with "grandchild pid ...
  still alive after Terminate", and `testleakcheck` also flagged the leaked
  `testchild` process — confirms the test is a real regression guard, not a false green.
  Restored the fix before proceeding.
- `make check` (lint, `go test -race ./...`, `npm run lint`, `npm run test`) — clean
  exit, 676 UI tests passed, no new warnings.
- `make build` — clean exit, `bin/router`/`bin/session`/`bin/pipeline` all present and
  executable; `./bin/router -help` confirms sibling-binary default resolution.

## Reviewer should scrutinize

- `killGroup`'s reliance on `cmd.Process.Pid` equaling the process group id: true
  whenever `Setpgid: true` and `Pgid` is left zero (the new leader's pgid becomes its own
  pid) — confirmed no other code path in `child.go` sets `SysProcAttr.Pgid` to anything
  else.
- The dev Ctrl-C behavior change called out in the README: before this change, a bare
  `go run .` for the router and its children shared one process group by terminal
  accident, so Ctrl-C reached everything; after this change each spawned child gets its
  own pgid, so Ctrl-C (SIGINT) only reaches the router process directly — the existing
  `signal.NotifyContext` + `sup.Shutdown(ctx)` path in `main.go` (unchanged by this task)
  is what now does the reaping instead, and it already covered exactly this case, so
  behavior should be equivalent in practice. Worth a manual `Ctrl-C` dev-loop check if
  the reviewer wants to be certain rather than take the code-path argument on faith.
- `defaultChildCmd`'s `info.Mode()&0111 != 0` executable check — required so a stray
  non-executable file literally named `session`/`pipeline` next to the router binary
  isn't silently treated as a spawn target; don't drop it as redundant.
