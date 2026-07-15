# HANDOFF: W6.11 Command palette & notifications

Architect stage output. Executor: read this in full before writing any code.
Design-only — nothing in this directory exists yet.

## §0 Coordination risk

Low. This module only *reads* existing state (sessions, tasks) via the
already-established hook pattern and *calls* one existing mutation
(`startPipeline`). It adds one new Electron IPC channel and touches three
existing files (`App.tsx`, `SessionsSurface.tsx`, `ptyBridge.ts`) with small,
additive diffs. No Go changes, no new Router routes, no schema migrations.

The one file with real collision risk is `ui/electron/main.ts` (bare
40-line scaffold today) — if another in-flight work item is also touching
Electron main-process wiring, diff carefully.

## §1 Backend reality check

Confirmed by direct grep of `services/router`, `services/data`,
`pkg/contracts`, `pkg/dataclient`:

- `GET /sessions?attention=true` (cited in MADS-UX.md §3.2) **does not
  exist**. Do not call it. The attention count and per-session "needs
  attention" flag must be derived client-side from data already fetched by
  `useSessionsList`/`useOpenTaskCounts`, using the same `dotState()` logic
  the session rail already uses (`ui/src/features/sessions/rail/railGroups.ts`).
- There is no tasks-by-multiple-sessions or "all tasks" endpoint beyond
  `GET /tasks` with an optional `session_id` filter. Omitting the filter
  returns all tasks — this is exactly what `useOpenTaskCounts` already
  does; the command palette's task list reuses the same call.
- "§8.5" in the work-item text and in MADS-UX.md's Open Question 5 does
  **not** refer to a MADS-UX.md subsection (that doc only has top-level
  sections 1–6). It points to `docs/REBUILD-SPEC.md` §8 (Observability),
  Requirements item 5: "Passive notification model — the system does not
  alert or interrupt the user. Data is available when the user looks.
  (Active notifications may be added later.)" This resolves the open
  question exactly as the work item says: **badge-only at launch**, no
  toast/alert/sound, with a schema hook (see §6) for a future in-app inbox.
- No Modal primitive exists in the kit (`ui/src/components/`) — confirmed
  via `ui/src/components/index.ts`. Prior work items (tasks, retro)
  deliberately avoided adding one. This module does not add one either
  (see §3/§5).
- "As W6.2" has no literal HANDOFF.md to point to — W6.2 landed as commit
  `14aece9` but shipped without one. Per established precedent (retro,
  tasks), "as W6.2" is read as: thin `routerGet`/`routerPost`/
  `subscribeToEvents` Router idiom (not the `FetchRouterClient` class
  idiom), kit components + tokens only, no Playwright, minimal shell diff,
  vitest component tests, README section addition.

## §2 Module layout

New directory: `ui/src/features/command-palette/`

```
command-palette/
  CommandPalette.tsx              presentational overlay: hotkey listener,
                                   open/close state, filter input, results
                                   list, keyboard nav, dispatch
  CommandPalette.test.tsx
  commandSource.ts                pure: (sessions, tasks) -> Command[]
                                   and filterCommands(commands, query)
  commandSource.test.ts
  state/
    useCommandPaletteData.ts      composition hook: owns useSessionsList,
                                   useOpenTaskCounts, useAllTasks; no new
                                   fetch is issued on palette open, only on
                                   mount (App.tsx level)
    useAllTasks.ts                new hook, modeled directly on
                                   useOpenTaskCounts.ts's fetch, returns
                                   Task[] instead of a reduced count map
    useAllTasks.test.ts
    attentionCount.ts             pure: (sessions, openTaskCounts) -> number,
                                   reuses dotState() from
                                   features/sessions/rail/railGroups.ts
    attentionCount.test.ts
    useAttentionBadge.ts          composition hook: derives the count via
                                   attentionCount and pushes it to the
                                   Electron badge bridge on change
  badgeBridge.ts                  window.mads?.badge accessor + BadgeBridge
                                   type (mirrors ptyBridge.ts's pattern)
  README.md excerpt               appended to ui/README.md, not a separate
                                   file (see §12)
```

