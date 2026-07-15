# ui/

The Electron + TypeScript app shell and design system (docs/MADS-WORK-ITEMS.md
W6.1). This package owns the token system, type rules, and reusable component
kit every future surface composes from, plus the Electron scaffold that hosts
them. It does not do per-platform packaging (electron-builder); that's
later work, not gated by this package.

Sessions (W6.2) is the first registered surface: session list rail + Launchpad
(hero composer, metadata pills, suggestion chips, provisioning strip). See
`src/features/sessions/`. Every future surface (Retro, Library, etc.) follows
the same pattern: one entry in `App.tsx`'s `SURFACES` map
(`{ id: Component }`), registered against `IconRail`'s existing item ids —
that map is the single app-shell registration point, kept to a one-line diff
per surface so concurrent surface work doesn't collide on a shared file.

The package also owns the Files tab & merge-back flow (W6.5; MADS-UX
§3.3/§5/§17 — see `src/files/` and the `files` demo mount below, not yet an
entry in `SURFACES`), the Conversation tab (W6.3; MADS-UX §3.3), the Retro
surface (W6.7; MADS-UX §3.4 — see `src/features/retro/`, registered as
`SURFACES.retro`, no demo mount needed) and `src/router/` — the Router HTTP
layer, which currently carries two idioms that landed concurrently: the
`RouterClient` interface + `FetchRouterClient` (Conversation) and the thin
`routerGet`/`routerPost` helpers (Sessions, Retro). Unifying them is
follow-up work. `ConversationTab` is not yet an entry in `SURFACES`; it's
reachable standalone via the `conversation` demo mount below.

