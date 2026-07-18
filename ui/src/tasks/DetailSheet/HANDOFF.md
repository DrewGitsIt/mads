# Task detail surfaces pipeline run state and blocker review

## Problem

Drew: the blocked-task review UI (blocker note, stage output, Retry/
Reassign) was only reachable from the Pipeline tab. Clicking a task in the
Tasks tab (list or graph) opens `TaskDetailSheet`, which shows nothing
about pipeline run state — even for a task the Tasks tab already renders
as `blocked` (Task graph 3's status encoding).

## Design

Two additions to `TaskDetailSheet.tsx`, both reusing existing pipeline
components rather than forking rendering:

1. A "Pipeline run" section, shown whenever the task has folded pipeline
   history: current stage + `StageTimeline` (`ui/src/pipeline/DrillIn/
   StageTimeline.tsx`), same component the drill-in uses. Not forked.
2. For `status === "blocked"` tasks specifically, the same blocker-note +
   Retry/Reassign surface `BlockedPanel` (Pipeline tab) shows — extracted
   into its own component so both call sites share one implementation.

### Fold-data gap you must close first

`TaskDetailSheet` already calls `useGraphRunState(draft.session_id,
pipelineClient)` (for `DependencyPicker`'s sibling-status display) —
`ui/src/tasks/GraphView/useGraphRunState.ts`. This is "the same event-log
fold the Pipeline tab uses" (`foldReducer`/`FOLD_EVENT_TYPES` from
`usePipelineRun.ts`) — reuse it, don't add a third fold.

**But `useGraphRunState` currently does *not* resume correctly.** It just
calls `subscribeToEvents({sessionId, types: FOLD_EVENT_TYPES}, dispatch)`
with no history catch-up. `usePipelineRun.ts` got a resume fix in
tsk_c6d8d6c147a7cddc (buffer live envelopes, `listRuns`/`getRunEvents`
catch-up for an in-flight run, seed the fold, then flush the buffer) —
`useGraphRunState` was never updated to match (it was written earlier, in
the Task graph 3 work, and untouched by that fix; confirm with `git log
--follow -- ui/src/tasks/GraphView/useGraphRunState.ts`).

This gap is not hypothetical for this task specifically: a blocked task's
pipeline run has almost always already reached a **terminal** outcome by
the time someone opens its detail sheet, not a still-running one.
`services/pipeline/engine/engine.go`'s `Run` loop keeps going once a task
blocks (parks it, keeps processing the rest) and emits `pipeline.completed`
with `outcome: "incomplete"` (not `"running"`) once the whole run goes
quiescent — see engine.go's `outcome := "completed"` / `if
len(summary.Blocked) > 0 ... outcome = "incomplete"` block. So
`usePipelineRun`'s existing resume logic (`runs.find(r => r.outcome ===
"running")`) would find nothing for the common case of "reviewing a
blocked task after its run already finished."

Also: `ui/src/router/events.ts` uses **one shared `EventSource` per
window**, keyed by a module-global `lastDeliveredSeq`. The `since=0` full
backlog replay happens once, for whichever subscriber causes the first
connection to open (usually mounted at app boot). A hook that subscribes
later (e.g. `useGraphRunState` mounting when a task sheet opens, well
after other tabs have already advanced `lastDeliveredSeq`) will **not**
see historical events — it only gets what's dispatched from then on. So
REST catch-up is genuinely required here, not just for parity.

**Fix**: extract the resume-then-subscribe plumbing out of
`usePipelineRun` into a shared, parameterized hook both callers use —
don't duplicate the buffer/seed/flush dance, and don't change
`usePipelineRun`'s own external behavior (its existing regression test
"with no in-flight run, resuming resolves promptly and phase stays idle
until a live pipeline.started arrives" must keep passing unmodified).

In `ui/src/pipeline/state/usePipelineRun.ts`, add:

```ts
export function useResumingRunFold(
  sessionId: string,
  client: PipelineClient | undefined,
  pickRun: (runs: PipelineRun[]) => PipelineRun | undefined,
  onEnvelope?: (env: Envelope) => void,
): { state: RunFoldState; resuming: boolean }
```

Same internals as today's `resumeAndSubscribe` (buffer live envelopes
behind a flag, `listRuns` → `pickRun(runs)` → if found, `getRunEvents(id,
target.run_seq - 1)`, fold via `events.reduce(foldReducer,
initialRunFoldState)`, `dispatch({kind:"seed", state})`, track
`caughtUpSeq`, flip `live = true`, flush buffered envelopes past
`caughtUpSeq`), just parameterized on `pickRun` instead of hardcoding
"find the in-flight run". Capture `client`/`pickRun`/`onEnvelope` in refs
(same idiom `useGraphRunState` already uses for `client`) so the effect's
dependency array stays `[sessionId, !!client]`-shaped and doesn't
resubscribe every render just because the caller passed a fresh inline
closure.

- `usePipelineRun` calls it with `pickRun = (runs) => runs.find(r =>
  r.outcome === "running")` (today's exact behavior, unchanged) and
  `onEnvelope` doing the existing `METADATA_EVENT_TYPES` → `refetchTasks()`
  dispatch.
- `useGraphRunState` calls it with `pickRun = (runs) => runs.length ?
  [...runs].sort((a, b) => b.run_seq - a.run_seq)[0] : undefined` — newest
  run regardless of outcome. This covers the in-flight case too: the
  engine only ever has one non-terminal run per session and it's always
  the newest by `run_seq` (same assumption `usePipelineRunHistory.ts`
  already relies on for its own "pick the newest terminal run" default).
  Because it's always the *newest* run, no `usePipelineRunHistory`-style
  upper-bound filtering is needed — there's nothing newer to leak in.

`useGraphRunState`'s public return type stays `RunFoldState | undefined`
(don't touch `DependencyPicker`'s existing consumption of it).

Existing tests that must keep passing unmodified after this refactor:
`usePipelineRun.test.ts` (all of it, including both resume tests) and
`useGraphRunState.test.ts`'s two existing tests (both use `client.runs =
[]` implicitly via `FakePipelineClient`'s default, so `pickRun` finds
nothing and behavior is unchanged — live-only fold).

Add a new test to `useGraphRunState.test.ts` proving the actual fix:
seed `client.runs`/`client.runEvents` with a **finished** run (`outcome:
"incomplete"`, mirroring `recordedTasks`'s blocked-task scenario or a
smaller synthetic one like `usePipelineRun.test.ts`'s resume test) and
assert `useGraphRunState` folds it on mount without any live SSE events
ever arriving — this is the case `usePipelineRun`'s own resume tests don't
cover (they only test the in-flight branch) and the case this task
actually depends on.

### `BlockedTaskFeedback` extraction

`ui/src/pipeline/Board/BlockedPanel.tsx` currently inlines, per blocked
task: a notes list filtered to `blocker`/`observation` types, and a
Retry/Reassign actions row (Retry calls `client.retryTask(taskId)`;
Reassign is permanently disabled with the "not supported by the current
pipeline engine" tooltip — `reassignTask` throws on every real/fake client
today, leave it disabled, don't wire it up further).

Extract that per-task body into `ui/src/pipeline/Board/
BlockedTaskFeedback.tsx` (+ `.module.css`, moving the `.feedback`, `.note`,
`.actions`, `.error` rules out of `BlockedPanel.module.css`):

```ts
export interface BlockedTaskFeedbackProps {
  taskId: string;
  notes: Note[];
  client: PipelineClient;
}
export function BlockedTaskFeedback({ taskId, notes, client }: BlockedTaskFeedbackProps)
```

Owns its own `retrying`/`error` local state (simpler than today's
panel-wide `retryingId`/`errorId` keyed-by-id state, and behaviorally
equivalent since each card mounts its own instance). Renders the notes
`<ul>` + actions `<div>` + optional error `<div>` — no outer `Card`/
`Chip`/header, so both call sites can wrap it however fits their layout.

Update `BlockedPanel.tsx` to render `<BlockedTaskFeedback taskId={task.id}
notes={task.notes ?? []} client={client} />` in place of the inlined
markup, keeping its own `Card` + clickable header + "blocked at {stage}"
badge as-is. `BlockedPanel.test.tsx` asserts on rendered text/roles/
titles, not internal structure — it should pass unmodified against this
refactor; if it doesn't, that's a sign the extraction changed observable
behavior, not just structure.

### `TaskDetailSheet.tsx` changes

Add, likely right below the Status field (most contextually relevant
neighbor, and the highest-visibility spot — this is the exact information
Drew says he expected next to task status):

```tsx
const taskFold = runState?.taskRunState[draft.id];
const hasRunHistory = !!taskFold && (taskFold.attempts.length > 0 || !!taskFold.currentStage);
```

```tsx
{pipelineClient && (hasRunHistory || draft.status === "blocked") && (
  <div className={styles.runState} data-blocked={draft.status === "blocked" || undefined}>
    <span className={styles.fieldLabel}>Pipeline run</span>
    {hasRunHistory && (
      <>
        {taskFold?.currentStage && (
          <p className={styles.stage}>
            {draft.status === "blocked" ? `blocked at ${taskFold.currentStage}` : taskFold.currentStage}
          </p>
        )}
        <StageTimeline attempts={taskFold?.attempts ?? []} />
      </>
    )}
    {draft.status === "blocked" && (
      <BlockedTaskFeedback taskId={draft.id} notes={notes} client={pipelineClient} />
    )}
  </div>
)}
```

Notes on this deliberately:

- The blocked review (notes + Retry/Reassign) is gated on `draft.status
  === "blocked"` alone, **not** nested inside `hasRunHistory` — a blocked
  task's notes and its ability to retry come from the `Task` object and
  `pipelineClient` directly, not from the fold. If the fold hasn't
  resolved yet (async `listRuns`/`getRunEvents` still in flight) or
  genuinely has nothing, blocker note + Retry must still show. Only the
  stage-timeline portion depends on `hasRunHistory`.
- Uses the `notes` state array (not `task.notes`) — same source of truth
  the rest of the sheet already uses for confirm/discard, so a discarded
  blocker note disappears from both places consistently.
- Retry needs no explicit refresh wiring: `client.retryTask` flips the
  task's status server-side, which fires `task.status_changed` on the
  shared event bus; `useTaskStore` (in `TasksTab.tsx`) already refetches
  on that event, so `selectedTask` → `TaskDetailSheet`'s `task` prop
  changes → the existing `useEffect(() => { setDraft(task); setNotes(...)
  }, [task])` picks it up. No new plumbing needed.
- This duplicates blocker-note *content* with the existing generic
  "Blocker" `NoteGroup` further down the sheet. That's intentional, not a
  bug to clean up — Drew asked for "the same review surface the Pipeline
  tab shows," which has retry/stage framing the generic notes editor
  doesn't. Don't touch `NoteGroup`/`NOTE_GROUPS`.
- Import `StageTimeline` from `../../pipeline/DrillIn/StageTimeline` and
  `BlockedTaskFeedback` from `../../pipeline/Board/BlockedTaskFeedback`.

Add matching CSS to `TaskDetailSheet.module.css`: a `.runState` block
(flex column, `gap: var(--space-2)`, `margin-bottom: var(--space-4)`,
reusing `.fieldLabel`'s look for the heading), and a `[data-blocked]`
variant with the same bad-tone treatment `BlockedPanel.module.css`'s
`.panel` uses (`border: 1px solid var(--bad); background: var(--bad-soft);
border-radius: var(--r); padding: var(--space-3);`) so a blocked task's
review is visually hard to miss here too, matching the Pipeline tab.

## Tests

`ui/src/tasks/DetailSheet/TaskDetailSheet.test.tsx` currently uses
`FakeRouterClient`/`fixtureTasks` (a different session than
`recordedSessionId`). For the new tests, pull in `recordedTasks`/
`recordedSessionId`/`recordedEnvelopes` from `ui/src/pipeline/client/
recordedRun.ts` and `FakePipelineClient` from `ui/src/pipeline/client/
fixtures.ts` — same fixtures `BlockedPanel.test.tsx` and
`useGraphRunState.test.ts` already use, so the blocked task in
`recordedTasks` (`flaky api`, blocked at `code_review`, real blocker note
content) is the one to drive the journey off, same idiom as
`BlockedPanel.test.tsx`'s `playRecordedRun()` helper (emit
`recordedEnvelopes` through `FakeEventSource.instances[0]`).

Required cases:

- A task with pipeline history (any status) renders `StageTimeline`
  (assert `screen.getByTestId("stage-timeline")`) and the current-stage
  text once the recorded run's events are played.
- The blocked task shows its blocker note content and a "Retry" button;
  clicking Retry calls `client.retryTask` with that task's id (mirror
  `BlockedPanel.test.tsx`'s retry test: rebind `client.retryTask` to
  capture the argument).
- No "Pipeline run" section renders when `pipelineClient` is omitted
  (existing opt-in convention — mirror the `useGraphRunState` "no client"
  test).
- `BlockedPanel.test.tsx` passes unmodified.

### e2e journey (`make ui-smoke`)

Add or extend a spec under `ui/e2e/` (e.g. extend `session-work.spec.ts`,
which already seeds a blocked-dependency scenario and asserts "0 blocked"
in Graph mode) to actually get a task into `blocked` status via a scripted
provider failure (see `ui/e2e/pipeline-drillin.spec.ts`'s scenario.yaml
pattern for a stage that emits `STAGE: fail`), then: Tasks tab → click the
blocked task → assert the blocker note text and a "Retry" button are
visible in the detail sheet → click Retry → assert the task leaves
`blocked` (same outcome-observation idiom `BlockedPanel`'s existing e2e
coverage, if any, or `session-work.spec.ts`'s dependency-picker test uses
for its own PUT/status assertions).

## Risks / open questions for the executor

- Double-check the assumption "at most one non-terminal run per session,
  always the newest by `run_seq`" against `services/router/internal/api/
  pipelines.go` before relying on it for `useGraphRunState`'s `pickRun` —
  it's already implicit in `usePipelineRunHistory.ts`'s default-run
  selection, but confirm rather than trust this doc.
- If a task has been through *multiple* pipeline runs (e.g. blocked in an
  older run, later superseded by a newer run that didn't touch it), the
  newest-run fold won't have this task's attempts — `hasRunHistory` will
  be false and only the blocked-review (not the timeline) will show. This
  matches Pipeline tab's own default (non-picker) view and is an
  acceptable, pre-existing limitation, not a regression to fix here.
