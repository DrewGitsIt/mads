# Task graph 2: dependency editing on the canvas

Second half of the task-graph spec, built on top of task graph 1's
xyflow canvas (`GraphCanvas.tsx`, `TaskGraphView.tsx`, `layout.ts`,
`edgeOps.ts`, `tray.ts`, `positions.ts` — all already landed). Read those
files before starting; this doc only describes the delta.

## 0. The one fact that reshapes every bullet below

**There is no dependency-specific REST endpoint.** Grep confirms it:
`services/data/internal/api/api.go` and `services/router/internal/api/api.go`
register `POST /tasks`, `GET /tasks`, `GET /tasks/cycles`,
`GET/PUT /tasks/{id}`, and confirm/discard/notes routes — nothing shaped
like `POST /tasks/{id}/dependencies`. The task brief's "POST the
dependency ... DELETE the dependency" is describing the *operation*
conceptually, not a literal verb — this is the same
spec-ahead-of-backend drift already logged for this repo (MADS-UX §3
routes routinely don't match the real Go handlers). The real, and only,
mechanism is `PUT /tasks/{id}` full-replace via `RouterClient.updateTask`
— exactly the idiom `edgeOps.ts` already uses for the tray drag-out flow.
Every dependency mutation in this task is "read the target task's full
`dependencies` array, splice it, `updateTask` the whole task."

`X-Mads-Actor` is already sent as `"user"` by default by every
`FetchRouterClient` write (`services/data/internal/api/tasks.go`'s
`actor()` helper defaults to `"user"` when the header is absent, and the
Router doesn't currently forward a custom actor header at all for task
writes) — nothing new to wire there.

**Cross-session refusal is unreachable from this UI, and that's fine.**
`TaskGraphView`'s `tasks`/`tasksById` props come from
`useTaskStore(routerClient, sessionId)`, which calls `listTasks(sessionId)`
— every node on screen is already same-session by construction, so a
drag-drawn edge can never actually name a cross-session pair. Don't try
to manufacture a real cross-session scenario end-to-end; test the error
*path* by mocking `routerClient.updateTask` to reject with a
`RouterValidationError` (the exact shape the store returns for
`writeDeps`'s `"belongs to another session"` check,
`services/data/internal/store/tasks.go:503`) and asserting the toast
appears. That exercises the real code path a genuine 400 would hit.

## 1. `edgeOps.ts` — two new plan builders, same idiom as `planEdgeStrip`

```ts
// Connect: bottom (source) handle of `from` dragged to top (target) handle
// of `to` means "to depends on from" (dagre TB ranks `from` above `to`,
// buildEdges already encodes edges as {from: depId, to: dependentId}).
// Returns null for a no-op: self-loop, or the dependency already exists
// (task_deps has PRIMARY KEY (task_id, depends_on) — a duplicate INSERT
// in the same PUT payload would 500, so this guard is load-bearing, not
// cosmetic).
export function planEdgeCreation(
  from: string,
  to: string,
  tasksById: Record<string, Task>,
): EdgeStripPlan | null {
  const target = tasksById[to];
  if (!target || from === to) return null;
  const deps = target.dependencies ?? [];
  if (deps.includes(from)) return null;
  return { updates: [{ task: target, nextDependencies: [...deps, from] }] };
}

// Remove: one entry per deleted edge (Backspace can multi-select and
// delete more than one edge at once — handle the general case, the
// single-edge X-button click is just a one-element call of the same fn).
export function planEdgesRemoval(
  pairs: { from: string; to: string }[],
  tasksById: Record<string, Task>,
): EdgeStripPlan {
  const updates: EdgeStripPlan["updates"] = [];
  for (const { from, to } of pairs) {
    const target = tasksById[to];
    if (!target) continue;
    updates.push({ task: target, nextDependencies: (target.dependencies ?? []).filter((d) => d !== from) });
  }
  return { updates };
}
```

Both reuse the existing `applyEdgeStrip`/`invertPlan` for actually firing
the writes and for undo — no new apply/invert logic needed. Add
`edgeOps.test.ts` cases for both: creation (dep added, self-loop → null,
duplicate → null), removal (single pair, multiple pairs, dangling id
skipped).

## 2. Tray/marker interplay — already free, verify don't re-implement

Re-read `computeGraphLayout` (`layout.ts`) before writing anything here.
The "unsequenced" marker chip and canvas/tray membership are both pure
functions of edge degree, recomputed fresh every render from
`tasks`/`tasksById`. That means:

- Completing an edge onto a marked node: once `updateTask` succeeds and
  `useTaskStore` refetches (streamed `task.updated` → `TaskGraphView`'s
  `tasks` prop changes), `edgeDegree` for that node becomes ≥1 and the
  `unsequenced` flag clears on its own. **No new code.**
- Removing a node's last edge when it has a manual position: degree drops
  to 0, `manualPositions[id]` is still set, so `computeGraphLayout` keeps
  it in `canvasNodes` (not `trayTaskIds`) and `unsequencedIds` picks it
  back up. **No new code.** (This is the existing "membership rule from
  task 1" the brief references — it was already built to survive edge
  removal, not just drag-out.)

Don't add any bespoke tray-transition logic for CREATE/REMOVE — if you
find yourself writing one, it means the update didn't actually flow
through `tasksById`/`tasks` correctly.

## 3. Cycle warning — also mostly free

`TaskGraphView` already holds `cycles: string[][]` and already refetches
`GET /tasks/cycles` in a `useEffect` keyed on `[routerClient, sessionId,
tasks]` — i.e. it already re-checks cycles after every edge mutation,
since any `updateTask` call is followed by a streamed event that changes
the `tasks` prop. **Don't add a second fetch trigger.** What's missing is
just the *rendering*:

- Compute `const cycleNodeIds = new Set(cycles.flat());` in
  `TaskGraphView` and pass it to `GraphCanvas` as a new optional prop
  (default `new Set()`, same convention as `unsequencedIds`).
- Thread it into `TaskNodeData` as `inCycle: boolean` (parallel to the
  existing `unsequenced` boolean) and add a warning-ring style in
  `TaskNode.module.css` using `--warn`/`--warn-soft` tokens — follow
  `Card.module.css`'s existing `box-shadow: 0 0 0 2px var(--accent-soft)`
  pattern for the shape, swap the token.
- Render a banner in `TaskGraphView.tsx` when `cycles.length > 0`:
  `"cycle — the pipeline will refuse to launch these"` (spec's exact
  copy). Style with `--warn`/`--warn-soft`/`--warn-fg`, not `--bad` — this
  is explicitly a warning, not a blocking error ("WARN, don't refuse").
  It clears by itself when `cycles` next resolves empty; no manual
  dismiss needed (but don't make it *un*-dismissable in a way that fights
  a screen reader — a plain `role="status"` div is enough, no close
  button required since it's state-driven).

## 4. Highlight mode — new pure function + GraphCanvas-local state

New file `highlight.ts` (+ `highlight.test.ts`):

```ts
import type { GraphEdgeLayout } from "./layout";

// Ancestors: nodes reachable by walking edges backward from `nodeId`
// (what it depends on, transitively). Descendants: forward (what depends
// on it, transitively — "what does this unblock"). Includes `nodeId`
// itself. Guard with a visited set — cycles are legal at rest (§3 above),
// this must not infinite-loop on one.
export function computeHighlightSet(nodeId: string, edges: GraphEdgeLayout[]): Set<string> {
  const forward = new Map<string, string[]>();
  const backward = new Map<string, string[]>();
  for (const e of edges) {
    (forward.get(e.from) ?? forward.set(e.from, []).get(e.from)!).push(e.to);
    (backward.get(e.to) ?? backward.set(e.to, []).get(e.to)!).push(e.from);
  }
  const result = new Set<string>([nodeId]);
  function walk(start: string, adj: Map<string, string[]>) {
    const stack = [...(adj.get(start) ?? [])];
    while (stack.length) {
      const n = stack.pop()!;
      if (result.has(n)) continue;
      result.add(n);
      stack.push(...(adj.get(n) ?? []));
    }
  }
  walk(nodeId, forward);
  walk(nodeId, backward);
  return result;
}
```

Test with a diamond (A→B, A→C, B→D, C→D: selecting A or D pulls in all
four; selecting B pulls in {A,B,D} but not C) and a disjoint second
component (selecting a node in component 2 never touches component 1's
ids) — matches the acceptance criterion's fixture description.

Wire into `GraphCanvas.tsx`, entirely local to `GraphCanvasInner` (don't
lift to `TaskGraphView` — it already has `edges` as a prop, no new prop
needed from the parent):

- `const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);`
- Extend `handleNodeClick` to also `setSelectedNodeId(node.id)` (same
  click that already fires `onSelectTask`, opening the detail sheet — one
  gesture, two effects, that's intentional per the brief).
- Add `onPaneClick={() => setSelectedNodeId(null)}` to `<ReactFlow>`.
- Add a scoped `document.addEventListener("keydown", ...)` for `"Escape"`
  clearing `selectedNodeId`, same pattern as `ConfirmDialog.tsx` — only
  active (effect re-subscribes) while `selectedNodeId !== null`.
- `const highlightSet = selectedNodeId ? computeHighlightSet(selectedNodeId, edges) : null;`
  Thread into `toXYNodes` as `dimmed: highlightSet !== null && !highlightSet.has(n.id)`
  and into `toXYEdges` as an edge-level dim (an edge dims unless *both*
  endpoints are in `highlightSet`).
- Add `.dimmed { opacity: 0.35; }` to `TaskNode.module.css` (matches the
  `opacity: 0.35` convention already used for dimming elsewhere in the UI
  — `SessionWorkspace.module.css`, `Gallery.module.css`) and an
  equivalent edge-opacity class in `GraphCanvas.module.css`.
- Tray nodes need no changes at all — `UnsequencedTray` isn't wired to
  `selectedNodeId` and its tasks never appear in `edges`, so
  `computeHighlightSet` never includes them and they're structurally
  incapable of highlighting. That *is* "tray nodes never highlight" —
  don't add an explicit exclusion, it'd be dead code.

**Gotcha — must set `deletable: false` on every xyflow node.** Once node
click also selects the node (via xyflow's own built-in click-to-select,
separate from our `onNodeClick` business logic), pressing Backspace with
that node selected will otherwise ALSO fire `onNodesDelete` and — because
`handleNodesChange` already wires `applyNodeChanges` — actually delete it
from local state. Set `deletable: false` in `toXYNodes`'s per-node object
so Backspace only ever touches edges. This is not optional; without it,
clicking a node then pressing Backspace silently vanishes it from the
canvas until the next task refetch repaints it.

## 5. Connect (create) and edge delete (remove) — `GraphCanvas.tsx`

New props on `GraphCanvasProps` (both optional, mirroring
`onNodeDragStop`'s style):

```ts
onConnect?: (source: string, target: string) => void;
onDeleteEdges?: (pairs: { from: string; to: string }[]) => void;
```

- `<ReactFlow onConnect={(c) => c.source && c.target && onConnect?.(c.source, c.target)} .../>`
  — `Handle` types are already asymmetric (`type="target"` Top,
  `type="source"` Bottom, from task 1), so xyflow only ever offers
  bottom→top drags; `connection.source`/`.target` are already correctly
  directioned, no extra validation needed.
- `<ReactFlow onEdgesDelete={(deleted) => onDeleteEdges?.(deleted.map((e) => ({ from: e.source, to: e.target })))} .../>`
  fires on Backspace/Delete with one or more edges selected — the default
  `deleteKeyCode` already includes Backspace, don't override it unless
  you need to *narrow* it.
- X-button-on-selected-edge affordance: register a custom edge type.
  `toXYEdges` needs one more param, a delete callback, so each edge's
  `data` carries its own bound `onDelete: () => void` (closes over that
  edge's `from`/`to` — no id-string-parsing needed anywhere, don't split
  on `"->"`, look the edge up by identity instead):

  ```tsx
  function DepEdge({ id, sourceX, sourceY, targetX, targetY, markerEnd, style, selected, data }: EdgeProps<TaskEdgeType>) {
    const [path, labelX, labelY] = getBezierPath({ sourceX, sourceY, targetX, targetY });
    return (
      <>
        <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style} />
        {selected && (
          <EdgeLabelRenderer>
            <button
              type="button"
              className={styles.edgeDeleteButton}
              style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
              onClick={data?.onDelete}
              aria-label="Remove dependency"
            >
              ✕
            </button>
          </EdgeLabelRenderer>
        )}
      </>
    );
  }
  const EDGE_TYPES = { depEdge: DepEdge };
  ```

  Set `type: "depEdge"` on every edge object in `toXYEdges` and register
  `edgeTypes={EDGE_TYPES}` on `<ReactFlow>`. Keep the existing
  `markerFor`/`className`/`animated` logic in `toXYEdges` unchanged —
  `DepEdge` just renders what it's given, it doesn't decide styling.

## 6. `TaskGraphView.tsx` — wire it up, consolidate toasts

`Toast.tsx` has no queue ("Presentational only — no queue/provider yet");
today's `undoState` already assumes exactly one toast can show at a time.
Creation errors, removal-undo, and the existing drag-out-undo all now
compete for that one slot — **consolidate into a single tagged-union
state** rather than adding parallel `useState`s:

```ts
type GraphToast =
  | { kind: "moved-to-tray"; taskId: string; plan: EdgeStripPlan; previousPosition: Point | undefined }
  | { kind: "dependency-removed"; plan: EdgeStripPlan }
  | { kind: "error"; title: string; message: string };

const [toast, setToast] = useState<GraphToast | null>(null);
```

This replaces the existing `undoState` (rename call sites: `setUndoState`
→ `setToast({ kind: "moved-to-tray", ... })`, and
`handleUndoDragOut`/render branch generalize to switch on `toast.kind`).
`pendingDragOut`/`ConfirmDialog` stay untouched — that's a separate,
pre-write confirmation, not a toast.

```ts
async function handleConnect(source: string, target: string) {
  const plan = planEdgeCreation(source, target, tasksById);
  if (!plan) return; // self-loop or duplicate — silent no-op, not an error
  try {
    await applyEdgeStrip(routerClient, plan);
  } catch (e) {
    setToast({
      kind: "error",
      title: "Couldn't create dependency",
      message: e instanceof Error ? e.message : "Unknown error",
    });
  }
}

async function handleDeleteEdges(pairs: { from: string; to: string }[]) {
  const plan = planEdgesRemoval(pairs, tasksById);
  if (plan.updates.length === 0) return;
  try {
    await applyEdgeStrip(routerClient, plan);
    setToast({ kind: "dependency-removed", plan });
  } catch (e) {
    setToast({
      kind: "error",
      title: "Couldn't remove dependency",
      message: e instanceof Error ? e.message : "Unknown error",
    });
  }
}

function handleUndoToast() {
  if (!toast || toast.kind === "error") return;
  if (toast.kind === "moved-to-tray") {
    if (toast.plan.updates.length > 0) void applyEdgeStrip(routerClient, invertPlan(toast.plan));
    if (toast.previousPosition) {
      const restored = toast.previousPosition;
      setPositions((prev) => ({ ...prev, [toast.taskId]: restored }));
    }
  } else if (toast.kind === "dependency-removed") {
    void applyEdgeStrip(routerClient, invertPlan(toast.plan));
  }
  setToast(null);
}
```

Render: tone `"bad"` for `kind === "error"` (description = `toast.message`,
no Undo action), tone `"neutral"` otherwise (Undo action wired to
`handleUndoToast`) — title switches on `kind` (`"Moved to the unsequenced
tray"` / `"Dependency removed"` / `toast.title`).

Pass `cycleNodeIds`, `onConnect={handleConnect}`, and
`onDeleteEdges={handleDeleteEdges}` down to `<GraphCanvas>` alongside the
existing props.

## 7. Test plan → acceptance criteria

- `edgeOps.test.ts`: `planEdgeCreation` (add, self-loop→null, dup→null),
  `planEdgesRemoval` (single, multi, dangling-id-skip).
- `highlight.test.ts`: diamond fixture (A→B, A→C, B→D, C→D) — assert the
  set for A, D, and B individually; assert a disjoint second component's
  ids never appear for a component-1 selection.
- `TaskGraphView.dragFlow.test.tsx` (or a new sibling test file) — follow
  the existing pattern of mocking `GraphCanvas` and calling its captured
  props directly, same as the drag-out tests already do:
  - `onConnect("tsk_a", "tsk_b")` → `updateTask` called once with
    `tsk_b`'s full object plus `tsk_a` appended to `dependencies`.
  - `onConnect` when `updateTask` rejects with `RouterValidationError` →
    error toast renders with that message (this is the "cross-session
    refusal surfaces as a toast" criterion — see §0, mock the rejection,
    don't try to construct a real cross-session pair).
  - `onDeleteEdges([{from,to}])` → `updateTask` called with the pruned
    array, "Dependency removed" toast with Undo renders, clicking Undo
    calls `updateTask` again with the original array restored (mirror the
    existing drag-out undo test's structure exactly, including the
    `client.taskEventSource.dispatch({type:"task.updated",...})` step
    before asserting the graph reflects it).
  - A manufactured cycle (A→B→A via two `updateTask` calls / two-element
    `dependencies`) → both `A` and `B` render with the cycle-ring class,
    the banner text is present; after breaking the cycle (dispatch a
    `task.updated` with the fixed `dependencies`), both clear.
- A `GraphCanvas.test.tsx` addition or new file for the highlight
  dimming, using the real (non-mocked) `GraphCanvas` +
  `installXyflowMeasurements()` like `GraphCanvas.test.tsx` already does:
  click a node in a diamond fixture, assert dimmed vs. non-dimmed class
  membership matches `computeHighlightSet`'s output; click background
  (`.react-flow__pane`) or press Escape, assert dimming clears.

## 8. Non-goals / things not to build

- No new backend routes, no new `RouterClient` methods — everything routes
  through the existing `updateTask`.
- No optimistic local edge state in `GraphCanvas` — `edges` stays a pure
  derivation from the `canvasNodes`/`edges` props every render, same as
  today. The existing drag-out flow already establishes the precedent
  that edge changes wait for the server round-trip + streamed refetch
  before the canvas visually updates; don't special-case connect/delete
  to feel more "instant" than that, it'd be an inconsistent UX and adds
  state-reconciliation risk for no spec'd benefit.
- Don't build a toast queue. One `GraphToast | null` is enough; a second
  action while one is showing just replaces it (same as today's single
  `undoState`).

## 9. Executor notes (this pass)

A prior executor run was killed mid-task; its work survived on
`backup/tsk_e31500cee2eb36b5` and, identically, as this branch's own
`3bf4016` WIP commit (confirmed via `git diff`, zero output — same tree).
That WIP already implemented everything above almost exactly as specced:
`edgeOps.ts`'s two new plan builders, `highlight.ts`, the `GraphCanvas.tsx`
wiring (custom `DepEdge`, `onConnect`/`onEdgesDelete`, highlight state +
Escape/pane-click clearing, `deletable: false`), `TaskGraphView.tsx`'s
`GraphToast` union and handlers, and the full test suite from §7 including
the tray/marker-transition tests (present but uncommitted in the working
tree; now committed).

**One real bug found and fixed**: `GraphCanvasInner`'s `cycleNodeIds`
prop had a `= new Set()` default-parameter value. Default parameters are
evaluated fresh on every function call, so any render where the caller
omits `cycleNodeIds` (e.g. `GraphCanvas.test.tsx`, which never passes it)
got a new `Set` identity every render. That Set sits in the `useEffect`
dependency array that calls `setNodes`, so the effect fired every render,
which triggered another render, forever — an infinite loop invisible from
`TaskGraphView`'s own tests (which always pass a real `cycleNodeIds`, or
mock `GraphCanvas` out entirely) but fatal for anything mounting the real
`GraphCanvas` without that prop. This is what actually caused the reported
`make build && make test` OOM: `GraphCanvas.test.tsx`'s tests spun one
worker at 100% CPU and climbing RSS until the whole `vitest run` process
hit Node's heap ceiling. Fixed with a module-level `EMPTY_CYCLE_IDS`
constant used as the default instead, so the reference is stable across
renders. Reviewer: worth double-checking no other optional prop in this
file (or `TaskGraphView.tsx`) has the same `= <object literal>` default
pattern feeding a dependency array — this class of bug is easy to
reintroduce.

Also added a short README.md paragraph for the create/remove/cycle/
highlight behavior (task 1's paragraph only covered tray/positions/cycles-
display, not editing).

`make check` and `make ui-smoke` are both green after the fix.