Sources of truth for the design system: `docs/MADS-UX.md` §2–3 and
`docs/palette-mockups.html` (the approved source for all three shipping
palettes — `clay`/`space`/`glacier` — across both themes; see
`src/tokens/tokens.ts` for how they're pinned). `docs/ux-prototype.html`
remains correct for clay-dark's original pre-review baseline but is
superseded by the mockup for the approved review values.

Tokens vary along two independent dimensions on the root element:
`[data-theme]` (`dark`/`light`) and `[data-palette]` (`clay`/`space`/
`glacier`). `clay` is the default palette when `[data-palette]` is absent.

## Run it alone

```sh
npm ci
npm run dev     # renderer dev server (Vite), open the printed localhost URL
npm start       # build + launch the Electron shell (placeholder content only)
```

The app shell renders an icon rail plus whichever surface `App.tsx`'s
`SURFACES` map resolves for the active rail item — Sessions and Retro today,
a teaching empty state for any rail item without a registered surface yet
(Library).

## Gallery

```sh
npm run gallery
```

Opens `gallery.html` via the Vite dev server: every component in the kit,
rendered across all 6 palette×theme combinations (`clay`/`space`/`glacier` ×
`dark`/`light`), grouped by component name. A palette/theme switcher above
the sections lets you filter/highlight a specific combination without
unmounting the others — all 6 stay rendered so the full audit view is always
available. The sections also exercise the kit's interaction states: a
hoverable card, a selected card, an edit-in-place card (`:focus-within`), an
interactive/pressed chip, and a dismissable toast. This is the review
artifact a human signs off on — all 6 combinations are approved, sourced from
`docs/palette-mockups.html`, not a first pass.

## Conversation tab

```sh
npm run conversation
```

Opens `conversation.html`: the Conversation tab (`src/conversation/
ConversationTab.tsx`) mounted standalone against `FakeRouterClient` fixtures
(`src/router/fixtures.ts`), with a picker to switch between an active and an
inactive (summary-card) session, and a toy in-memory pty so terminal mode and
the Shell dock are reviewable without Electron. This is the review artifact
until W6.2's tab registry exists to register `ConversationTab` into properly.

Terminal mode and the docked Shell use `@xterm/xterm` over a `PtyBridge`
(`src/conversation/TerminalRenderer/ptyBridge.ts`). The real pty transport
(`node-pty`) is a native addon isolated to the Electron main process
(`electron/pty/ptyHost.ts`, dynamically imported in a try/catch, exposed to
the renderer only via `window.mads.pty` in `electron/preload.ts`) —
`src/**` never imports it, even transitively, so `npm run test` needs no
display or native build. Its real-pty contract test is named
`ptyHost.realtest.ts` (not `*.test.ts`) so it's excluded from `npm run test`;
run it manually with `npm run test:pty` on a machine with a usable pty — it
skips cleanly (not a failure) wherever one isn't available.

## Tasks tab & Context Rail

```sh
npm run tasks
```

Opens `tasks.html`: `TasksTab` (`src/tasks/TasksTab.tsx`) and `ContextRail`
(`src/context-rail/ContextRail.tsx`) mounted side by side against
`FakeRouterClient` fixtures, standing in for the Session Workspace container
that will eventually host both. `TasksTab` owns list/graph toggle, the detail
sheet, draft confirm/discard, and bulk import; `ContextRail` owns the
Tasks/Changes rail tabs and quick-filter chips. This is the review artifact
until W6.2's tab registry exists to wire `TasksTab` into `App.tsx` properly.

Both `TasksTab` and `ContextRail` read live task state via `useTaskStore`
(`src/tasks/useTaskStore.ts`), which subscribes to `task.*`/`note.*` events
off `GET /events` and always refetches the affected task rather than trusting
the (sparse) event payload. `TasksTab` owns its own instance per the module
layout in `src/tasks/HANDOFF.md` §4; `ContextRail` instead takes
`tasks`/`tasksById` as props, so the demo mount runs a second instance to
supply the rail. Against `FakeRouterClient` this is free (`streamTaskEvents`
always returns one shared fake event source), but a real container mounting
both tabs together should lift a single `useTaskStore` call and pass the
result into each instead of opening two live subscriptions for one session —
see `src/tasks/HANDOFF.md`'s executor notes.

Task list/graph rendering follows the Queued display rule (`src/tasks/
ListView/statusPresentation.ts`): a `"blocked"` task with an unfinished
dependency renders calm/neutral ("Queued"), while `"blocked"` with no
unfinished dependency (needs a human) renders red — the schema has no
separate status for the two, so this is a client-side heuristic. The
dependency graph (`src/tasks/GraphView/`) lays out with `@dagrejs/dagre`
deterministically (nodes sorted by id, dimensions from a title-length
heuristic, never measured from the DOM) and highlights cycles from the
server-authoritative `GET /tasks/cycles`, not client-side detection.

## Files tab

```sh
npm run files
```

Opens `files.html`: the Files tab (`src/files/FilesTab.tsx`) mounted
standalone against `FakeRouterClient` fixtures (`src/router/fixtures.ts`),
with a picker to switch between a clean-changes session, a session with an
unattributed observation row, and a session with a seeded merge conflict.
This is the review artifact until W6.2's tab registry exists to register
`FilesTab` into properly.

`FilesTab` composes a change list (`ChangeList`) and commit list
(`CommitList`) with attribution chips (trailer/session/heuristic/orphan/
unattributed — `src/files/ChangeList/attribution.ts`), a `DiffViewer`, and
the `MergeBackFlow`. The diff viewer and the seed-only embedded merge editor
both lazy-load `@monaco-editor/react` (`React.lazy` + `Suspense`) so Monaco
never lands in the initial bundle and never has to mount under happy-dom in
tests — component tests mock `@monaco-editor/react` instead of loading real
Monaco (see `DiffViewer.test.tsx`/`ConflictResolution.test.tsx`).

The merge-back flow's status (attempting/conflicted/completed) is driven
entirely by parsed `merge.*` control-plane events from
`RouterClient.streamControlEvents`, not by the `mergeSession()` promise
result — `MergeBackFlow.test.tsx` drives this against a fake, in-memory
control-event source (`FakeControlEventSource.emit(type, envelope)`).
Conflict resolution has one real, usable action (open the session's
worktree in an external editor) and one seed-only stub (`MergeEditor`, a
raw-text Monaco instance over a fixture conflict-marker string) — there is
currently no backend path that leaves real conflict-marker content in the
tree to point an embedded editor at, since `Manager.Merge` aborts the merge
on conflict. See `src/files/HANDOFF.md` §5 for the full reasoning and the
follow-up this needs on the Session Service side.

## Retro surface

`src/features/retro/RetroSurface.tsx` (W6.7; MADS-UX §3.4), registered as
`SURFACES.retro` — no demo mount, since its own component tests (stubbed
`fetch` + `FakeEventSource`) already cover it standalone. It owns three
phases: picking (session checkboxes with an All/Work/Retro-generated kind
filter, an observations textarea), generating (a 3-step Starting/Gathering/
Generating indicator driven by `retro.*` SSE events, cancellable back to
picking), and reviewing (DPA cards whose status chip doubles as the accept/
reject toggle, plus a "Create follow-up session (N accepted)" CTA and a
history list of past retros).

Unlike Conversation/Files, this surface talks to the Router directly through
the thin `routerGet`/`routerPost` + `subscribeToEvents` idiom, not
`RouterClient`/`FetchRouterClient`. `retro.*` events aren't session-scoped
(`Envelope.session_id` is empty) — the surface subscribes globally and
correlates events by `payload.retro_id` instead. Submodules: `picker/`
(checkboxes, kind filter, chip-strip recap), `generation/` (the step
indicator), `dpa/` (DPA cards), `history/` (the past-retros list), `state/`
(the job-lifecycle and data-fetching hooks). See
`src/features/retro/HANDOFF.md` for the full design.

## Pipeline tab

```sh
npm run pipeline
```

Opens `pipeline.html`: the Pipeline tab (`src/pipeline/PipelineTab.tsx`)
mounted standalone against `FakePipelineClient` and a recorded engine-run
event sequence (`src/pipeline/client/recordedRun.ts`, captured from a real
engine run against provider-fake — three tasks, including a stage loop-back
and a terminal block). A global `EventSource` stub feeds the recording to
`usePipelineRun` in place of a real Router SSE connection; a "Play recorded
sequence" button replays it on demand. This is the review artifact until
W6.2's tab registry exists to register `PipelineTab` into properly.

`PipelineTab` composes a `RunHeader` (Start/Pause/Resume/Cancel, elapsed +
task counts — Pause/Resume/Reassign render disabled, since the engine has no
API for them yet; no live cost, by design, see MADS-UX §8), a `StageBoard`
(one column per stage in first-seen order, cards move on `stage.*` events,
loop-backs show an attempt badge), a `BlockedPanel` (blocked tasks surfaced
separately from the board with their accumulated feedback/blocker notes),
and a `TaskDrillIn` split panel (stage timeline with attempts/loop-backs/
feedback, a live agent output stream, and touched-file count) opened by
selecting a task. All state is folded from `pipeline.*`/`stage.*`/
`task.*`/`note.*`/`file.changed` envelopes by `usePipelineRun`
(`src/pipeline/state/usePipelineRun.ts`); retry/reassign actions go through
`PipelineClient`, which wraps the Router the same way `RouterClient` does
for Conversation.

The drill-in's terminal reuses `TerminalView` over a `PtyBridge` adapter
(`src/pipeline/DrillIn/pipelineOutputBridge.ts`) that turns a read-only
`PipelineOutputSource` (agent output chunks, no input channel) into the
interface `TerminalView` expects — `write`/`resize` are no-ops, matching the
one-shot, non-interactive nature of agent stage output.

## Command palette & notifications

`src/features/command-palette/` (W6.11; MADS-UX §3.8), mounted directly in
`App.tsx` (not a `SURFACES` entry — it's a global overlay, toggled by ⌘K/
Ctrl+K rather than the icon rail). `useCommandPaletteData` composes the
already-mounted `useSessionsList`/`useOpenTaskCounts` hooks plus a new
`useAllTasks`, all fetched once on mount/SSE event at the `App.tsx` level;
`CommandPalette.tsx` itself only ever receives this data as props and
filters it client-side (`commandSource.ts`), so opening the palette issues
no new Router request. Dispatching a command (jump to session, start
pipeline, open task, new session, run retro) is handled in `App.tsx`.

The dock/tray badge count reuses `dotState` (`src/features/sessions/rail/
railGroups.ts`) — the same "needs attention" derivation the session rail
uses — rather than a second implementation, via
`state/attentionCount.ts`/`state/useAttentionBadge.ts`. It's pushed to
Electron over a `badge:set-count` IPC channel (`electron/preload.ts` /
`electron/main.ts`, wrapping `app.setBadgeCount`), reachable from the
renderer via `window.mads.badge`, the same `contextBridge` pattern as
`window.mads.pty`. The notification model is badge-only at launch; no
in-app inbox exists yet, but `Command`'s shape in `commandSource.ts` is
meant to be reusable as a future inbox's item source rather than needing a
new backend feed.

## Test it

```sh
npm run lint     # eslint (typescript-eslint + eslint-plugin-react-hooks)
npm run test     # vitest: tokens, contrast, every component
npm run test:pty # manual-only: real node-pty round-trip, see above
```

`src/tokens/tokens.test.ts` pins every hex/rgba value for all 6 palette×theme
combinations to `docs/palette-mockups.html` as a regression guard, asserts
`tokens.css` and `tokens.ts` stay byte-identical across all 6, and runs a
WCAG AA contrast check (relative luminance, no dependency) over every
documented fg/bg pair in each combination. Every component has a
`*.test.tsx` alongside it (component-level, `@testing-library/react` +
happy-dom by default). Tests that render sanitized markdown (`Markdown` /
`ChatRenderer` / `SessionSummaryCard` / `ConversationTab`) opt into jsdom per
file via a `// @vitest-environment jsdom` pragma instead — DOMPurify doesn't
sanitize correctly under happy-dom (see
`src/conversation/ChatRenderer/markdown.test.tsx` for the specifics); the
real Electron/Chromium renderer is unaffected.

By design, this package has no Playwright/E2E/visual-regression tooling —
that's a later wave, developed separately once real surfaces exist to test
against.

## Router client (`src/router/`)

All data reaches the UI through the Router's HTTP/SSE API — never the Data
Service directly. `src/router/client.ts` exports `routerGet<T>(path, params?)`
and `routerPost<T>(path, body?)`; `src/router/events.ts` exports
`subscribeToEvents({ sessionId?, types }, onEvent): () => void`, a thin
wrapper over `EventSource` against `GET /events`. The server always names the
SSE `event:` field after the envelope's own type (never a generic `message`
event), so callers must pass the explicit `types` they want to receive.
`src/router/types.ts` hand-mirrors the `pkg/contracts` wire shapes actually
consumed by the UI (snake_case fields, matching the JSON on the wire — no
camelCase translation layer). Like the component kit and `tokens.css`, this
is a shared seam future surfaces should compose, not reimplement.

