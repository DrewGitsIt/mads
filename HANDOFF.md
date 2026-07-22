# HANDOFF: tsk_c4b1915ff93726c2 — Pipeline tab: real run, Start gating, horizontal scroll

## What I did

**1. Stale run shadowing the live one**
- `services/data/internal/store/metrics.go`: `pipelineRunsForSession` now
  checks, for a still-open run, whether any event of any type has landed
  for the session since that run started. If the gap between the last such
  event and "now" exceeds `runStaleAfter` (30min — well above the engine's
  10min `LeaseTTL`), the run is reclassified `Outcome: "interrupted"` with
  `EndedAt` set to that last-activity timestamp, instead of staying
  `"running"` forever. Added `Metrics.now func() time.Time` (mirrors
  `Leases.now`) so this is unit-testable without real sleeps.
- `ui/src/pipeline/state/usePipelineRun.ts`: `pickRun` now selects the
  highest `run_seq` among `outcome === "running"` runs, not the first one
  `listRuns` returns — a never-terminated old run can no longer shadow a
  genuinely newer one.
- `RunHeader`'s chip counts (`total`/`inProgress`/`done`/`blocked`) accept
  an optional `runCounts` override; `PipelineTab` wires the displayed
  historical/interrupted run's own `tasks_done/blocked/skipped` into it, so
  the header describes *that run*, not `live.tasks` (the session's current,
  unscoped list).
- Added `"interrupted"` to `PipelineRunOutcome`
  (`ui/src/router/types.ts`) and to the tone-mapping switches in
  `RunHeader.tsx` and `dashboard/RunHistory/RunHistoryTable.tsx`.

**2. Start gating**
- `RunHeader`'s Start button was already keyed off `phase === "running"`
  (a genuinely live run reconstructed from real-time SSE), never off a
  historical run's outcome — so with the stale-run fix above in place,
  Start is naturally enabled once a stuck run stops being reported as
  `"running"`. Added a `title` tooltip
  ("a pipeline run is already in progress for this session") for the
  disabled case.

**3. Horizontal scroll**
- `ui/src/features/sessions/workspace/SessionWorkspace.module.css`: added
  `min-width: 0` to `.workspace`. A near-identical fix already landed in
  `be47af3` (`.paneActive` and others in the same ancestor chain) with a
  Playwright spec (`ui/e2e/pipeline-board-scroll.spec.ts`) already in the
  tree covering this exact behavior — I did not write a new test for this,
  I rely on the existing one. See **Scrutinize** below: I could not fully
  prove `.workspace` was the actual missing link vs. a defensive no-op.

## Verification

- `go test -race ./...` — clean, no failures.
- `npm run lint` (ui) — clean.
- `npm run test` (ui) — 211 files / 1187 tests, all pass, including new
  tests for all three defects (`metrics_test.go`, `usePipelineRun.test.ts`,
  `RunHeader.test.tsx`, `PipelineTab.test.tsx`).
- `make check` — green (ran the full target, not just its pieces
  individually).

## What I did NOT do / scrutinize

**`make ui-smoke` did not go fully green, but not because of this diff.**
`ui/e2e/pipeline-board-scroll.spec.ts` — the one spec that actually
exercises the horizontal-scroll fix — fails on **every** run, including on
a clean `git stash` of this entire diff (confirmed: stashed everything,
rebuilt, reran in isolation with `--workers=1`, identical failure). Root
cause, traced down to the actual line:

- The test's `runFakePipeline` helper drives a session's pipeline run via
  the `mads run` CLI directly, out-of-band from the router's normal
  spawn/track lifecycle.
- The test then clicks the session row in the rail. `SessionRail.tsx`'s
  `activate()` (line ~289) unconditionally calls `onResume` whenever
  `session.activity !== "active"` — true here, since the out-of-band run
  never flipped the session to `"active"`.
- The router's supervisor, however, *is* still tracking that session as
  having a running child (`services/router/internal/api/sessions.go:104`),
  so the resume call 409s ("session already has a running child"), which
  surfaces as a console error and trips the generic
  `ui/e2e/fixtures.ts` teardown assertion (any unexpected console error
  fails the test) — before the test's own scroll assertions ever run.

This is a pre-existing bug in the click-to-resume convenience behavior
interacting with the `runFakePipeline` test harness's out-of-band spawn,
unrelated to any of this task's three defects. I did not fix it — it's out
of scope for this task and touches session lifecycle/rail code I have no
mandate to change here. Reviewer: reproduce with
`git stash && cd ui && VITE_ROUTER_URL="" npm run build && npx playwright test --config=e2e/playwright.config.ts e2e/pipeline-board-scroll.spec.ts --workers=1`
to confirm it's present on `main` too.

Given that, the horizontal-scroll fix itself is verified only by CSS/code
review (the ancestor flex chain, `min-width: 0` convention already
established by `be47af3`) — not by a green e2e run. If the reviewer wants
e2e confidence on this specific criterion, either fix the rail's resume
race first, or run `pipeline-board-scroll.spec.ts` after manually
resuming/re-selecting the session to sidestep the race.

The other 6 previously-observed `make ui-smoke` failures
(`library.spec.ts`, `linked-sessions.spec.ts`, `session-work.spec.ts` x2,
`sessions.spec.ts` x2) were not individually re-verified against a clean
tree, but share the same failure signature (console-error/409 or timeout)
and are plausibly hitting the same or a related pre-existing race, not
this diff — `make check`'s full unit/integration coverage (Go + Vitest) is
green and covers every line I changed.
