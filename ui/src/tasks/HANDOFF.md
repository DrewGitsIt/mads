# HANDOFF — W6.4 Tasks tab & Context Rail

Architect stage output. Executor: read this fully before writing code. Unlike
W6.3 (Conversation tab), this task's backend gap is *shallow* — the Data
Service already has full task/note CRUD — so a real, if thin, Router change
is in scope here. Read §1 before assuming this is another "build against
fixtures only" task.

## 0. Coordination check (do this first)

Re-run these checks against the current `main`/target branch before writing
code — this doc was written against a worktree where none of it existed yet:

- **W6.2 (Session list & Launchpad, tab registry)**: not landed as of design
  time (`docs/MADS-WORK-ITEMS.md` git history shows no W6.2 merge; `App.tsx`
  is still the bare icon-rail scaffold with no tab switch). W6.4's own
  dependency line is `W6.1, W1.3, W5.1` — not W6.2 — so, like W6.3, this is
  designed to be buildable and reviewable standalone via a demo mount (§8),
  not wired into `App.tsx`.
- **W6.3 (Conversation tab)**: landed (`086b5ef`). `ui/src/router/{client,types,fixtures}.ts`
  already exist with a `RouterClient` interface, `FetchRouterClient`, and
  `FakeRouterClient`. **Extend these in place** — do not create a second
  Router client module. This doc's §3 additions are written as diffs against
  what's already there.
- **W6.5 (Files tab)**: not landed. The Context Rail's Changes tab (§7) reads
  session file changes but does not open a full diff view — that stays a
  `onOpenFile` callback prop, wired to nothing until W6.5 exists (see §7).

## 1. Backend reality check — this task DOES touch Go code

Read `services/data/internal/api/tasks.go` and `pkg/dataclient/dataclient.go`
before assuming anything is missing. As of this design:

- **The Data Service already has full task/note CRUD**: `POST /tasks`,
  `POST /tasks/import`, `GET /tasks`, `GET /tasks/cycles`, `GET /tasks/{id}`,
  `PUT /tasks/{id}`, `POST /tasks/{id}/confirm|discard`, `POST
  /tasks/{id}/notes`, `POST /tasks/{id}/notes/{noteId}/confirm|discard`. All
  of it is already wrapped by `pkg/dataclient.Client` (`CreateTask`, `Tasks`,
  `Task`, `UpdateTask`, `ConfirmTask`, `DiscardTask`, `ImportTasks`,
  `AddNote`, `ConfirmNote`, `DiscardNote`, `Cycles`).
- **The Router has *none* of these routes.** `services/router/internal/api/api.go`'s
  mux has no `/tasks*` entry at all today.
- **This is the one real gap, and it's cheap to close**: the Router's
  `server` struct already holds a `*dataclient.Client` (`s.data`) with every
  method above ready to call. This is unlike W6.3's message/interrupt gap,
  where the underlying capability didn't exist yet anywhere. Here it's a pure
  proxy — no new business logic, no new persistence, same shape as
  `sessions.go`'s existing `handleCreateSession`/`handleBounceSession`.
- **Therefore: add the proxy routes to the Router as part of this task.**
  Don't route around this one by building only against fixtures — the
  acceptance criteria explicitly requires "draft confirm/discard round-trips
  against a real Router + datatest-backed Data Service," which is a Go-level
  test (`services/router/internal/api/tasks_test.go`, new file) using the
  existing `newTestServer(t)` pattern from `api_test.go` (real `datatest`
  Data Service, real Router `api.New(...)` handler, no mocks). Mirror
  `retros_test.go`/`agents_test.go` for style.