For tests, `src/router/testFakeEventSource.ts` exports `FakeEventSource`, a
scripted stand-in for the browser `EventSource` (`vi.stubGlobal("EventSource",
FakeEventSource)`, then `FakeEventSource.instances[0].emit(type, jsonData)`
to play back a recorded SSE frame). `src/features/sessions/` tests that
exercise provisioning/live-list behavior use this instead of a real Router —
follow that pattern rather than adding a second fake.

## Feature flags

`src/featureFlags.ts` is a temporary hardcoded stub (`export const FLAGS = {
multiRepo: false }`) standing in for a real `/config` endpoint, which doesn't
exist yet. Gate any flagged UI behind `FLAGS.<name>`; swap the stub for a
real fetch once the backend adds one, rather than threading config through
each surface individually.

## Critical paths

- `src/tokens/tokens.css` is the only place colors are declared as literal
  hex/rgba values. Components must read `var(--token-name)`, never hardcode a
  color — `src/tokens/tokens.test.ts` guards all 6 palette×theme values
  against drift from the approved source, and `src/tokens/tokens.ts` must
  stay byte-identical to `tokens.css` (same test file).
- `src/components/Draft/Draft.tsx` (`DraftChip` + `Draft`) is the one
  provenance treatment every future surface must reuse for synthesis cards,
  generated agents, and DPA drafts — don't reimplement the border/badge
  pattern elsewhere.
- `src/components/Popover/Popover.tsx` is the one controlled popover
  primitive (open/onClose/anchor) — metadata pills and any future
  dropdown/menu UI should compose it rather than rolling a new
  positioning/outside-click implementation. The caller owns its own
  open/closed `useState`; `Popover` itself holds no state.
- `make build`/`make test` from the repo root run this package's build/lint/
  test as part of the standard gates (see root `Makefile`'s `ui-deps` target,
  which runs `npm ci`) — a fresh worktree needs no manual steps beyond that.
- `node-pty` is an `optionalDependency`, dynamically `import()`-ed only from
  `electron/pty/ptyHost.ts` (main process) inside a try/catch. Nothing under
  `src/**` may import it, even transitively — that's what keeps `npm run
  test` (and `make test`) safe to run on a machine with no display or native
  build toolchain.
