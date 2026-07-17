# Pipeline tab: replay run history from the event log — executor handoff

Implemented per the architect's HANDOFF plan (this file replaces it). All
work items in the original §6 file list are done; `make check` and
`make ui-smoke` are both green (869/869 vitest, 14/14 e2e).

## What was built

- Router `GET /events` now dispatches on `Accept` like the Data Service
  already does: `text/event-stream` → existing SSE path (untouched
  behavior); anything else → new batch JSON path proxying
  `dataclient.Client.Events`.
- `RouterClient.metricsPipelineRuns` takes an optional `sessionId`; new
  `RouterClient.getEvents(filter)` for the batch path.
- `usePipelineRun.ts` now exports `FOLD_EVENT_TYPES`, `RunFoldState`,
  `initialRunFoldState`, `foldReducer` — no behavior change to the hook
  itself.
- New `usePipelineRunHistory(sessionId, client, active)` hook: lists runs
  via `metricsPipelineRuns`, default-selects the newest terminal
  (non-`"running"`) run, fetches that run's events via
  `getRunEvents(sessionId, runSeq - 1)`, folds them with the shared
  `foldReducer`, and bounds an older selected run's fold by the next-newer
  run's `run_seq` so its columns/task state can't leak forward.
- New `RunPicker` component: a `<select aria-label="Pipeline run">` over
  runs newest-first with a status line.
- `PipelineTab.tsx` composes `usePipelineRun` (live) and
  `usePipelineRunHistory` (historical, `active = !liveStarted`). A live
  run, once started this mount, permanently preempts the historical view
  (including its own finished state) for the rest of the mount. Empty
  state ("No pipeline runs yet") only when there's neither a live run nor
  any historical run.
- `pipelineClient.ts`/`FakePipelineClient`: new `listRuns`/`getRunEvents`.

## Deviation from the architect's plan, and how it was resolved

§1 of the architect's plan assumed the live SSE connection (`usePipelineRun`'s
`subscribeToEvents`, no `since` param) never sees events that happened
before the tab mounts, and §5 accordingly asked the e2e test to assert the
`RunPicker` is visible after `page.goto`.

That assumption is false against the real backend: `services/data/internal
/api/events.go`'s `streamEvents` doc comment says outright "replay from the
cursor, then push each new commit as it lands" — with no `since` query
param the cursor is `0`, and the query has no `Limit`, so a fresh
`EventSource` connection replays the *entire* matching history before
tailing new events, confirmed by reading `handleStreamEvents` (Router),
`streamEvents` (Data Service), and `store.Log.Query`/`Filter.Limit` ("0
means no cap"). Confirmed empirically too: a fresh `make ui-smoke` run
against this fixture's small backlog folds the finished run via the live
path before the historical fetch even resolves, so the RunPicker never
mounts under normal conditions here.

A first pass at this task left the e2e test not exercising the
RunPicker/historical-fetch path at all (it asserted the live-fold outcome
text instead, with a note asking the reviewer whether that was
acceptable) — code review correctly failed that: the acceptance criteria
explicitly requires a journey assertion that the picker switches runs.
Fixed by forcing the historical path deterministically instead of relying
on backlog size: `session-work.spec.ts`'s new "pipeline tab run picker
replays historical runs" test installs a `page.route("**/events*", …)`
handler that inspects each request's `Accept` header (mirroring
`events.go`'s own dispatch) and never resolves the ones asking for
`text/event-stream` — the SSE connection just hangs, so `usePipelineRun`'s
phase never leaves `"idle"` and `PipelineTab` renders the historical path
for the whole test, no race. The plain JSON `getEvents` fetches
(`Accept: */*`, used by `usePipelineRunHistory`) pass through untouched.

The test seeds a *second* pipeline run (a fresh, dependency-free task, run
after the original one) so there are two real runs to switch between, then:
- asserts the `RunPicker` combobox is visible with 2 options, newest
  selected by default (proves the historical path rendered, not the live
  one — the live path never shows a picker at all);
- selects the older run and asserts, via `page.waitForResponse`, that this
  fires a real `GET /events?...since=<olderRun-1>...` request (not just a
  client-side label flip) whose response is 2xx;
- asserts the RunPicker's own status line (built from `GET
  /metrics/pipelines`, per-run `tasks_done`/`tasks_blocked`/timestamps) is
  visibly different afterwards.

Note on why the assertion stops at the RunPicker's own metadata rather than
a StageBoard/BlockedPanel difference: `PipelineTab.tsx` always sources
`tasks`/blocked-status from the live `usePipelineRun`'s global task list
(HANDOFF.md's own composition above), never from the selected historical
run — so a "done" task lands in `StageBoard`'s Done bucket the same way
regardless of which run's fold is selected. The genuinely fold-driven
`columns`/`taskRunState` (from `getRunEvents`) only produces a visibly
different board when a task is *not yet done* at the time of the mount
(mid-run, not this fixture's fully-completed historical runs) — that
per-fold behavior (including cross-run contamination) is what
`usePipelineRunHistory.test.ts` and `PipelineTab.test.tsx`'s unit tests
already cover directly against `foldReducer`. The e2e addition's job is
proving the *transport* (real backend fetch, real Accept-based dispatch,
real picker interaction) works end-to-end, which the network-response and
RunPicker-metadata assertions above do.

## Not done / explicitly out of scope (per architect's plan)

- No server-side runs table — the event log stays the source of truth.
- No second `runFakePipeline` e2e case for multi-run picker switching
  (unit-level coverage only, per §5).
- `RunHeader.tsx`, `StageBoard.tsx`, `BlockedPanel.tsx`, `TaskDrillIn.tsx`
  and their tests are untouched.

## Test coverage added

- `usePipelineRunHistory.test.ts`, `RunPicker.test.tsx` (new)
- `PipelineTab.test.tsx`: historical-only render (no live SSE playback),
  run-switching via the picker
- `client.test.ts` (Router): `getEvents`, scoped `metricsPipelineRuns`
- `pipelineClient.test.ts`: `listRuns`/`getRunEvents`
- Go: `TestGetEventsBatchReturnsJSON` plus existing SSE/manifest tests
  confirmed unmodified
- e2e: `session-work.spec.ts` seeds a second pipeline run in `beforeAll`
  and adds "pipeline tab run picker replays historical runs" (see
  deviation note above), which stalls the live SSE via Playwright route
  interception and drives the RunPicker against the real backend. The
  original "pipeline tab shows the completed run" test is unchanged — it
  still documents and asserts the live-SSE-wins-the-race case.
