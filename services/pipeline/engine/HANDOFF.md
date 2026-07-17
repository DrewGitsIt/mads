# Engine: never shell-interpolate task-derived strings — executor notes

## What I did

Implemented the fix exactly as scoped (the architect handoff that was here
described the same root cause and fix; I followed it).

- `services/pipeline/engine/worktree.go`, `(*worktrees).merge`: removed the
  `sh -c` composition that Go-`%q`-quoted the task title into a shell
  command string. Replaced with direct argv: `strings.Fields(mergeCmd)[1:]`
  (the trusted template prefix, e.g. `git merge --no-ff` minus the leading
  `git`) plus `branch`, `-m subject`, `-m "Task: <id>"` as separate
  `exec.Command` args via the existing `w.git` helper. `subject` and the
  trailer are task data and now never touch a shell.
- `services/pipeline/engine/worktree.go`, the `git` helper: labeled the
  combined-output error with `stderr:` so every git-backed stage failure in
  this file (merge, rebase-before-merge, commit-stray, sweep-handoff,
  conflict diagnostics) includes the underlying detail on a labeled line
  instead of a bare trailing blob.
- `services/pipeline/engine/worktree_test.go`: added
  `TestHostileTaskTitleMergesCleanly`, table-driven over five hostile
  titles (backtick, `$(...)`, mixed quotes, embedded newline, leading
  dash). Each seeds a task with that title, a fake rule that writes a
  `HANDOFF.md` plus a work file, runs the full isolated pipeline (worktree
  create → branch → stray-commit → HANDOFF sweep → merge), and asserts the
  task lands in `Done`, the work file reaches trunk, `HANDOFF.md` doesn't,
  and trunk is clean.

## What I did not do

- Did not touch `engine.go`'s `runCommand` (`sh -c` on `st.Command`) —
  that's template-authored input (`make test`, `git merge --no-ff`), not
  task data, out of scope per the task.
- Did not touch `pkg/githook/githook.go` — its shell script is static, not
  built from task data by Go code; confirmed safe, no change needed.
- Did not re-audit files outside `services/pipeline/engine` — scope was
  this package.

## Verification

- `grep -rn '"sh"' services/pipeline/engine/*.go` (excluding `_test.go`)
  shows exactly one hit: `engine.go`'s `runCommand` on `st.Command`.
- `go test -race ./services/pipeline/engine/...` — green, including the new
  hostile-title cases.
- `make check` from repo root — green (Go + UI).

## Reviewer should scrutinize

- `strings.Fields(mergeCmd)[1:]` assumes `mergeCmd` always starts with a
  `git` token and has at least 2 fields. This holds today because `merge`
  is only called from `engine.go` when `isMergeStage` has already verified
  `st.Command` starts with `"git merge"` — but if that gate ever loosens,
  this slice would panic on a short/malformed template command. Worth a
  second look if `isMergeStage`'s check changes in the future.
- The embedded-newline test case relies on the pipeline tolerating a
  multiline task title end-to-end (worktree branch name is `mads/<id>`, not
  derived from the title, so this shouldn't matter, but flagging it since
  it's the least obviously-safe of the five cases).
