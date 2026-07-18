# Executor handoff: Pipeline drill-in — live agent output and a real panel

Task: tsk_e04512caebe39037
Stage: executor → reviewer

## What I did

Implemented the architect's design (§1-§6 of the original doc) essentially
as specified:

- **Providers**: added optional `OnChunk func(text string)` to
  `OneShotRequest` (`providers/provider/provider.go`), a new
  `BehaviorOneShotStream` matrix row (`providers/provider/matrix.go`), and
  declared a stance in every `Matrix()` literal (`claudecode`: Native,
  `fake`: Native, `kirocli`: Unsupported, plus the conformance/registry
  test stubs). `providers/fake` now streams scripted chunks through
  `OneShot` with the scenario's per-chunk delay instead of joining and
  sleeping once. `providers/claudecode` switches to `--output-format
  stream-json --verbose` when `OnChunk != nil` and parses incremental text
  deltas, falling back to the existing blocking `--output-format json`
  path when it's nil. `providers/conformance` gained an `oneShotStream()`
  probe wired into `Check()`.
- **Engine**: `Config.MadsHome` (empty ⇒ no-op, matching `recordUsage`'s
  best-effort philosophy). `e.event()` now returns the posted envelope's
  `Seq`, captured once as `e.runSeq` in `run()`. `runAgent` writes each
  stage attempt's output to
  `$MADS_HOME/runs/<session_id>/<run_seq>/<task_id>/<stage>-attempt-<n>.log`
  via a new `services/pipeline/engine/outputlog.go`, falling back to
  writing `res.Text` as a single chunk when the provider produced no
  incremental output. `cmd/mads/run.go` passes `MadsHome:
  defaultMadsHome()`.
- **Router**: new `GET /sessions/{id}/pipeline/tasks/{taskId}/output` SSE
  route (`services/router/internal/api/pipeline_output.go`), added
  `RunsDir` to `Options`/`server` and wired from `main.go`. Resolves the
  session's latest run dir, tails the task's most-recently-modified
  `*.log` file, re-globs each poll tick to follow stage transitions, and
  emits a terminal `event: end` frame once the file stops growing and the
  task's status is terminal.
- **UI client**: `pipelineClient.streamTaskOutput(sessionId, taskId)`
  (dropped the dead `runId` param per §0.4) opens a real `EventSource`
  against that route, closing on the `end` event. Removed
  `PipelineTab.tsx`'s now-dead `runId` state and `onRunStarted` wiring.
- **Panel**: `TaskDrillIn`'s default height is now `~40%` of the viewport
  (computed once per mount, clamped), persisted across reload via
  `ui/src/pipeline/DrillIn/drillInHeight.ts` (`mads.pipeline.drillInHeight`
  in `localStorage`), saved on resize-end (not mid-drag) through a new
  `ResizablePanel` `onResizeEnd` prop.
- **Journey test**: `ui/e2e/pipeline-drillin.spec.ts` — spawns
  `cmd/mads run` in the background against a scripted fake scenario with
  deliberate multi-chunk delays, opens the Pipeline tab, selects the
  running task, and asserts streamed chunks appear in the drill-in
  terminal; a second test drags the resize handle and confirms the height
  survives a reload.

No new Data Service event type was added for output chunks, per the
acceptance criteria.

## Collateral fixes (outside the stated scope, but blocking `make ui-smoke`)

**1. Render-loop `EventSource` leak in `PipelineTab.tsx`.** The original
call site built a fresh `PipelineOutputSource` (and therefore a fresh
`EventSource`) directly in JSX on every render — since `PipelineTab`
re-renders on every live-polling tick, this opened a new connection
continuously without ever closing the old one. Fixed by hoisting the
source into a `useMemo` keyed on `selectedTask?.id` (not the whole task
object, which changes identity every poll — the `useMemo` deps
intentionally use `.id`, with an `eslint-disable-next-line
react-hooks/exhaustive-deps` and a comment explaining why) plus a
`useEffect` that closes the previous source on task switch/unmount.