- **`GET /events` already supports task/note streaming — no Router change
  needed for live updates.** It proxies `dataclient.Stream(ctx, since,
  sessionID, types)` and `contracts.EventType.MatchesFilter` already supports
  domain wildcards (`"task.*"`). `GET /events?session_id=<id>&types=task.*,note.*`
  works today. Use this for the graph/list live-update requirement — do
  **not** wire through `RouterClient.streamSession` (that hits
  `/sessions/:id/stream`, a data-plane endpoint that still doesn't exist per
  W6.3's HANDOFF §1). Add a **new**, separate `RouterClient` method for this
  (§3) — don't overload `streamSession`, the two are semantically different
  planes (control vs. data).
- **Also proxy `GET /sessions/{id}/files`** (`dataclient.FileChanges`,
  already implemented at `services/data/internal/api/sessions.go`) — the
  Context Rail's Changes tab (§7, in this task's explicit scope: "Context
  Rail with Tasks/Changes tabs") needs it and it's the same one-line-proxy
  shape. Note the path name mismatch: MADS-UX §3.3 calls it `GET
  /sessions/:id/changes`; the real Data Service route is `GET
  /sessions/{id}/files`. Proxy the real path, don't invent a new name on the
  Router — flag the spec/reality mismatch in the PR description, don't
  silently rename.
- **Two more spec-vs-reality mismatches to know about, not fix:**
  - MADS-UX says `PATCH /tasks/:id`; the Data Service (and therefore the
    Router proxy) is `PUT /tasks/{id}`, **full-replace**. This is
    load-bearing for the UI design in §5 — read it before building any
    "edit one field" flow.
  - MADS-UX says `GET /tasks?session=:id`; the real query param (Data
    Service's `TaskQuery`, `dataclient.Tasks`) is `session_id`. Use
    `session_id` everywhere in the UI code.

### 1a. Router changes (new file + registration)

`services/router/internal/api/tasks.go` (new), same style as `sessions.go` —
thin handlers, `decode`/`writeError`/`writeJSON` from `api.go`, no new
abstractions:

```
handleCreateTask        POST   /tasks                              -> s.data.CreateTask
handleImportTasks       POST   /tasks/import                       -> s.data.ImportTasks
handleListTasks         GET    /tasks                               -> s.data.Tasks(session_id, status)
handleTaskCycles        GET    /tasks/cycles                        -> s.data.Cycles(session_id)
handleGetTask           GET    /tasks/{id}                          -> s.data.Task
handleUpdateTask        PUT    /tasks/{id}                          -> s.data.UpdateTask
handleConfirmTask       POST   /tasks/{id}/confirm                  -> s.data.ConfirmTask
handleDiscardTask       POST   /tasks/{id}/discard                  -> s.data.DiscardTask
handleAddNote           POST   /tasks/{id}/notes                    -> s.data.AddNote
handleConfirmNote       POST   /tasks/{id}/notes/{noteId}/confirm   -> s.data.ConfirmNote
handleDiscardNote       POST   /tasks/{id}/notes/{noteId}/discard   -> s.data.DiscardNote
handleSessionFiles      GET    /sessions/{id}/files                 -> s.data.FileChanges
```

Register all in `api.go`'s `New()` mux alongside the existing entries.
`GET /tasks/cycles` must be registered as a literal path — Go 1.22+'s
`http.ServeMux` already resolves `/tasks/cycles` over the `/tasks/{id}`
wildcard by specificity, proven by the Data Service registering the exact
same pair today; no ordering trick needed, just register both.

Error handling: use the Router's own `writeError` (400 for
`*contracts.ValidationError` — flat, not 422; 404 for `dataclient.ErrNotFound`)
— **not** the Data Service's `writeStoreError`. This keeps the Router
consistent with its documented convention (see `services/router/README.md`'s
"Errors" section) across every proxy route, not just sessions.

No `handleAddHandoff` proxy — nothing in this task's scope writes handoffs
(that's pipeline-stage machinery, W6.6). Skip it; add later if a real caller
needs it.

Update `services/router/README.md`: add the new routes to the API contract
list (same one-liner-per-route style already used there) and a short
Critical-paths note that task/note/file-change proxying is a pure pass
-through with zero Router-side state, matching the file's existing framing
("Router... holds no business logic about what sessions or pipelines do").

### 1b. Non-goal: `POST /sessions/:id/pipeline/start`

MADS-UX §3.3 lists this as a Tasks-tab action. The Router's actual pipeline
route is already `POST /pipelines` (body `{session_id, template_id}`,
existing, unrelated to this task). Launching a pipeline from the Tasks tab
is not in this task's "Done when" criteria (create → link deps → Queued →
confirm draft → live graph update — no pipeline launch). **Don't build a
Start Pipeline button here.** If a future task wants one, it calls the
existing `POST /pipelines`, not a new route.

## 2. Queued display rule — the load-bearing interpretation

MADS-UX: "dependency-waiting tasks render as calm 'Queued' ... even though
their schema status is `blocked` — red is reserved for tasks that need a
human." `contracts.Task` has **no field that distinguishes these two cases**
— just `Status` (`"blocked"` either way) and `Dependencies []string`. There
is no `blocked_reason` in the schema. This is a real data-model gap, same
class as W6.3's `ContextMeter`/trust-pill gaps — solved here with a
client-side heuristic, not a backend change (flag it, don't silently invent
a server field):

```
isDependencyQueued(task, tasksById) =
  task.status === "blocked"
  && task.dependencies.length > 0
  && task.dependencies.some(depId => tasksById[depId]?.status !== "done")
```

If `task.status === "blocked"` and this is **false** (no deps, or every
listed dep is already done, so nothing dependency-shaped explains the
block) → render **needs-human** (red/`bad` tone, per the rule's reservation).
If **true** → render calm **Queued**: dashed ring + `← dep` chips naming the
specific not-yet-done dependencies (tone `info`, not `bad`/`warn` — this is
deliberately a *fourth* visual bucket alongside the real `ok`/`warn`/`bad`
tones, reusing the existing `--info` token rather than inventing a new
semantic color that `tokens.test.ts` would need to pin).

Implement this once, in `ui/src/tasks/ListView/statusPresentation.ts`, as a
pure function `(task, tasksById) -> { label, chipTone, dashedRing, depChips }`.
`TaskRow`, `GraphNode`, and the Context Rail's compact row all call it — the
rule lives in exactly one place. Don't reimplement it in the graph or the
rail.

The "dashed ring" itself is a local CSS class (`border: 2px dashed
var(--info)` or similar), not a new kit component or token — scope it to
`TaskRow.module.css`/`GraphNode.module.css`. If W6.6's Pipeline tab later
wants the same "queued" treatment for blocked pipeline tasks, that's the
signal to promote it into the shared kit; don't preempt that now.

## 3. `ui/src/router/` additions (extend in place, don't fork)

`types.ts` — add, mirroring `pkg/contracts/task.go` / `session.go` field-for
-field:

```ts
export type TaskStatus =
  | "ready" | "in_progress" | "blocked" | "needs_review" | "done" | "wont_do";

export interface Task {
  id: string;
  session_id: string;
  status: TaskStatus;
  dependencies?: string[];
  labels?: string[];
  retry_count: number;
  created_at: string;
  updated_at: string;
  title: string;
  description?: string;
  acceptance_criteria?: string;
  scope?: string;
  draft?: boolean;
  source: string;
  dpa_id?: string;
  notes?: Note[];
  // handoffs omitted — not read by this tab (§1a)
}

export type NoteType =
  | "blocker" | "decision" | "context" | "risk" | "follow_up" | "observation";
export type NoteStatus = "draft" | "confirmed";
export interface Note {
  id?: number;
  type: NoteType;
  content: string;
  source: string;
  status: NoteStatus;
  timestamp: string;
}

// mirrors pkg/contracts/session.go FileChange (Context Rail Changes tab, §7)
export interface FileChange {
  id: number;
  session_id: string;
  task_id?: string;
  repo?: string;
  path: string;
  change: "added" | "modified" | "deleted";
  origin: string;
  attributed: boolean;
  actor?: string;
  timestamp: string;
}
```

`client.ts` — add to `RouterClient`:

```ts
listTasks(sessionId: string, status?: TaskStatus): Promise<Task[]>;
getTask(id: string): Promise<Task>;
createTask(task: Partial<Task>): Promise<Task>;
updateTask(task: Task): Promise<Task>;       // full replace — see §5
confirmTask(id: string): Promise<void>;
discardTask(id: string): Promise<void>;
importTasks(tasks: Partial<Task>[]): Promise<Task[]>;
taskCycles(sessionId: string): Promise<string[][]>;
addNote(taskId: string, note: { type: NoteType; content: string }): Promise<Note>;
confirmNote(taskId: string, noteId: number): Promise<void>;
discardNote(taskId: string, noteId: number): Promise<void>;
getSessionChanges(sessionId: string): Promise<FileChange[]>;
streamTaskEvents(sessionId: string): SessionEventSource; // GET /events?session_id=&types=task.*,note.*,file.changed
```

`FetchRouterClient` implementations are straight fetch wrappers like the
existing ones, **except** they must surface `ValidationError` distinctly so
the bulk-import/edit UI can show all field errors at once (design rule
10.3), not just a flat string. Add:

```ts
export class RouterValidationError extends Error {
  constructor(public readonly errors: { field: string; message: string }[]) {
    super(errors.map(e => `${e.field}: ${e.message}`).join("; "));
  }
}
```

and have the task-mutating `FetchRouterClient` methods check for a 400
response whose body matches `{errors: [...]}` and throw
`RouterValidationError` instead of the generic `Error` the other methods
throw. This is a genuinely new pattern in `client.ts` (nothing in the
Conversation tab needed field-level errors) — keep it narrowly scoped to the
task-mutating methods, don't retrofit the existing ones.

`fixtures.ts` — add fixture tasks covering every case a reviewer needs to
see without a real backend: a plain `ready` task, an `in_progress` task, a
dependency-queued `blocked` task (dep not done — renders calm), a
needs-human `blocked` task (no deps — renders red), a `needs_review` task, a
`done` task, one **draft** task (`draft: true`) with a draft note, and a
small 2-task dependency cycle (for graph cycle-highlighting review). Add
matching `fixtureFileChanges`.

**Make `FakeSessionEventSource` actually dispatch-capable** — today's
version (used by the Conversation tab) is a total no-op, which is fine there
since nothing consumes `streamSession` yet. This task's acceptance criteria
("graph updates from a streamed task.* event in a component test") needs a
fake that can push events. Extend it in place:

```ts
export class FakeSessionEventSource implements SessionEventSource {
  private listeners: ((ev: MessageEvent<string>) => void)[] = [];
  addEventListener(_: "message", fn: (ev: MessageEvent<string>) => void) { this.listeners.push(fn); }
  removeEventListener(_: "message", fn: (ev: MessageEvent<string>) => void) {
    this.listeners = this.listeners.filter(l => l !== fn);
  }
  close() {}
  /** test-only: simulate a server-pushed envelope */
  dispatch(envelope: Partial<Envelope> & { type: string }) {
    const ev = { data: JSON.stringify(envelope) } as MessageEvent<string>;
    this.listeners.forEach(fn => fn(ev));
  }
}
```

`.data` as a JSON-stringified envelope matches the real wire format —
`services/router/internal/api/events.go` re-emits the Data Service's
`id:`/`event:`/`data:` SSE framing with the envelope JSON as the `data:`
line, and `EventSource.onmessage`'s `.data` is exactly that string. Give
`FakeRouterClient.streamTaskEvents` a way to return the *same* instance a
test constructed (constructor param or a settable field), so the test can
hold a reference to dispatch on.

## 4. Module layout

```
ui/src/tasks/
  HANDOFF.md                    (this file)
  TasksTab.tsx                  top-level: list/graph toggle, owns useTaskStore,
                                 renders TaskDetailSheet on selection
  TasksTab.module.css
  TasksTab.test.tsx

  useTaskStore.ts                fetch GET /tasks (session_id), subscribe via
                                  streamTaskEvents; on any task.*/note.* envelope,
                                  re-fetch *that* task (getTask, keyed by
                                  envelope.task_id) and patch it into state, or
                                  drop it from state on task.discarded — see §6,
                                  event payloads are sparse, don't trust them for
                                  full state
  useTaskStore.test.ts

  ListView/
    TaskListView.tsx             grouped-by-status (All groups return per MADS-UX
                                  design rule; single-status quick filter collapses
                                  grouping)
    TaskListView.module.css
    TaskListView.test.tsx
    TaskRow.tsx                  title, status chip/dashed-ring via statusPresentation,
                                  dep chips, label chips
    TaskRow.module.css
    TaskRow.test.tsx
    statusPresentation.ts        §2's pure function — single source of truth
    statusPresentation.test.ts

  GraphView/
    TaskGraphView.tsx            composes layout.ts + GraphCanvas + Minimap + GraphNode
    TaskGraphView.module.css
    TaskGraphView.test.tsx
    layout.ts                    pure fn (tasks, cycles) -> {nodes, edges} via dagre,
                                  see §5 for determinism requirements
    layout.test.ts
    GraphCanvas.tsx               SVG viewport: drag-to-pan, wheel+ctrlKey-to-zoom
                                  (clamped), on-canvas zoom controls
    GraphCanvas.test.tsx
    Minimap.tsx                   scaled-down SVG mirror + viewport rect, click-to-
                                   recenter; geometry from state only, see §5
    Minimap.test.tsx
    GraphNode.tsx                 status-colored node (via statusPresentation),
                                   cycle-highlighted edges get a distinct stroke
    GraphNode.test.tsx

  DetailSheet/
    TaskDetailSheet.tsx           SideSheet: every field editable, full-object PUT
                                   on save (§5's PUT gotcha), markdown rendering for
                                   description/acceptance_criteria (reuse
                                   ui/src/conversation/ChatRenderer/markdown.tsx —
                                   don't fork a second sanitizer)
    TaskDetailSheet.module.css
    TaskDetailSheet.test.tsx
    NoteGroup.tsx                 collapsible group per NoteType; draft notes use
                                   kit Draft/DraftChip + Confirm/Discard
    NoteGroup.test.tsx
    DependencyPicker.tsx          multi-select of other session tasks; cheap local
                                   DFS warns "would create a cycle" without a network
                                   call (see §5); the authoritative cycle set for
                                   graph highlighting still comes from GET /tasks/cycles
    DependencyPicker.test.tsx

  DraftCard/
    TaskDraftCard.tsx             kit Draft wrapper + Confirm/Edit/Discard row; Edit
                                   opens TaskDetailSheet in edit mode without confirming
                                   (confirm stays a separate explicit action)
    TaskDraftCard.module.css
    TaskDraftCard.test.tsx

  BulkImport/
    BulkImportPanel.tsx           hosted in a second SideSheet (mutually exclusive
                                   with the detail sheet, not a new Modal component —
                                   see §5); JSON textarea, Validate/Import, renders
                                   every RouterValidationError field at once
    BulkImportPanel.module.css
    BulkImportPanel.test.tsx

ui/src/context-rail/
  ContextRail.tsx                 Tasks/Changes tab switch, collapsible, status
                                   quick-filter chips (All/In progress/Ready/Review/Done)
  ContextRail.module.css
  ContextRail.test.tsx
  RailTaskList.tsx                 compact TaskRow reuse (a `compact` prop on TaskRow,
                                    not a forked component) + draft cards pinned to top
  RailTaskList.test.tsx
  RailChangesList.tsx              compact list from getSessionChanges; A/M/D badge +
                                    attribution; click invokes an onOpenFile(path) prop
                                    — wired to nothing until W6.5's Files tab exists,
                                    per §0
  RailChangesList.test.tsx
```

`ContextRail` is a sibling of `ui/src/tasks/`, not nested under it — it's
constant across *all* Session Workspace tabs per MADS-UX §3.3, not
Tasks-specific, even though this task is the one that builds it.

## 5. Design notes with real gotchas

**Full-replace PUT, not PATCH (§1's mismatch, load-bearing here).** Every
"edit one field" interaction in `TaskDetailSheet` — inline title rename,
status dropdown, dependency picker, label editor — must build the *entire*
updated `Task` object (spread the current task, override the changed
field(s)) and call `updateTask(fullTask)`. Sending a partial object would
null out every other field server-side (`handleUpdateTask` does
`t.ID = r.PathValue("id")` then a full `UPDATE ... SET` with whatever
deserialized, no merge). Keep `TaskDetailSheet`'s local form state as one
full `Task` draft object being edited, not per-field PATCH calls.

**Determinism for the graph (`layout.ts`).** Feed `dagre` (recommend
`@dagrejs/dagre` — the maintained fork of the unmaintained `dagre` package,
same API, ships its own TS types, no `@types/dagre` needed) a
**deterministically ordered** node list — sort tasks by `id` before adding
them to the graph, since dagre's layout output depends on insertion order
for tie-breaking. Use **fixed node dimensions from a cheap heuristic**
(e.g. `width = clamp(120, 8*title.length, 260)`, constant `height`) — do
**not** measure rendered text width via `getBBox`/`getComputedTextLength`,
which is unreliable-to-absent under happy-dom and would make layout
non-deterministic across environments. This also happens to be exactly what
makes the graph testable at all under happy-dom: `layout.ts` is a pure
function with no DOM dependency, so `layout.test.ts` can assert exact `{x,
y}` node positions for a small fixed graph.

**Cycle highlighting: two separate mechanisms, don't conflate them.**
1. `GraphView`'s edge highlighting is **authoritative** — call `GET
   /tasks/cycles` (proxied §1a) once on mount and on every task/note event
   batch, and mark any edge that appears in a returned cycle with a distinct
   stroke style. This is real cycle detection, server-side, already
   implemented (`services/data/internal/store/tasks.go`'s `findCycles`).
2. `DependencyPicker`'s "would create a cycle" warning is a **cheap,
   non-authoritative, client-only heads-up** — a small DFS from the
   candidate dependency forward through its own dependents, checking
   whether it reaches the task being edited — so the picker doesn't fire a
   network request on every checkbox toggle. It's advisory only; the real
   check is `Cycles` after save. Don't try to make these the same code path.

**Minimap/pan/zoom geometry: state-only, never `getBoundingClientRect`.**
Follow the precedent already established by `ResizablePanel` (drives
resize purely off `MouseEvent.clientX/clientY` deltas, asserted via
`fireEvent` in its test, never reads container geometry from the DOM).
`GraphCanvas`'s pan/zoom state (`translateX`, `translateY`, `scale`) and
`Minimap`'s viewport rectangle must be computed from that state plus
`layout.ts`'s known logical extents (min/max node x/y) — never from
`ref.current.getBoundingClientRect()`, which returns all-zero geometry
under happy-dom and would make every interaction test either flaky or
meaningless. Wheel-to-zoom: `onWheel` handler, zoom only when
`event.ctrlKey` is true (trackpad pinch and ⌘/Ctrl+wheel both set this in
real browsers), otherwise treat the wheel delta as pan — this is the "wheel
+ ctrl zoom" the task brief specifies, distinct from MADS-UX's looser "zoom
via mouse wheel" prose. Also render on-canvas +/−/reset zoom controls (plain
kit `Button`s) as the non-wheel path, matching "zoom via mouse wheel **and**
on-canvas controls."

**Bulk import uses a second `SideSheet`, not a new Modal component.** The
kit has no `Modal` (only `Panel`/`SideSheet`/`Toast`) and design rule 4
reserves modals for small single-purpose forms/confirmations. Bulk import's
form (paste JSON, validate, import) fits that description, but rather than
introducing a new kit primitive for one caller, reuse `SideSheet` — it
already renders as an overlay panel. `TasksTab` holds one "which sheet is
open" piece of state (`"detail" | "import" | null`) so the two are mutually
exclusive.

**Reuse the Conversation tab's markdown renderer.** `description` and
`acceptance_criteria` are markdown per MADS-UX. Don't add a second
sanitization path — import `ui/src/conversation/ChatRenderer/markdown.tsx`'s
renderer. Its test file already documents the happy-dom-vs-jsdom DOMPurify
gap (see that file's `// @vitest-environment jsdom` pragma) — any new test
file in this task that renders task description/criteria through it needs
the same per-file pragma, for the same reason.

## 6. Live updates — event payloads are sparse, refetch don't trust

Checked `services/data/internal/store/tasks.go`'s `Append` calls directly:
`task.created` payload is `{title, draft}`, `task.updated` has **no
payload**, `task.status_changed` is `{from, to}`, `task.confirmed`/
`task.discarded`/`note.*` carry no payload at all. **None of these carry
the full task/note state.** `useTaskStore`'s event handler must treat every
envelope as "something about `envelope.task_id` changed, go re-fetch it,"
not try to reconstruct state from the payload:

```
on envelope:
  if envelope.type === "task.discarded": remove envelope.task_id from state
  else if envelope.type.startsWith("task.") || envelope.type.startsWith("note."):
    getTask(envelope.task_id).then(t => patch t into state)  // note.* also
    // refetches the task since notes are embedded on Task, not separate state
```

This is simple, correct, and sufficient for the acceptance criterion (a
component test dispatches one fake envelope via `FakeSessionEventSource.dispatch`
and asserts the graph/list re-renders with updated data from a
`getTask`/`listTasks` call the fake `RouterClient` answers).

**Not building**: SSE reconnect/cursor tracking (`since=` resume) on the UI
side. The Router's `GET /events` supports it, but nothing in this task's
acceptance criteria requires surviving a dropped connection, and adding it
now is scope creep with no test to prove it against. Flag as residual risk,
same as W6.3 did for its own SSE gaps — a future task wiring `since=` back
in in `useTaskStore` is a small, isolated addition, not a redesign.

## 7. Context Rail specifics

- **Tasks tab**: `RailTaskList` — draft `TaskDraftCard`s pinned above the
  regular list (pulse + yellow border per MADS-UX — reuse kit `Draft`, don't
  invent a second provenance treatment), then compact `TaskRow`s. Status
  quick-filter chips (`All`/`In progress`/`Ready`/`Review`/`Done`) filter the
  same `useTaskStore` state the main Tasks tab uses — **share the hook
  instance/instantiate it twice against the same `RouterClient`**, don't
  build a second fetch/subscribe path. If `TasksTab` and `ContextRail` are
  both mounted (main tab + rail simultaneously, per MADS-UX "constant across
  display modes"), consider lifting `useTaskStore` one level up and passing
  the result down as props instead of each owning its own SSE subscription
  — two independent `GET /events` connections per session is wasteful and
  not what the Router doc's "per-subscriber proxy" design implies you should
  do carelessly. Decide this concretely in the demo mount (§8): it needs
  both mounted together to be reviewable, which will surface the answer
  immediately.
- **Changes tab**: `RailChangesList` from `getSessionChanges` (§1a's new
  proxy route) — A/M/D badge from `FileChange.change`, attribution chip from
  `.actor`/`.attributed`. No live `file.changed` subscription is required by
  this task's acceptance criteria (only `task.*` streaming is tested) — a
  one-shot fetch is enough for now; note the gap rather than silently
  building partial SSE wiring for it.
- **Collapsible**: per MADS-UX. A simple width-collapse toggle (kit `Button`
  ghost icon) is enough — don't build a hover-peek interaction like
  `IconRail`'s; that's a different, already-solved pattern for a different
  surface.

## 8. Prototype / review artifact

Same reasoning as W6.3 §8: no Session Workspace shell exists to mount this
into yet (§0). Add `ui/tasks.html` + `npm run tasks` (mirrors `conversation.html`/
`npm run conversation`), mounting `TasksTab` **and** `ContextRail` together
against `FakeRouterClient` fixtures (§3), with a picker to select which
fixture session to view (reuse the active/inactive session picker pattern
from `conversation.html` if convenient) so both the dependency-queued and
needs-human "blocked" cases, the draft card, and the cycle are all visually
reviewable. Add the `tasks` entry to `vite.config.ts`'s
`build.rollupOptions.input` (needed for `make build`, not `make check` —
mirrors the `conversation` entry already there) and document the demo mount
in `ui/README.md` the same way the Conversation tab section does.

Export `TasksTab`/`ContextRail` as plain components (props: `sessionId`,
`routerClient`) so wiring them into a real shell later is what W6.2's own
HANDOFF promised: a one-line addition, not a redesign.

## 9. Test-to-acceptance-criteria map

- "Create → link deps → Queued rendering (calm) verified at component level"
  → `statusPresentation.test.ts` (the heuristic itself) + `TaskRow.test.tsx`/
  `TaskListView.test.tsx` (a fixture task with an undone dependency renders
  the dashed-ring/`info`-tone treatment, not `bad`).
- "a needs-human block renders red" → same test files, a fixture task with
  `status: "blocked"` and either no dependencies or all-done dependencies
  renders `bad` tone.
- "draft confirm/discard round-trips against a real Router + datatest
  -backed Data Service" → **Go test**, `services/router/internal/api/tasks_test.go`,
  using `newTestServer(t)` (real `datatest.Start`, real Router handler): create
  a draft task (`Draft: true` via a direct `dataclient` call or `POST
  /tasks`), `POST /tasks/{id}/confirm`, assert `draft` flips false and
  `task.confirmed` lands on `GET /events`; same for discard, asserting a 404
  on subsequent `GET /tasks/{id}`. Also cover `TaskDraftCard.test.tsx` at the
  component level (Confirm/Discard buttons call the right `RouterClient`
  methods) — the Go test proves the wire round-trip, the component test
  proves the UI wiring; both are needed, they're not redundant.
- "Graph updates from a streamed task.* event in a component test" →
  `TaskGraphView.test.tsx` (or `useTaskStore.test.ts`): construct a
  `FakeSessionEventSource`, mount with a `FakeRouterClient` wired to return
  it from `streamTaskEvents`, call `source.dispatch({type: "task.created",
  task_id: "...", session_id: "..."})`, assert a new node appears (backed by
  the fake's `getTask` answering with the new fixture task).
- "make check passes" → Go side: new `tasks.go`/`tasks_test.go` follow
  existing patterns exactly (real spawned processes already covered by
  `TestMain`'s leak check in the package, nothing new to leak here — this
  task adds no child-process spawning). UI side: `layout.ts`/`Minimap`/
  `GraphCanvas` avoid DOM measurement entirely (§5), so nothing here is
  happy-dom-fragile the way `node-pty`/xterm was in W6.3; the only
  environment-sensitive test surface is markdown rendering, already solved
  by reusing `ChatRenderer/markdown.tsx`'s established `jsdom` pragma
  pattern (§5).

## 10. Non-goals (explicitly out of scope, don't build)

- `POST /sessions/:id/pipeline/start` / any pipeline-launch UI from the
  Tasks tab (§1b) — W6.6 owns the Pipeline tab's run header.
- `POST /tasks/{id}/handoffs` proxy — no caller in this task's scope.
- Live `file.changed` SSE subscription for the Changes tab (§7) — one-shot
  fetch only.
- SSE reconnect/cursor (`since=`) handling in `useTaskStore` (§6).
- Opening a real diff view from the Changes tab — `onOpenFile` callback only,
  unwired until W6.5.
- A shared kit-level "dashed ring"/queued treatment — kept local to this
  task's two components until a second surface (W6.6) actually needs it
  (§2).

## 11. Executor notes (fill in during/after implementation)

Suggested build order: `router/` extensions (types + client + fixtures,
including the dispatch-capable fake, §3) → Router Go proxy routes + Go test
(§1a, can happen in parallel with the above since it's independent code) →
`ListView/statusPresentation.ts` (§2, has no UI yet, easiest to get right in
isolation with pure unit tests) → `ListView/` → `GraphView/layout.ts` (pure,
test it thoroughly before touching rendering) → `GraphView/` (canvas +
minimap + node) → `DetailSheet/` → `DraftCard/` → `BulkImport/` →
`ContextRail/` → `TasksTab.tsx` + `tasks.html` demo mount.

**Deviations from this doc, flag for review:**

- §7's shared-`useTaskStore` question was deferred to the demo mount as
  instructed. Decided: `TasksTab` keeps owning its own `useTaskStore`
  instance (as §4 already says), and `ContextRail` keeps taking
  `tasks`/`tasksById` as props (as already built). The demo mount
  (`TasksDemo.tsx`) therefore runs a *second* `useTaskStore` call to feed
  `ContextRail`. Against `FakeRouterClient` this is free — `streamTaskEvents`
  always returns the same shared fake event source, so both instances stay
  in sync. Against a real backend this would open two genuine SSE
  connections for one session. Whoever builds the real Session Workspace
  container (out of scope here) should lift a single `useTaskStore` call and
  pass `tasks`/`tasksById` into both `TasksTab` and `ContextRail` — that
  would mean adding an optional controlled-props override to `TasksTab`,
  which wasn't added now to keep this change minimal and because it isn't
  exercised by any acceptance criterion. **Scrutinize**: is this deferral
  acceptable, or should `TasksTab` get that override now?
- `TasksTab`'s selection of a task in the rail (`ContextRail`'s
  `onSelectTask`) is not wired into `TasksTab`'s own `TaskDetailSheet` state
  — the demo mount instead renders its own second `TaskDetailSheet` instance
  when a rail row is clicked, to avoid threading cross-component selection
  state through both owners for a demo-only concern. Both sheets are the
  same component with the same full-replace-PUT semantics, so this is
  cosmetic duplication in the demo only, not a functional gap.
- Did not add a Playwright/E2E layer (per standing UI conventions) or any
  Modal component (kit has none; bulk import correctly reuses `SideSheet` per
  §5).

**Not done / residual risk:**

- SSE reconnect/cursor (`since=`) handling in `useTaskStore` — explicitly
  out of scope (§10). A dropped connection currently means the tab silently
  stops updating until remount.
- Live `file.changed` subscription for the Changes tab — one-shot fetch only
  (§7/§10), as specified.
- `DependencyPicker`'s cycle warning is a cheap client-only DFS heads-up
  (§5) — it does not know about cycles introduced elsewhere in the session
  by concurrent edits; `GraphView`'s highlighting (from `GET /tasks/cycles`)
  remains the authoritative signal and is refetched on every task-list
  change.
- The real Session Workspace container that will host `TasksTab` +
  `ContextRail` together in production doesn't exist yet (W6.2 territory) —
  see the shared-store deviation above.
- `make check` passes (Go race tests + lint, UI lint + all 263 vitest
  tests, including the acceptance-criteria tests in `TasksTab.test.tsx`,
  `TaskDetailSheet.test.tsx`, `TaskRow.test.tsx`/`GraphNode.test.tsx` for
  Queued/needs-human rendering, and `useTaskStore.test.ts`/
  `TaskGraphView.test.tsx` for the streamed `task.*` graph update).

**Review fix (post-handoff):** the first pass only had `useTaskStore.test.ts`
proving the *hook's* state updated on a streamed event — nothing asserted the
*graph component* itself re-rendered, which is what the acceptance criterion
("Graph updates from a streamed task.* event in a component test") and this
section's own test-to-criteria map actually call for. `TaskGraphView` takes
`tasks`/`tasksById` as props and doesn't own `useTaskStore` (that's
`TasksTab`'s job, §4), so `TaskGraphView.test.tsx` gained a small
`GraphHarness` wrapper that composes `useTaskStore` + `TaskGraphView` the same
way `TasksTab` does, plus two tests: a streamed `task.created` event renders a
new node (new title text appears in the SVG), and a streamed `task.updated`
event replaces an existing node's rendered title. Both go through the real
`GraphNode`/`GraphCanvas` render path, not just store state.