No new files under `ui/src/router/` — `Session` and `Task` types already
exist in `ui/src/router/types.ts` and are sufficient; no new wire types are
needed for the palette itself.

## §3 Data flow: "no new fetch on open"

`useCommandPaletteData()` is mounted **once**, at `App.tsx` level, alongside
the rail — not inside `CommandPalette.tsx`, and not conditionally on
`open`. It composes:

- `useSessionsList()` (existing, unmodified) → `sessions`
- `useOpenTaskCounts()` (existing, unmodified) → `openTaskCounts` (for the
  badge)
- `useAllTasks()` (new) → `tasks` (for the palette's task search)

All three fetch on mount and refetch on their respective SSE event sets,
exactly like every other surface in this codebase. `CommandPalette.tsx`
receives `sessions`/`tasks` as **props** and is purely presentational: no
`routerGet` call, no `useEffect` fetch, anywhere in the component. Opening
the palette (⌘K) only toggles local `open` state and focuses the filter
input — it is provably free of new network activity, which is what the
acceptance criterion ("opens over cached data") actually requires.

`useAllTasks.ts` — copy the fetch/subscribe shape from
`useOpenTaskCounts.ts` verbatim (same `GET /tasks` call, same
`TASK_EVENT_TYPES` array, same refetch-on-any-matching-event pattern) but
return the raw `Task[]` instead of reducing to counts. Do not have
`useAllTasks` and `useOpenTaskCounts` share one subscription internally —
this repo's precedent (retro HANDOFF §11) treats this kind of duplication
as deliberate: cross-module state sharing has caused more confusion than
double-subscribing a cheap SSE stream.

## §4 `commandSource.ts` — building and filtering the command list

Two pure functions, both unit-testable without React:

```ts
export type CommandKind =
  | "jump-session"
  | "start-pipeline"
  | "open-task"
  | "new-session"
  | "run-retro"
  | "toggle-shell"
  | "open-settings";

export interface Command {
  id: string;            // stable key, e.g. "jump:session-1"
  kind: CommandKind;
  label: string;
  detail?: string;       // e.g. session workspace, task status
  sessionId?: string;
  taskId?: string;
}

export function buildCommands(sessions: Session[], tasks: Task[]): Command[];
export function filterCommands(commands: Command[], query: string): Command[];
```

**Design decision — per-session action pairing.** The palette has no
"current session" concept (the shell's own navigation is a flat
`useState` in `App.tsx`, not a router with an active-session URL param —
see §1). "Start pipeline" requires a `sessionId`. Resolved by generating
one `jump-session` command **and** one `start-pipeline` command per
session in `buildCommands`, e.g.:

- `"Jump to · {session.name}"` (kind `jump-session`)
- `"Start pipeline · {session.name}"` (kind `start-pipeline`)

This directly satisfies "jump-to-session and start-pipeline dispatch
correctly" as two independently selectable, independently testable
commands, without inventing a selection/context step inside the palette
itself.

`open-task` commands are generated one per task, labeled with the task
title and session name (`task.title` + look up `session.name` by
`task.session_id`) so results are identifiable without a session filter
UI. Dispatch opens the session (jump) — there is no task detail route
independent of a session surface today (confirmed: `SessionsSurface` owns
task display, no standalone task deep link exists) — so `open-task`
dispatch is implemented as jump-to-session; do not build a second sheet.

Static commands (`new-session`, `run-retro`, `toggle-shell`,
`open-settings`) are fixed entries appended by `buildCommands`, not
derived from data.

`filterCommands` does a simple case-insensitive substring match against
`label` (+ `detail` if present). No fuzzy scoring library — keep it
`String.includes`, consistent with this repo's "no new dependency for a
first pass" bias. Group ordering in results: sessions first, then tasks,
then actions, each group's relative order stable (input order preserved,
`Array.prototype.filter`).

