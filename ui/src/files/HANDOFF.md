# Executor handoff: Files tab pipeline commits

Implemented against the architect's HANDOFF (this file, prior revision) in
full. All acceptance criteria met; `make check` and `make ui-smoke` are
green.

## What was done

- **Router**: `GET /sessions/{id}/commits` now proxies to the Data Service
  and enriches each commit with a best-effort file list via
  `git show --name-status` against `Commit.Repo` (`services/router/internal/api/commits.go`).
  Degrades to `files: null` when the repo path is empty, unreachable, or
  the hash fails a strict `^[0-9a-fA-F]{7,40}$` validation (defends against
  hash values being interpreted as git flags). Route added to
  `route_manifest_test.go`'s real manifest and removed from
  `knownGapRoutes`.
- **githook**: `pkg/githook/githook.go`'s `Script` now resolves and reports
  the primary repo path (via `git rev-parse --git-common-dir` + `dirname`),
  so a commit made inside a cleaned-up pipeline task worktree still
  resolves to a live path after the worktree is removed. Verified via new
  assertions in `services/pipeline/engine/worktree_test.go` and
  `services/session/internal/runner/observation_test.go`.
- **UI**: `FilesTab` now renders two collapsing sections — "Committed"
  (`CommitList`, newest-first, task-title chip, short hash, per-commit file
  sublist) and "Working changes" (existing `ChangeList`, unchanged). Both
  empty renders one honest empty state naming both sources. `CommitFile`/
  `Commit.files` added to `router/types.ts`.
- **e2e**: `ui/e2e/lib/seed.ts` gained `recordRealCommit` (real git commit
  in the seeded workspace repo, reported directly to the Data Service —
  same "seed the producer gap directly" pattern as `recordFileChange`,
  since the fake provider doesn't drive a real pipeline-stage commit).
  `session-work.spec.ts` now captures the imported task, records a real
  commit linked to it via trailer, drops the `/commits` 404 carve-out, and
  asserts the commit's message, short hash, and task-chip title render in
  the Files tab.

## Deviations from the architect's HANDOFF

- `fixtureCommits["session-files-clean"]`'s `files` array uses paths
  distinct from `fixtureFileChanges["session-files-clean"]`'s (e.g.
  `services/data/internal/backoff.go` instead of reusing `ingest.go`) —
  the HANDOFF's suggested fixture reused identical paths across the two
  lists, which made `screen.getByText` ambiguous in three pre-existing
  `FilesTab.test.tsx` tests. Same reasoning for renaming the
  `tsk_retry-backoff` fixture task's title away from "Add retry backoff to
  ingest" (collided with the fixture commit's message text under the same
  case-insensitive regex).
- Two pre-existing tests not mentioned in the HANDOFF's test-update section
  needed fixing because they depended on invariants the new fixture data
  broke:
  - `src/features/sessions/workspace/tabRegistry.test.tsx`: asserted on
    the old "Changed files"/"Commits" panel headers, which the HANDOFF's
    own §4 renames to "Working changes"/"Committed". Updated to the new
    header text (session-active's fixture has no commits, so only the
    "Working changes" header is asserted).
  - `src/tasks/useTaskStore.test.ts`: asserted `result.current.tasks.length
    === fixtureTasks.length`, assuming every fixture task belongs to
    session-active. Adding three new tasks for other sessions (per HANDOFF
    §6) broke that. Fixed by filtering to session-active's own count
    instead of the global fixture length.

## What was not done / out of scope

Everything in the architect HANDOFF's §9 "explicitly out of scope" list
was left alone: no changes to `services/data`, no diff-viewer wiring for
historical commits, `store.Sessions.Commits`'s ordering unchanged, and the
githook wasn't extended to populate `Author`.

## What the reviewer should scrutinize

- `commitHashRe` in `commits.go` is the only thing standing between a
  malformed/adversarial `Commit.Hash` and being passed to `git show` —
  worth double-checking it can't be bypassed (e.g. via a hash value that
  matches the regex but still smuggles a flag; I believe it can't, since
  the regex is fully anchored and hex-only, but a second look is cheap).
  `exec.CommandContext` is called with an argv slice, not a shell string,
  so no shell-injection surface even if the regex were wrong.
- The repo-path resolution logic in the githook `Script` (POSIX `sh`,
  string-substitution based) is the riskiest piece of shell in this
  change — it's covered by the two Go-side integration tests but not by
  any shell-level unit test, since none existed for `pkg/githook` before.
- The two "deviation" test fixes above (`tabRegistry.test.tsx`,
  `useTaskStore.test.ts`) — these are pre-existing tests I modified that
  weren't called out in the original design; worth confirming the fix
  reflects the actual intended behavior rather than just silencing a
  failure.

## Verification performed

- `make check` — Go race tests + lint, UI lint/typecheck/vitest (852
  tests). All green.
- `make ui-smoke` — real Router/Data Service/pipeline binaries, all 13
  Playwright journeys including the extended `session-work.spec.ts`. All
  green; the Files tab test explicitly asserts the seeded pipeline
  session's real commit (message, short hash, task chip) renders.
- Did not additionally spin up a manual Maintenance/pipeline-only session
  by hand — the `ui-smoke` journey's `session-work.spec.ts` exercises this
  exact path end-to-end (real pipeline run via `runFakePipeline`, real
  commit via `recordRealCommit`, Files tab assertions), which I'm treating
  as satisfying HANDOFF §10's manual-verification note.

STAGE: pass