**2. Browser per-origin socket-cap exhaustion, hit only once #1 was fixed
and the drill-in's own stream became a genuine 7th connection request.**
Root-caused (via a temporary `EventSource.readyState` instrumentation
probe, since removed) to six *already-open, permanent* SSE connections
per session workspace: the rail's session-list, task-counts/palette, and
link-events subscriptions; `SessionWorkspace`'s own session-scoped
subscription; `ConversationTab`'s raw `/output` stream; and
`PipelineTab`'s own `usePipelineRun` session-scoped subscription — exactly
saturating Chromium's 6-per-origin HTTP/1.1 cap. The drill-in's own
`/output` `EventSource` would then permanently queue (`readyState` stuck
at `0`), never firing `open`/`message`, regardless of how correct the
server/client implementation was.

Fixed by consolidating `usePipelineRun`'s session-scoped subscription into
the one `SessionWorkspace` already holds open, the same pattern already
established for `MergeBackFlow` (see `WORKSPACE_SESSION_EVENT_TYPES`'s doc
comment) and documented as intentional by `usePipelineRunning.ts`'s
`RUN_EVENT_TYPES` comment:

- Widened `WORKSPACE_SESSION_EVENT_TYPES`
  (`ui/src/router/sessionEventTypes.ts`) to a superset covering both
  `SessionWorkspace`'s and `usePipelineRun`'s needed event types, and
  switched `usePipelineRun` to subscribe with that same literal array
  (required for `router/events.ts`'s URL-keyed pool to actually collapse
  the two into one connection instead of two).
- Since `SessionWorkspace`'s dispatcher previously used an implicit
  `else { refetchSession(); }` catch-all, widening the array would have
  made it spuriously refetch the session on every `task.*`/`stage.*`/
  `file.changed`/`note.created` event. Introduced
  `SESSION_SHELL_REFETCH_EVENT_TYPES` (an explicit subset meaning "the
  Session object may have changed") and switched the dispatcher to check
  membership against it instead of an else-catch-all.

This frees one connection (6 open → 5), letting the drill-in's own stream
fit as the 6th instead of queuing indefinitely.

## What I did not do

- **Did not fix a second, currently-harmless bug found during root-cause
  investigation**: `useSessionLinks.ts` is called independently by both
  `ConversationTab.tsx` and `SessionsSurface.tsx`, each opening its own
  `subscribeToEvents({types: LINK_EVENT_TYPES})` call with a momentarily
  identical URL — a pool-key race that should collapse into one
  connection but currently produces two. Confirmed (via the same
  instrumentation) that the first instance self-closes quickly and isn't
  a live contributor to the socket-cap issue at the point this test
  fails, so it wasn't required to fix the acceptance criteria. Worth
  fixing separately.
- **Did not touch `ConversationTab`'s always-open raw `/output` stream**
  or `useTaskStore`'s separate `streamTaskEvents` connection
  (`ui/src/tasks/useTaskStore.ts`) — the latter is only reachable from the
  Tasks tab, which this journey never visits, so it's outside this test's
  ambient connection set.
- **Did not fix `services/pipeline/main.go`** (still a dead skeleton,
  §0.2 of the original design) — real, separate work item, unrelated to
  this task's design.

## What to scrutinize

- **`SessionWorkspace.tsx`'s dispatcher logic change** (implicit
  `else`-catch-all → explicit `SESSION_SHELL_REFETCH_EVENT_TYPES`
  membership check) has no direct test coverage for the
  refetch-suppression behavior itself — existing tests assert on
  `pipeline.started`/run-dot state and use substring URL matching, so they
  don't distinguish "refetches" from "doesn't refetch" for the newly
  shared event types. Correctness here rests on careful reading, not a
  new assertion.
- **`WORKSPACE_SESSION_EVENT_TYPES` is a widely shared constant** (four
  consumers now: `SessionWorkspace`, `ConversationTab`, `MergeBackFlow`,
  `usePipelineRun`) with an explicit doc-comment contract that all callers
  must pass the exact same array for the pool to collapse connections.
  Any future caller that recombines a subset instead of importing this
  array directly will silently reopen a duplicate connection.
- **The socket-cap fix is architectural, not local** — it changes which
  events flow through which subscription, in a file several unrelated
  features depend on. Worth a careful diff read of
  `ui/src/router/sessionEventTypes.ts` and `SessionWorkspace.tsx` even
  though tests are green.

## Checks

- `make check`: passes — golangci-lint 0 issues, `go test -race ./...` all
  packages ok, ESLint 0 problems, 966/966 UI unit tests (195 files).
- `make ui-smoke`: passes — 18/18 e2e tests, including both new
  `pipeline-drillin.spec.ts` tests (live streaming output, resize
  persistence across reload).