## §5 `CommandPalette.tsx` — presentational overlay

Props:

```ts
interface CommandPaletteProps {
  open: boolean;
  onClose: () => void;
  commands: Command[];
  onDispatch: (command: Command) => void;
}
```

Behavior:
- Global ⌘K / Ctrl+K listener lives in the **parent** (`App.tsx`), not
  inside this component — the component only needs to know it's `open`.
  Keeping the hotkey registration outside makes `open` state trivially
  testable (`render(<CommandPalette open={true} .../>)`, no
  `fireEvent.keyDown` needed for most tests) and matches how `Popover`
  separates "am I open" from "what opens me."
- While `open`, render a full-viewport scrim `<div>` (fixed position, kit
  token background, `onClick={onClose}`) with a centered `Panel`
  containing a filter `<input>` (autofocused) and the filtered results
  list.
- `Escape` and outside-click both call `onClose` — mirror
  `Popover.tsx`'s existing `mousedown`/`Escape` document-listener
  `useEffect` pattern (same cleanup shape), but implemented locally in
  this component rather than by literally reusing `Popover`, since
  `Popover` requires an `anchor` prop that a full-screen palette doesn't
  have. **Do not add an `anchor`-less mode to `Popover` itself** — that
  widens a shared primitive for one caller, which is exactly what the
  tasks/retro precedent avoided doing with Modal.
- Arrow Up/Down move a `selectedIndex` through the *currently filtered*
  list (clamped, wraps or clamps — clamping is simpler and fine); Enter
  dispatches the selected command; typing updates the filter query and
  resets `selectedIndex` to 0.
- Dispatching a command calls `onDispatch(command)` and then `onClose()`.
  `CommandPalette` does not know what dispatch *does* — that's App.tsx's
  job (§7). This keeps the component's own tests entirely about
  filtering/keyboard nav/dispatch-call-shape, not side effects.
- No new kit primitive. Reuses `Panel`/`PanelHeader`/`PanelBody` for
  chrome, existing input/text tokens for the filter field, existing
  spacing/elevation tokens for the scrim. If `Panel`'s current styling
  doesn't support a "floating over everything" elevation, that's a token
  usage detail for the executor to resolve within the existing token set
  — do not add a new elevation token without checking `ui/src/tokens/`
  first for one that already fits.

## §6 Badge: `attentionCount.ts` + `useAttentionBadge.ts` + Electron bridge

**Design decision — reuse `dotState`, not a new definition.**
`ui/src/features/sessions/rail/railGroups.ts` already exports
`dotState(session, openTaskCount): "on" | "attn" | "off"`, the canonical
"needs attention" signal (the amber dot in the rail). `attentionCount.ts`
imports it directly:

```ts
import { dotState } from "../../sessions/rail/railGroups";
import type { Session } from "../../../router/types";

export function attentionCount(
  sessions: Session[],
  openTaskCounts: Record<string, number>,
): number {
  return sessions.filter(
    (s) => dotState(s, openTaskCounts[s.id] ?? 0) === "attn",
  ).length;
}
```

This is a deliberate, narrow exception to the "no sibling feature module
imports another's *state*" precedent (retro HANDOFF §11): that precedent
exists to prevent duplicate live subscriptions, not to prevent reusing a
pure, side-effect-free function. Redefining "needs attention" independently
here would let the badge and the rail's amber dot silently drift apart —
worse than the cross-module import.

`useAttentionBadge(sessions, openTaskCounts)` is a small composition hook
that computes `attentionCount(...)` (memoized) and calls the badge bridge
in a `useEffect` keyed on the count:

```ts
useEffect(() => {
  getBadgeBridge()?.setCount(count);
}, [count]);
```

