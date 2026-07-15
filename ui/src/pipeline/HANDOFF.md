# HANDOFF — W6.6 Pipeline tab

Architect stage output. Executor: read this fully before writing code. This
task has more backend-reality gaps than W6.3/W6.4 combined — several things
MADS-UX §3.3 describes don't exist yet at any layer, not just unproxied. Every
gap below has an explicit call (build it / stub it / flag it); don't
re-litigate them, but do flag anything you find that contradicts what's
written here (this doc was written by reading the code, not by assumption,
but "as of this design" always has a shelf life).

## 0. Coordination risk

W6.4 (Tasks tab) and W6.5 (Files tab) are siblings this task does **not**
depend on (`docs/MADS-WORK-ITEMS.md`: W6.6 depends on `W6.1, W3.3, W4.2` only)
and, as of this design, neither has landed (`ui/src/tasks/`, `ui/src/files/`
don't exist). Good — no shared-module collision risk there. The only files
this task touches that a concurrent surface might also touch:

- `ui/vite.config.ts` (adding a `pipeline` build entry, same pattern as
  `conversation`) and `ui/package.json` (`"pipeline": "vite --open
  /pipeline.html"` script) — additive, low collision risk, same shape as
  W6.3's `conversation` entry.
- `services/router/internal/api/api.go`'s route list (§2 below adds one line)
  — same file W6.2/W6.3 and any future Router work touch; keep the diff to
  exactly one new `mux.HandleFunc` line plus a new handler file, don't reorder
  existing lines.

No shared `ui/src/router/` type changes needed — this task only *adds* a new
`ui/src/pipeline/` module and reuses `subscribeToEvents`/`routerGet`/
`routerPost`/`FakeEventSource` as-is (§3).

## 1. Backend reality check — read this before building anything

Confirmed by reading `services/router/internal/api/*.go`,
`services/pipeline/engine/engine.go`, `services/pipeline/main.go`,
`services/router/internal/supervisor/supervisor.go`, and
`pkg/contracts/{event,task,metrics}.go`:

**Exists and works today, Router-proxied:**
- `POST /pipelines {session_id, template_id}` → `{run_id, addr}` (spawns a
  pipeline child, `services/router/internal/api/pipelines.go`).
- `POST /pipelines/{id}/cancel` → terminates the child, appends
  `pipeline.cancelled`.
- `GET /events?since=&session_id=&types=` — SSE, and **critically**: it
  backfills from `since` (default 0, i.e. full history) through the Data
  Service's persisted log, then stays live on the same connection
  (`handleStreamEvents` → `s.data.Stream(ctx, since, ...)`). This one
  mechanism is how this tab reconstructs *all* board/timeline/blocked-feedback
  state — see §4. No polling, no extra read endpoints needed for any of that.
- `GET /tasks?session_id=` — full `Task` objects including `Notes` (blocker/
  loop-back observation text lives here, already populated by the engine —
  see §1c) and `Dependencies`.

**Does NOT exist anywhere (not just unproxied — genuinely unimplemented):**
- **Pause/Resume.** The engine (`services/pipeline/engine/engine.go`'s
  `run()`) is a tight continuous scheduler with no pause hook, and there is no
  Router route either. This is not a thin-proxy gap; it's a missing engine
  capability. **Do not attempt to implement pipeline pause/resume in this
  task.** Render the buttons (UX/acceptance criteria expect them in the run
  header) but `disabled`, with a `title` tooltip: "not supported by the
  current pipeline engine." Flag for whoever owns Pipeline Engine
  capabilities next.
- **Reassign.** `contracts.Task` has no per-task/per-stage agent-override
  field, and the engine resolves the acting agent purely from
  `template.Stage.Agent` (`auto` → `executor`) — there is no mechanism to run
  one task's retry under a different agent. Same treatment as Pause/Resume:
  render disabled with the same tooltip pattern, don't build it.
- **A live, per-task agent output stream.** `GET /pipelines/{id}/output`
  exists on the Router and proxies straight through
  (`stream_forward.go:handlePipelineOutput`) to the spawned child's `/output`
  — **but `services/pipeline/main.go` is still the wave-0 skeleton** (only
  `GET /health`; no `engine.Run` wiring, no `/output` handler at all). Even
  once that lands, the proxy is a single stream for the *whole run*, with no
  task/stage tagging — there is no way to filter it to one clicked task. This
  is a real, deeper gap than a Router proxy: build the frontend against a
  documented, task-scoped interface (§6) and a fixture; do not attempt to wire
  a real stream in this task. Flag clearly for whoever wires
  `services/pipeline`'s HTTP server to the engine (cross-cutting, not owned
  here).
