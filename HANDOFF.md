# HANDOFF: Engine self-salvage (gate-failure auto-triage + backup-on-block)

Task: tsk_6e67ab6bc89361b6

## What I did

**A) Gate-failure auto-triage** (`services/pipeline/engine/triage.go`, new):
- `parseFailingGoPackages` extracts distinct packages from `go test`'s `FAIL\t<pkg>\t...`
  lines, first-seen order, nil when not package-attributable.
- `triageGateFailure` reruns the failing portion in the same worktree, serially:
  package-attributable failures rerun under `-race -count=1` up to 3 times (stopping
  at first green); otherwise the gate command reruns once. Returns a `gateTriage` with
  classification (`flake`/`deterministic`), packages, and per-run evidence.
- `gateTriage.evidence` renders the block-note appendix: classification, trunk head,
  failing packages (or "none identified"), and each isolation run's pass/fail + command.
- `landAttempt` (`engine.go`) now loops: rebase → build → sweep → gate, and on gate
  failure calls `triageGateFailure`. Isolation-green retries the whole land attempt
  (rebase + full gate) up to `maxLandRetries = 2`, emitting `land.retried` each time.
  Isolation-red (or retries exhausted) blocks as before, with the triage evidence
  appended after the existing suspect-set message via `gateFailureErr`.
- New event `contracts.LandRetried` ("land.retried") added to `pkg/contracts/event.go`
  and the `EventTypes` registry, payload: branch, fork_sha, trunk_head, stage, attempt,
  classification, packages, isolation_runs.
- Rebase-conflict path is untouched: `rebaseBeforeMerge` errors still `break` out of
  the loop before reaching gate/triage logic, and the `errors.As(err, &ce)` handling
  after the loop is identical to before.

**B) Backup-on-block, cleanup-on-done** (`services/pipeline/engine/worktree.go`):
- `worktrees.backupBranch(ctx, taskID)` force-updates `backup/<taskID>` to
  `mads/<taskID>`'s current head, if that branch exists; returns whether it did.
- `worktrees.deleteBackupBranch(ctx, taskID)` best-effort deletes `backup/<taskID>`.
- `engine.block()` calls `backupBranch` before recording the block and, when a branch
  existed, appends a note line naming the backup branch.
- `runTask`'s done path calls `deleteBackupBranch` after `setStatus(..., StatusDone)`
  succeeds.

**Tests** (`services/pipeline/engine/worktree_test.go`, 5 new, all passing):
- `TestGateFlakeAutoRetriesAndLands` — gate fails once then passes; lands with no
  intervention; asserts `land.retried` payload (classification=flake, attempt=1,
  isolation_runs>0).
- `TestGateDeterministicFailureBlocksWithEvidence` — `exit 1` gate; blocks with the
  suspect-set wording plus triage evidence (classification=deterministic, "failing
  packages: none identified", exactly 1 isolation rerun); zero `land.retried` events.
- `TestGateFlakeRetryBoundedThenBlocks` — odd/even counter-script gate that looks
  flaky every round; asserts exactly 2 `land.retried` events (attempts 1, 2) then
  blocks — proves the bound, not unbounded retry.
- `TestBlockBacksUpWorktreeBranch` — deterministic-gate block; `backup/<id>` matches
  `mads/<id>` head; note names the backup branch.
- `TestDoneDeletesStaleBackupBranch` — pre-creates `backup/<id>`, runs a normal
  successful task, asserts the backup branch is gone after done.

**README** (`services/pipeline/README.md`) — added two prose paragraphs (gate-failure
auto-triage rationale; backup-on-block/cleanup-on-done), one Conventions bullet, and
two Critical-paths bullets naming the new tests.

## What I did not do

- No changes to the rebase-conflict / `ConflictError` path — confirmed byte-for-byte
  behavior preserved (existing conflict-attribution tests pass unmodified).
- No `merge_resolve` stage work — explicitly out of scope per the task.
- Did not add a standalone unit test for `parseFailingGoPackages` — coverage comes
  via the integration tests exercising both the package-attributable and generic
  triage paths; the regex is small enough that I judged it not worth a separate test.
- Did not touch anything in `services/router` or `services/session` — the two
  `make check` lint failures there (`supervisor_test.go` errcheck,
  `api_test.go` staticcheck) pre-exist on `main` (confirmed via `git stash` +
  targeted lint run before this change) and are unrelated to this task's scope.

## What to scrutinize

- **Retry-bound interaction with `landAttempt`'s loop**: the loop retries the *whole*
  land attempt (including rebase) on a flake classification, not just the gate. If
  trunk moved between retries, the retry rebases again — intentional (spec: "retry
  the whole land attempt: rebase + full gate"), but worth confirming that's the
  desired semantics vs. only re-running the gate in place.
  (See `services/pipeline/engine/engine.go:landAttempt`, the `for attempt := 0; err == nil; attempt++` loop.)
- **`triageGateFailure`'s generic (non-package) path** runs the *entire* gate command
  again, not a narrowed slice of it — for a `make test`-style gate this reruns
  everything, which is slower than package-scoped isolation but was the simplest
  correct interpretation of "rerun the failed portion of the gate command once" when
  the output isn't package-attributable (e.g. UI suite, lint). Worth confirming this
  matches intent rather than, say, wanting output-driven sub-selection for other
  command shapes.
- **`go vet ./...`**, `go build ./...`, `go test -race ./...` (full repo) and
  `bin/golangci-lint run ./pkg/contracts/... ./services/pipeline/...` all pass. Full
  `make check` fails only on the two pre-existing, unrelated lint issues noted above —
  did not attempt to fix those since they're out of this task's scope and touching
  them wasn't authorized.