### Electron bridge

`badgeBridge.ts` (new, in this module) mirrors
`ui/src/conversation/TerminalRenderer/ptyBridge.ts`'s accessor pattern:

```ts
export interface BadgeBridge {
  setCount(count: number): void;
}

export function getBadgeBridge(): BadgeBridge | undefined {
  return typeof window !== "undefined" ? window.mads?.badge : undefined;
}
```

**Required cross-module diff — read carefully.** `ptyBridge.ts` already
has:

```ts
declare global {
  interface Window {
    mads?: {
      pty?: PtyBridge;
    };
  }
}
```

TypeScript's declaration merging requires every `declare global` block
that augments `Window.mads` to agree on its shape — two separate files
each declaring a *different* inline type for `mads` is a compile error
(TS2717, "Subsequent property declarations must have the same type"),
not a silent merge. **Do not add a second `declare global { interface
Window { mads?: {...} } }` block in `badgeBridge.ts`.** Instead, extend
the *existing* block in `ptyBridge.ts` by one line:

```ts
declare global {
  interface Window {
    mads?: {
      pty?: PtyBridge;
      badge?: { setCount(count: number): void };
    };
  }
}
```

This is the one required edit to a file outside `features/command-palette/`
besides `App.tsx` and `SessionsSurface.tsx`. Keep the inline badge shape
here structurally identical to `BadgeBridge` in `badgeBridge.ts` (or just
inline it in both places — they don't need to literally share a type,
only agree structurally).

### `preload.ts` / `main.ts`

`preload.ts` — add a `badge` namespace next to the existing `pty` one,
exposed via the same `contextBridge.exposeInMainWorld("mads", { pty,
badge })`:

```ts
const badge = {
  setCount: (count: number) => ipcRenderer.invoke("badge:set-count", count),
};
```

`main.ts` — add one handler:

```ts
ipcMain.handle("badge:set-count", (_event, count: number) => {
  app.setBadgeCount(count);
});
```

`app.setBadgeCount` is a no-op returning `false` on Windows (no throw) —
no platform branching needed. No Tray icon work is in scope (MADS-UX §3.8
and the work item both say "dock/tray badge," but no existing Tray
instantiation exists anywhere in `ui/electron/`, and adding one is a much
larger, unscoped change — see §10).