- **A run-state read endpoint** (`GET /pipeline-runs/:id` per MADS-UX §3.3).
  Doesn't exist. `contracts.PipelineRun` (`pkg/contracts/metrics.go`) is a
  metrics reconstruction (`dataclient.MetricsPipelineRuns`), not proxied by
  the Router, and its `RunSeq` identity (the `pipeline.started` event's seq)
  doesn't match the Router's own `run_id` string (`supervisor.NewRunID()`) —
  two different identifiers for "which run," never reconciled. Not needed:
  §4's event-replay approach reconstructs everything this tab needs without
  it. Don't add this endpoint; not in scope.
- **A template stage-list read endpoint on the Router.** `GET /templates`
  exist on the Data Service (`pkg/dataclient.Client.Template`) but aren't
  Router-proxied. **Deliberately not adding this** (see §4 — columns are
  derived from the observed event stream instead, which needs zero backend
  changes and is simpler to reason about than reconciling two sources of
  stage order).

**One thing worth adding — thin, composed, in scope (case "a" per
[[mads-ux-spec-gaps]]):** `POST /tasks/{id}/retry`. `contracts.Task` already
has `RetryCount int`, and `dataclient.Client` already has both `Task(id)` and
`UpdateTask(t)`. Add a small handler to
`services/router/internal/api/pipelines.go`:

```go
func (s *server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.data.Task(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	task.Status = contracts.StatusReady
	task.RetryCount++
	if err := s.data.UpdateTask(r.Context(), &task); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}
```

Register `mux.HandleFunc("POST /tasks/{id}/retry", s.handleRetryTask)` in
`api.go`, one line, alongside the existing route list — don't reorder others.
Add a Go test mirroring the style in `services/router/internal/api/*_test.go`
(construct a blocked task via a fake `dataclient`/httptest Data Service the
same way existing Router API tests do; assert status flips to `ready` and
`retry_count` increments). **Note the semantics**: this resets the task to
`ready` and lets the *live scheduler pick it up if a run is still in
progress* (the engine already rescans `Data.Tasks()` every loop iteration —
see `engine.go`'s "live work queue" comment). If the run has already
completed (returned from `Run`), retry has no effect until a new `POST
/pipelines` is issued. That's expected pipeline behavior, not a UI bug — don't
try to auto-restart a run from the frontend.

## 1c. What the engine already gives you for free (read before designing state)

From `services/pipeline/engine/engine.go` (traced from `runTask`/`block`/
`note`/`event`):

- `stage.started` / `stage.completed` / `stage.skipped` payload: `{"stage":
  string}` (skipped also carries `"when"`). Envelope `task_id` is always set.
- `stage.failed` payload: `{"stage": string, "reason": string}` — `reason` is
  already the trimmed failure text. **This is the feedback text for a
  loop-back or a block** — no need to parse note content for it.
- A loop-back (stage has `on_fail.loop_back` and hasn't exhausted
  `max_loops`) does **not** get its own event type. It shows up as: another
  `stage.started` for the loop target stage, *and* a
  `note.created`→`NoteObservation` note on the task
  (`"stage %s failed (loop-back %d/%d to %s): %s"`, already includes the
  attempt counter and target). **Attempt count for a given stage = count of
  `stage.started` events with that `task_id`+`stage` name in the replayed
  history.**
- A terminal block: `stage.failed` (as above) **then** a `NoteBlocker` note
  (`"stage %s: %s"`) **then** `task.status_changed` to `blocked` — no
  dedicated `stage.blocked` event exists. Detect "this task is blocked" from
  `Task.Status === "blocked"` (refetched via `GET /tasks`), and get the
  accumulated feedback from `Task.Notes` where `type` is `blocker` or
  `observation` — already present on the `Task` object, no extra fetch.
- The default template's real stage order (`services/data/internal/store/
  migrations/0006_templates.up.sql`, `default-implementation`): `architect →
  executor → build → code_review → merge → verify`. `build`/`code_review`
  loop back to `executor` (`max_loops: 2`); `verify` blocks on failure with no
  loop-back. This is the shape `TestDefaultTemplateReviewLoopBack` in
  `services/pipeline/engine/engine_test.go` exercises end-to-end against
  `providers/fake` — see §5, this is your recorded-fixture source.

## 2. Data model / fold algorithm (the core design decision)

There is no persisted "current stage per task" anywhere — it only exists as
history in the event log. So unlike `useSessionsList` (W6.2's "event as
invalidation ping → refetch the list" idiom, `ui/src/features/sessions/state/
useSessionsList.ts`), the board **must** fold the stage/pipeline event
payloads themselves; there's no GET to refetch instead. Use both idioms,
deliberately, for different pieces of state:

- **Task metadata** (title, status, notes, dependencies, retry_count): `GET
  /tasks?session_id=` on mount, **refetch** the whole list on
  `task.status_changed` / `task.updated` / `note.created` / `task.created` —
  exactly `useSessionsList`'s pattern. Router has no single-task GET, so
  refetching the list is correct, not a shortcut.
- **Stage/run state** (board column per task, attempt counts, elapsed,
  per-stage feedback, per-task touched-file count): fold every `pipeline.*` /
  `stage.*` / `file.changed` envelope from the replayed history into local
  reducer state. This is the deliberate exception to the refetch idiom —
  document it as such in code (a one-line comment pointing here is enough,
  don't re-explain the whole rationale inline).

**Run-boundary problem and its fix**: `stage.*`/`task.*` events carry
`session_id`/`task_id` but **no run id** — a session can have multiple
pipeline runs over its lifetime (cancel, restart), and old runs' events for
the same task IDs are indistinguishable from the current run's by type alone.
Fix: track the `seq` of the most recent `pipeline.started` event seen; on a
new one, **reset all board state** (a fresh run started); fold `stage.*`
events only when `env.seq >= currentRunStartSeq`. On `pipeline.completed` /
`.cancelled` / `.failed`, freeze the board (stop folding, keep displaying) and
show a "run finished" state in the header until the next `pipeline.started`.
This is correct for "one live run at a time per session," which is the only
case the spec's UI supports anyway (no concurrent-runs affordance anywhere in
MADS-UX §3.3).

**Column layout**: no template read endpoint (§1), so derive column order
from first-seen `stage.started`/`stage.skipped` stage names across the
current run's folded events (not per-task — union across the whole run) —
plus two fixed, non-stage areas: a leading **Queued** list (tasks whose
`Status` is `ready`/`in_progress` but no `stage.started` has been folded for
them yet — i.e. still waiting for a scheduler slot or a dependency) and a
trailing **Done** column (`Status === "done"`). **Blocked tasks are not a
column** — pull them out into a separate, visually prominent panel/banner
above or beside the board (per acceptance criteria: "a block surfaces the
blocker note" as a distinct, hard-to-miss element, not a card quietly sitting
in a column), listing each blocked task's title, the stage it blocked at, and
its full accumulated feedback (all `blocker`/`observation` notes, in order).

Known, accepted limitation from this design: a stage the current run's tasks
haven't reached yet won't have a column until a task enters it (e.g. `verify`
won't appear until the first task passes `merge`). This only matters in the
first few seconds after Start; not worth a template-read endpoint to avoid
(see §1's explicit "not adding this" call).

## 3. Module layout

```
ui/src/pipeline/
  PipelineTab.tsx                top-level: composes header + board + drill-in
  PipelineTab.module.css
  PipelineTab.test.tsx
  HANDOFF.md                     (this file)

  client/
    pipelineClient.ts             PipelineClient interface + FetchPipelineClient
    pipelineClient.test.ts        the only file allowed to mock fetch directly
    fixtures.ts                   FakePipelineClient + fixture Task[]
    recordedRun.ts                literal recorded envelope sequence (§5)

  state/
    usePipelineRun.ts              the fold (§2): seeds tasks, subscribes via
                                    subscribeToEvents, maintains board state
    usePipelineRun.test.ts         drives recordedRun.ts through FakeEventSource

  RunHeader/
    RunHeader.tsx                  Start/Pause/Resume/Cancel, elapsed, counts
    RunHeader.module.css
    RunHeader.test.tsx

  Board/
    StageBoard.tsx                 Queued + dynamic stage columns + Done
    StageBoard.module.css
    StageBoard.test.tsx
    TaskCard.tsx                   title, attempt badge when >1, click → drill-in
    TaskCard.module.css
    TaskCard.test.tsx
    BlockedPanel.tsx                prominent blocked-tasks area (see §2)
    BlockedPanel.module.css
    BlockedPanel.test.tsx

  DrillIn/
    TaskDrillIn.tsx                 kit's ResizablePanel (top-edge drag),
                                     hosts StageTimeline + AgentOutputView +
                                     TaskFiles
    TaskDrillIn.module.css
    TaskDrillIn.test.tsx
    StageTimeline.tsx               ordered stage attempts, loop-back
                                     markers, feedback text (§1c)
    StageTimeline.test.tsx
    AgentOutputView.tsx             TerminalView (reused, read-only) over a
                                     PipelineOutputBridge (§6)
    AgentOutputView.test.tsx
    pipelineOutputBridge.ts         PtyBridge-shaped adapter; see §6 for why
                                     it's a stub, not a real transport
    TaskFiles.tsx                   touched-file count + list, tallied from
                                     folded file.changed events (§2)
    TaskFiles.test.tsx

ui/pipeline.html                   demo mount entry (§7)
ui/src/pipeline/PipelineDemo.tsx
```

## 4. `PipelineClient` (`client/pipelineClient.ts`)

A dedicated interface for this tab, analogous to `RouterClient`
(`ui/src/router/client.ts`) but scoped to pipeline actions — don't bloat the
shared `RouterClient` with these; conversation's `RouterClient` doesn't need
them, and `ui/README.md` already documents two coexisting client idioms as
acceptable ("unifying them is follow-up work"). Reuse `routerGet`/
`routerPost`/`ROUTER_BASE` from `router/client.ts` and `subscribeToEvents`
from `router/events.ts` directly — don't reimplement either.

```ts
export interface PipelineClient {
  listTasks(sessionId: string): Promise<Task[]>;
  startPipeline(sessionId: string, templateId: string): Promise<{ run_id: string }>;
  cancelPipeline(runId: string): Promise<void>;
  retryTask(taskId: string): Promise<Task>;
  // Both reject — no backend capability exists (§1). Wired so the interface
  // documents the eventual contract; UI disables the buttons rather than
  // ever calling these in the real client.
  pausePipeline(runId: string): Promise<void>;
  resumePipeline(runId: string): Promise<void>;
  reassignTask(taskId: string, agent: string): Promise<void>;
  streamTaskOutput(runId: string, taskId: string): PipelineOutputSource; // §6
}
```

`FetchPipelineClient`: `listTasks` → `routerGet("/tasks", {session_id})`;
`startPipeline` → `routerPost("/pipelines", {session_id, template_id})`;
`cancelPipeline` → `routerPost(`/pipelines/${runId}/cancel`)`; `retryTask` →
`routerPost(`/tasks/${taskId}/retry`)`; the three unsupported methods reject
immediately with `new Error("not supported by the current pipeline engine")`
without making a network call (don't hit a 404 when we already know it'll
fail).

`FakePipelineClient` (`fixtures.ts`): in-memory, same shape as
`FakeRouterClient` — array of fixture `Task[]`, `startPipeline` returns a
fixed `run_id`, `retryTask` flips the fixture task back to `ready`. The three
unsupported methods still reject (so a component test can assert the buttons
are disabled and never call them, rather than silently succeeding against the
fake in a way the real client never would).

## 5. Recorded stage.* fixture (`client/recordedRun.ts`) — the test-driving mechanism

Per the task brief: drive board/timeline tests with a recorded sequence from
a **real** `engine.Run` against `providers/fake`, not hand-authored JSON. The
exact scenario already exists:
`services/pipeline/engine/engine_test.go`'s `TestDefaultTemplateReviewLoopBack`
— 3 tasks (`core lib ← flaky api ← cli`), the real
`default-implementation` template (§1c's stage order), and a fake reviewer
that fails `flaky api` at `code_review` every time. It produces, deterministically:
`core lib` → `done` (all 6 stages); `flaky api` → 3× `code_review`
`stage.started`/`stage.failed` (1 + `max_loops=2`), 2 loop-back
`NoteObservation`s, 1 `NoteBlocker`, final status `blocked`; `cli` → never
starts (`Summary.Skipped`, dependency never satisfied).

To capture it: temporarily add a small dump inside (or right after) that
test — `t.Logf("%s", must(json.Marshal(h.events(t, "pipeline.*", "stage.*",
"task.*", "note.created", "file.changed"))))` plus a dump of
`h.data.Tasks(ctx, h.sess.ID, "")` for the final task snapshot — run it once
(`go test ./services/pipeline/engine/ -run TestDefaultTemplateReviewLoopBack
-v`), and hand-copy the captured JSON into `recordedRun.ts` as two literal
exported constants (`recordedEnvelopes: Envelope[]`, `recordedTasks: Task[]`).
**Do not leave the dump in the Go test** — revert that temporary change after
capturing; `recordedRun.ts` is a one-time, checked-in recording, not a live
generator wired into CI. Regenerate it by hand only if the default template
or this fake scenario changes.

Drive tests with it via the existing `FakeEventSource` pattern
(`ui/src/router/testFakeEventSource.ts`, already used by
`ui/src/features/sessions/state/useSessionsList.test.ts` — follow that
pattern exactly, don't build a second fake): `vi.stubGlobal("EventSource",
FakeEventSource)`, then `for (const env of recordedEnvelopes)
source.emit(env.type, JSON.stringify(env))` (session id in the fixture tasks
must match whatever `usePipelineRun.test.ts`/`StageBoard.test.tsx` pass in).
This is what makes "a loop-back is visibly rendered (attempt count +
feedback)" and "a block surfaces the blocker note" testable against real
engine behavior rather than an invented sequence.

## 6. Live agent output (`AgentOutputView` / `pipelineOutputBridge.ts`)

Per §1, there is no real per-task output stream today. Reuse `TerminalView`
(`ui/src/conversation/TerminalRenderer/TerminalView.tsx`) rather than building
a second terminal renderer — it already wraps `@xterm/xterm` (pure JS,
happy-dom-safe, already a normal dependency, no node-pty involved at all here)
behind the `PtyBridge` interface. This stream is **read-only** (a pipeline
stage's one-shot agent call has no interactive input channel), so
`pipelineOutputBridge.ts`'s implementation makes `write`/`resize` no-ops and
only ever calls the `onData` listeners it's fed. A `FakePipelineOutputBridge`
in `fixtures.ts` replays a canned string of chunks on a timer, for the demo
mount and `AgentOutputView.test.tsx`. Document in this bridge's file header
(one line) that a real implementation needs `services/pipeline`'s HTTP server
wired to the engine first, plus per-task tagging on `/output` — neither
exists yet, and this task does not add them.

## 7. Demo mount (`pipeline.html` / `npm run pipeline`)

Exactly the `conversation.html` pattern (§8 of
`ui/src/conversation/HANDOFF.md`; `ui/README.md`'s "Conversation tab"
section): add `ui/pipeline.html`, a `pipeline` entry in `vite.config.ts`'s
`rollupOptions.input`, and a `"pipeline": "vite --open /pipeline.html"` script
in `package.json`. `PipelineDemo.tsx` mounts `PipelineTab` against
`FakePipelineClient` + `recordedRun.ts`'s fixture, with a button that
"plays" the recorded sequence into the `FakeEventSource` on demand (so a human
can watch the board move, the loop-back happen, and the block surface,
without needing a real Router). No Session Workspace tab registry exists yet
(same as Conversation) — don't touch `App.tsx`'s `SURFACES` map; export a
plain `PipelineTab` component (props: `sessionId`, `pipelineClient`) so wiring
it in later is the promised one-line addition. Add a short "Pipeline tab"
section to `ui/README.md` mirroring the "Conversation tab" section's shape.

## 8. Test-to-acceptance-criteria map

- "Board moves cards through stages from a recorded event sequence" →
  `usePipelineRun.test.ts` + `StageBoard.test.tsx`, both driven by
  `recordedRun.ts` through `FakeEventSource` (§5).
- "a loop-back is visibly rendered (attempt count + feedback)" →
  `TaskCard.test.tsx` (attempt badge > 1) + `StageTimeline.test.tsx` (loop-back
  marker + `stage.failed`'s `reason` text), both against the `flaky api` task
  in the recorded fixture.
- "a block surfaces the blocker note" → `BlockedPanel.test.tsx`, asserting the
  `flaky api` task appears with its `NoteBlocker` + two `NoteObservation`
  texts, driven by the same recorded fixture.
- "Drill-in shows timeline + streamed output at component level" →
  `TaskDrillIn.test.tsx` (composition + `ResizablePanel` top-edge drag, same
  assertions as `ShellDock.test.tsx`) + `AgentOutputView.test.tsx` (fake
  bridge chunks render and append).
- "make check passes" → `npm run lint`, `npm run test`, root `make check`
  (Go tests including the new `handleRetryTask` test + ui lint + ui test) all
  clean. This task adds no Electron/native-dependency surface (§6 — pure JS
  `TerminalView` reuse, no node-pty), so there's nothing display-dependent to
  guard against beyond what W6.3 already solved.

## 9. Non-goals (explicitly out of scope, don't build)

- Real Pause/Resume, Reassign, or a real per-task live output stream (§1, §6)
  — disabled controls / documented stub interfaces only.
- Any change to `services/pipeline/main.go` (wiring `engine.Run` into an HTTP
  server) — cross-cutting, cross-wave backend work, not this task's job.
- A Router `GET /templates/{id}` proxy — deliberately not added (§1, §2);
  columns are event-derived instead.
- Cost-so-far in the run header — spec §8 explicitly excludes live cost from
  this tab.
- Wiring `PipelineTab` into any shell/tab registry — none exists yet (§7).
- Agent name display in the stage timeline (`agent.selected`'s payload could
  supply it) — cheap-looking but adds a second event-payload type to parse
  for a detail no acceptance criterion tests; deferred.
- Orphan/unmerged-state-per-task display (MADS-UX §3.3 mentions it) — that's
  Files tab (W6.5) territory once merge-back semantics exist; don't
  reconstruct a Files-tab feature here as a placeholder.

## 10. Executor notes

Built per the module layout in §3, in this order: `POST /tasks/{id}/retry`
(§1) + Go test, `client/recordedRun.ts` (§5, captured from a real
`TestDefaultTemplateReviewLoopBack` run, temporary dump reverted from the Go
test after copying), `client/pipelineClient.ts` + `fixtures.ts`,
`state/usePipelineRun.ts` (§2 fold), `RunHeader/`, `Board/` (`TaskCard` →
`StageBoard` → `BlockedPanel`), `DrillIn/` (`pipelineOutputBridge.ts` →
`AgentOutputView` → `StageTimeline` → `TaskFiles` → `TaskDrillIn`), then
top-level `PipelineTab.tsx` + the `pipeline.html` demo mount +
`ui/README.md`'s "Pipeline tab" section. All 11 pipeline test files / 40
tests pass; root `make check` (Go tests + golangci-lint + `npm run lint` +
`npm run test`, 333/333 across 59 files) passes clean.

**Deviations from this doc, flag for review:**

- **`RunHeader` gained an `onRunStarted?: (runId: string) => void` prop**,
  not specified in §3/§4. `RunHeader` owns its own `run_id` state
  internally (needed to enable/disable Cancel), but `PipelineTab` also needs
  the run id to call `pipelineClient.streamTaskOutput(runId, taskId)` from
  the drill-in. Rather than duplicating `RunHeader`'s start-tracking logic
  in `PipelineTab` or lifting `run_id` state up (which would've meant
  `RunHeader` no longer owns it, a bigger change to an already-built,
  already-tested component), added this one optional callback fired right
  after `RunHeader`'s own `setRunId`. Small, backward compatible, but not in
  the original module contract — scrutinize whether lifting `run_id` state
  to `PipelineTab` entirely would have been cleaner.
- **ESLint config change**: `ui/eslint.config.mjs` gained
  `argsIgnorePattern: "^_"` on `@typescript-eslint/no-unused-vars`. This
  surfaced because `pipelineClient.ts`'s stub methods for the three
  unsupported actions (§4 — `pausePipeline`/`resumePipeline`/
  `reassignTask`) have *all* their params unused, and ESLint's default
  `"args": "after-used"` mode only ignores unused params before the last
  *used* one, not params in an all-unused signature, even with an
  underscore prefix. This is a repo-wide config gap the pipeline module
  happened to be the first to trip — fixed at the config level (verified
  zero regressions across the whole `ui/src` tree) rather than dropping
  parameter names (which would've broken call-site arity checks in
  `RunHeader.test.tsx` and `pipelineClient.test.ts`). Scrutinize if a
  narrower, per-file `eslint-disable` would have been preferred over a
  repo-wide rule change.
- **No dedicated `pipelineOutputBridge.test.ts`**, despite it being listed
  as its own file in §3's module layout. Its behavior (mount subscribes
  once, chunks forward, unmount calls `source.close()`) is fully covered by
  `AgentOutputView.test.tsx` instead, since the bridge has no independent
  behavior worth testing in isolation from the component that constructs
  it. Flagging since §3 lists it as a peer test file to the others.
- **`TaskFiles.test.tsx` uses a hand-built fixture, not `recordedRun.ts`.**
  The recorded engine run (§5) has zero `file.changed` events (confirmed by
  grep) — the fake reviewer scenario never touches files. Since
  `TaskFiles` is a pure presentational component (count + list) whose
  contract doesn't depend on fold correctness (already covered by
  `usePipelineRun.test.ts`), used a small hand-built `touchedFiles` array
  instead of extending the recorded fixture.

**Not done / residual risk:**

- **No browser/dev-server smoke test of `pipeline.html`.** This
  environment has no browser-automation tool available. Verified instead
  via `npx vite build` (clean production build of the `pipeline` entry,
  output discarded after the check) plus the full component test suite —
  but the demo has not been visually confirmed to render/interact
  correctly in an actual browser. Whoever reviews this should run `npm run
  pipeline` and click through the "Play recorded sequence" flow once.
- Everything in §9 (Non-goals) remains undone by design, not oversight —
  Pause/Resume/Reassign are disabled controls, there is no real per-task
  output stream, and `services/pipeline/main.go` is untouched.
- `stage.skipped` folding was noted as untested in an earlier session of
  this same task and remains so — the recorded fixture's `cli` task never
  starts at all (`Summary.Skipped`, dependency never satisfied), so no
  `stage.skipped` event exists in the fixture to exercise that fold branch.
  Low risk (it's a straightforward reducer case mirroring
  `stage.started`/`stage.completed`), but not exercised end-to-end.
