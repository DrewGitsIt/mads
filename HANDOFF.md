# Executor handoff: Task detail surfaces pipeline run state and blocker review

## What I did

Implemented exactly what the architect's handoff (`ui/src/tasks/DetailSheet/
HANDOFF.md`) specified, no design deviations.

**`useResumingRunFold`** (`ui/src/pipeline/state/usePipelineRun.ts`)
- Extracted the buffer/seed/flush resume-then-subscribe plumbing out of
  `usePipelineRun`'s internal `resumeAndSubscribe` into a standalone,
  exported hook parameterized on `pickRun`. `usePipelineRun` now calls it
  with `pickRun = (runs) => runs.find(r => r.outcome === "running")`
  (unchanged behavior) plus its existing `onEnvelope` → `refetchTasks()`
  wiring.
- `useGraphRunState.ts` (`ui/src/tasks/GraphView/`) now calls the same
  hook with `pickRun` = newest run by `run_seq` regardless of outcome, so
  it correctly folds an already-terminal run (the common case for a
  blocked task's detail sheet) instead of only picking up live events
  from mount time forward.

**`BlockedTaskFeedback`** (`ui/src/pipeline/Board/BlockedTaskFeedback.tsx`
+ `.module.css`)
- Extracted the per-task notes list + Retry/Reassign actions row out of
  `BlockedPanel.tsx` into its own component, taking `taskId`/`notes`/
  `client` props and owning its own `retrying`/`error` local state.
  `BlockedPanel.tsx` now renders it in place of the inlined markup,
  keeping its own `Card`/header/badge as-is.

**`TaskDetailSheet.tsx`**
- Added a "Pipeline run" section below the Status field: current stage +
  `StageTimeline` (same component the drill-in uses) whenever the task
  has folded run history, plus `BlockedTaskFeedback` whenever
  `draft.status === "blocked"` — gated independently of `hasRunHistory`
  per the handoff (a blocked task's notes/Retry come from the `Task`
  object directly, not the fold). Added matching `.runState` /
  `[data-blocked]` CSS to `TaskDetailSheet.module.css`.

## Tests

- `ui/src/tasks/DetailSheet/TaskDetailSheet.test.tsx`: 7 tests, pulling in
  `recordedTasks`/`recordedSessionId`/`recordedEnvelopes` and
  `FakePipelineClient` per the handoff's fixture guidance — covers
  `StageTimeline` rendering off the recorded run, the blocked task's
  blocker note + working Retry (asserts `client.retryTask` called with
  the right id), no "Pipeline run" section when `pipelineClient` is
  omitted.
- `ui/src/tasks/GraphView/useGraphRunState.test.ts`: added a test seeding
  a finished (`outcome: "incomplete"`) run and asserting the fold resolves
  on mount with no live SSE events — the resume-fix case the two existing
  tests don't cover.
- `ui/src/pipeline/Board/BlockedPanel.test.tsx` and
  `ui/src/pipeline/state/usePipelineRun.test.ts`: unmodified, pass as-is
  against the refactor.

### e2e journey

Extended `ui/e2e/session-work.spec.ts` with a new serial `describe` block:
seeds one task, drives it to `blocked` via `mads run -provider fake
-fake-scenario <path>` where the scenario fails the `code_review` stage
every time (mirroring `services/pipeline/engine/engine_test.go`'s
`TestDefaultTemplateReviewLoopBack`, simplified to one task/no dependency
chain), tolerating the CLI's expected non-zero exit on a blocked outcome.
The test then: opens the session, switches to the Tasks tab, clicks the
blocked task, asserts the blocker note text and a "Retry" button are
visible in the (Tasks-tab-opened) detail sheet, clicks Retry, and asserts
the sheet's Status field leaves `blocked` — relying on `useTaskStore`'s
existing live `task.status_changed` subscription for the refresh, no new
plumbing needed. `ui/e2e/lib/seed.ts`'s `runFakePipeline` gained an
optional `scenarioPath` param to support this.

## What I did not do (and why it's not needed)

- No changes to `NoteGroup`/`NOTE_GROUPS` or the generic notes editor —
  the handoff explicitly calls the blocker-note duplication between the
  new Pipeline run section and the existing generic "Blocker" note group
  intentional.
- Reassign stays permanently disabled in `BlockedTaskFeedback` (unchanged
  from `BlockedPanel`'s prior behavior) — `reassignTask` throws on every
  real/fake client today, per the handoff.
- No `usePipelineRunHistory`-style upper-bound filtering added to
  `useGraphRunState`'s `pickRun` — confirmed via
  `services/router/internal/api/pipelines.go` that at most one non-terminal
  run exists per session, and it's always the newest by `run_seq`, so
  picking the newest run unconditionally covers the in-flight case too.

## Incidental fix (out of scope, but blocked `make check`)

`services/data/internal/store/leases.go:271` (`Leases.ByHolder`) had a
bare `defer rows.Close()` tripping errcheck. Confirmed via `git stash`
that this predates this task (introduced in `099465c` "Engine
reap-on-launch: reclaim leases whose holder is provably dead"), unrelated
to anything touched here. Fixed to `defer func() { _ = rows.Close() }()`,
matching the dominant convention already used elsewhere in the same
package (`metrics.go`, `log.go`, `tasks.go`, `links.go`), since a clean
`make check` is a hard acceptance criterion for this task.

## For the reviewer to scrutinize

- `useGraphRunState`'s `pickRun` (newest run by `run_seq`, any outcome) is
  a slightly different selection strategy than `usePipelineRun`'s
  (in-flight only) — worth double-checking against real multi-run
  sessions that a task blocked in an older, superseded run correctly
  falls back to blocked-review-only (no timeline), as the architect's
  "Risks" section anticipates, rather than showing a stale/wrong timeline.
- The blocker note is intentionally rendered twice in a blocked task's
  detail sheet (once in the new Pipeline run section, once in the
  existing generic notes list) — this is called out as deliberate in the
  architect handoff, not a bug.

## Full check runs

- `make check` (golangci-lint + `go test -race ./...` +
  `npm --prefix ui run lint` + `npm --prefix ui run test`) — clean, 1000
  UI tests pass.
- `make ui-smoke` (fresh `ui/dist` build with `VITE_ROUTER_URL=""` +
  full Playwright e2e suite) — 23/23 pass, including the new blocked-task
  journey spec.