No test coverage is expected for `main.ts`/`preload.ts` changes — this
repo has no unit-test convention for Electron main-process code
(`vitest.config.ts` only includes `src/**/*.test.{ts,tsx}`; the sole
electron test precedent, `ptyHost.realtest.ts`, is manual-only and
excluded from `make check` via `vitest.electron.config.ts`). Cover the
*logic* (`attentionCount`, `useAttentionBadge`'s effect triggering) in
`src/`, not the IPC plumbing itself.

## §7 Schema hook for future in-app inbox (passive notification model)

The work item requires "schema hooks for a future in-app inbox" without
building the inbox. Scope this narrowly: `Command` (§4) already has a
`kind` field general enough to extend later. Add one comment, not code, at
the top of `commandSource.ts`:

```ts
// Notification model: badge-only at launch (REBUILD-SPEC.md §8 req. 5 —
// passive, no interrupt). A future in-app inbox would likely reuse
// `attentionCount`'s per-session derivation as its item source rather
// than a new backend feed; no backend schema changes are anticipated.
```

Do not add an unused `Notification` type, an empty inbox component, or a
feature flag — those would be exactly the "half-finished implementation"
this repo's conventions warn against. The "hook" is the fact that
`attentionCount`/`dotState` already compute per-session attention
independently of the badge, so a future inbox has a ready data source to
read from — that's a design note, not new code.

## §8 App.tsx and SessionsSurface.tsx wiring

`App.tsx` diff (small, additive):

```tsx
const { sessions, tasks, openTaskCounts } = useCommandPaletteData();
const [paletteOpen, setPaletteOpen] = useState(false);
const [jumpToSessionId, setJumpToSessionId] = useState<string>();

useAttentionBadge(sessions, openTaskCounts);

useEffect(() => {
  const handler = (e: KeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "k") {
      e.preventDefault();
      setPaletteOpen((v) => !v);
    }
  };
  document.addEventListener("keydown", handler);
  return () => document.removeEventListener("keydown", handler);
}, []);

const commands = useMemo(() => buildCommands(sessions, tasks), [sessions, tasks]);

const handleDispatch = (command: Command) => {
  switch (command.kind) {
    case "jump-session":
    case "open-task":
      setActiveId("sessions");
      setJumpToSessionId(command.sessionId);
      break;
    case "start-pipeline":
      if (command.sessionId) pipelineClient.startPipeline(command.sessionId, "default-implementation");
      break;
    case "new-session":
      setActiveId("sessions"); // real create flow already lives in SessionsSurface; this just navigates there
      break;
    case "run-retro":
      setActiveId("retro");
      break;
    case "toggle-shell":
    case "open-settings":
      break; // no-op: no shell-toggle or settings surface exists yet (§10)
  }
};
```

`<CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} commands={commands} onDispatch={handleDispatch} />`
is rendered as a sibling of `<IconRail>`/`<Surface>`, not inside the
`SURFACES` map — it's a global overlay, not a rail destination.

**`SessionsSurface.tsx` diff** — add an optional prop so "jump to session"
is a real, observable effect rather than a stub:

```tsx
interface SessionsSurfaceProps {
  jumpToSessionId?: string;
}

useEffect(() => {
  if (jumpToSessionId) setSelectedId(jumpToSessionId);
}, [jumpToSessionId]);
```

Pass `jumpToSessionId` through from `App.tsx` when rendering the
`sessions` surface. This is the one behavior-bearing diff to an existing
feature module, justified because "jump-to-session... dispatch correctly"
is an explicit acceptance criterion — a palette that only changes rail
tabs without actually selecting the session would not satisfy it.

`new-session` and `run-retro` dispatch to real, already-existing flows
(session creation lives inside `SessionsSurface`'s own UI; retro is a rail
surface) — the palette only needs to navigate there, it does not
duplicate the creation form.

## §9 Test-to-acceptance-criteria map

| Acceptance criterion | Test(s) |
|---|---|
| ⌘K opens over cached data (no new fetch on open) | `CommandPalette.test.tsx`: render with `open` toggling from `false`→`true` against a `FakeRouterClient` that throws/counts calls on any fetch after initial mount; assert no additional call. Also: `App.test.tsx` (or wherever shell wiring is tested) asserts `useSessionsList`/`useAllTasks` fetch exactly once regardless of palette open state. |
| jump-to-session dispatches correctly | `commandSource.test.ts`: `buildCommands` emits one `jump-session` Command per session with correct `sessionId`. `CommandPalette.test.tsx`: selecting a jump command calls `onDispatch` with that command. Integration: a `SessionsSurface.test.tsx` case asserts `jumpToSessionId` prop changes `selectedId`. |
| start-pipeline dispatches correctly | `commandSource.test.ts`: one `start-pipeline` Command per session. App-level wiring test: `handleDispatch({kind:"start-pipeline", sessionId})` calls `pipelineClient.startPipeline(sessionId, "default-implementation")` against a fake `PipelineClient`. |
| badge tracks attention count from events | `attentionCount.test.ts`: pure function table tests against `fixtureSessions`/task-count maps, covering `"attn"` vs `"on"`/`"off"`. `useAttentionBadge` test (via a rendered hook or thin wrapper component) with a `FakeEventSource`: emit a `task.status_changed` event, assert the fake badge bridge's `setCount` is called with the updated count. |
| filtering | `commandSource.test.ts`: `filterCommands` substring match, case-insensitive, empty query returns all. |
| keyboard nav | `CommandPalette.test.tsx`: `fireEvent.keyDown` ArrowDown/ArrowUp/Enter/Escape sequences using `fixtureSessions`/`fixtureTasks`-derived commands. |
| ⌘K hotkey opens palette | App-level test: `fireEvent.keyDown(document, {key: "k", metaKey: true})` toggles palette open. |

All fixture data comes from `ui/src/router/fixtures.ts`
(`fixtureSessions`, `fixtureTasks`) — no new fixtures needed unless an
edge case (e.g. a session with zero open tasks) isn't already represented,
in which case extend fixtures.ts minimally rather than inlining ad hoc
objects in each test.

## §10 Non-goals (explicitly out of scope)

- **Tray icon.** No `Tray` instantiation exists anywhere in
  `ui/electron/` today; MADS-UX/work-item wording says "dock/tray" but
  building a Linux/Windows tray icon + context menu is a materially larger,
  unscoped change. Ship the dock badge (`app.setBadgeCount`, cross-platform
  via Electron, no-op on Windows) only.
- **In-app notification inbox.** Explicitly deferred by the work item
  itself ("schema hooks... deferred"). See §7 for what "schema hooks"
  concretely means here — a design note, not a component.
- **`toggle-shell` / `open-settings` commands.** Listed in MADS-UX §3.8's
  action set, but no shell-toggle mechanism or settings surface exists
  anywhere in the current UI. Include them in the static command list
  (so the palette's action set matches the spec and future work items can
  wire them without touching `commandSource.ts`), but dispatch is a no-op.
  Do not build a settings surface or shell-toggle to fill this in — out of
  scope for W6.11.
- **Fuzzy search / command scoring library.** Plain substring filter only.
- **Task detail deep link.** `open-task` dispatch reuses jump-to-session;
  no new task detail route.
- **Sound/toast/OS-level notification center integration.** Explicitly
  ruled out by the "passive notification model" resolution (§1, §7).

## §11 Risks / judgment calls for human review

1. **`ptyBridge.ts` edit is required, not optional.** Flagged prominently
   in §6 because it's easy to miss (a new file's own `declare global`
   block looks like it should "just work" and will compile fine in
   isolation, then fail when the whole program is type-checked together).
2. **No Tray icon**, despite "dock/tray badge" wording in both the spec
   and the work item — judgment call to scope to dock-only (§10). Flag to
   the user if tray behavior on Linux/Windows is actually expected at
   launch; it is a separate, larger piece of work.
3. **`open-task` reuses jump-to-session** rather than a distinct
   behavior, since no task-detail surface independent of a session exists.
   If a future task-detail sheet lands, `commandSource.ts`'s `open-task`
   dispatch should be revisited.
4. **`dotState` cross-module import** (§6) is a deliberate, reasoned
   exception to this repo's "no sibling feature imports another's state"
   convention. It imports a *pure function*, not a hook/subscription, so
   the duplicate-subscription concern the convention exists to prevent
   doesn't apply — but flag it explicitly in the PR description so it
   isn't mistaken for an accidental violation.
5. **Default pipeline template id `"default-implementation"`** is a
   literal used elsewhere (`RunHeader.tsx` default prop), not an exported
   constant. If a shared constant gets introduced later, update this
   call site too.

## §12 README

Append a new subsection to `ui/README.md` (existing surfaces each get
one) describing: module location, the "no fetch on open" data-flow
guarantee, the badge derivation reusing `dotState`, and the Electron IPC
channel name (`badge:set-count`) for anyone touching `ui/electron/` later.
Keep it to the same length/style as the existing Retro/Tasks sections —
a few short paragraphs, not a spec restatement.

## §13 make check

No Go changes in this work item — `make check` at the repo root should
pass purely from the UI package's `npm run build`/`npm test`/lint gates.
Run `make check` from repo root as the final step before considering the
work item done.
