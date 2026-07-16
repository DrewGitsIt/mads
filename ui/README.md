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
`SURFACES.retro`, no demo mount needed), Settings & first-run (W6.10;
MADS-UX §3.7/§3.9 — see `src/features/settings/` and `src/onboarding/`,
registered as `SURFACES.settings`, no demo mount needed) and `src/router/`
— the Router HTTP layer, exposed as a single idiom: the `RouterClient`
interface + `FetchRouterClient`/`FakeRouterClient` implementations, used
everywhere via the shared `router` singleton (or an injected `client` prop
in tests), plus `subscribeToEvents` for the SSE event stream. `ConversationTab`
is reached by selecting a session in the Sessions rail (`SessionsSurface`),
which mounts it in the main pane passing the shared `router` singleton and
the Electron PTY bridge when present — it's still not its own `SURFACES`
entry, since `SURFACES` maps `IconRail` items, not in-surface state. The
`conversation` demo mount below still exists standalone (against
`FakeRouterClient` fixtures) for reviewing it in isolation without a live
Router.

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
`SURFACES` map resolves for the active rail item — Sessions, Retro, and
Settings today, a teaching empty state for any rail item without a
registered surface yet (Library). On a never-configured install, `App.tsx`
gates the whole shell behind `FirstRunWizard` until onboarding completes.

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
the Shell dock are reviewable without Electron. In the real app,
`ConversationTab` is mounted by `SessionsSurface` when a session is selected
in the rail (back/deselect returns to the Launchpad); this demo mount remains
useful for reviewing it in isolation without a live Router.

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

## Library

```sh
npm run library
```

Opens `library.html`: `LibrarySurface` (`src/library/LibrarySurface.tsx`, W6.8;
MADS-UX §3.5) mounted standalone against a real Router (`VITE_ROUTER_URL`),
not `FakeRouterClient` — unlike the other demo mounts, `FakeRouterClient`
doesn't carry fixtures for most Library shelf endpoints yet, so there's no
fake client to point it at (`RouterClient`/`subscribeToEvents`,
`src/router/client.ts`/`events.ts`). `LibrarySurface` owns a `Chip`-row shelf
switcher over four shelves — Agents, Capabilities, Workspaces, Pipeline
templates — each following the same shape: a `use<Shelf>.ts` hook
(list+CRUD, refetch-on-any-event via `subscribeToEvents`) feeding a
`<Shelf>Shelf.tsx` card grid, with a `SideSheet`-based `<Item>Editor.tsx` for
create/edit. Browse Registry (`src/library/registry/RegistryShelfStub.tsx`)
stays behind `FLAGS.remoteRegistry` and is a stub until its own work item.

Agents (`src/library/agents/`) adds an NL-generate flow
(`NLGenerateBox`/`useAgentGeneration.ts`) that posts to the W5.3
generation endpoint and renders the result through `Draft`/`DraftChip`
(matching the kit's one provenance treatment, see Critical paths below) with
a `SimilarAgentWarning` when the backend flags a near-duplicate. The editor
composes a prompt scaffold, enrichment actions (describe/suggest-
capabilities/refine), a provider-discovery-backed model dropdown
(`ModelField`), a quadrant tooltip (`QuadrantField`), and an import-from-
provider mapping preview (`ImportFromProviderDialog`).

Capabilities (`src/library/capabilities/`) shows a live health dot per card
(`HealthDot`) that owns its own `capability.health_changed` subscription and
refetches just that one capability — correct even without patch-in-place
semantics, since a full refetch is always safe. `CredentialEnvVarsField` is
the credential editor: it's built over a plain `string[]` of env-var names,
so a credential value structurally cannot enter the React tree or a request
body — `CredentialEnvVarsField.test.tsx` asserts this with negative regex
matches against rendered output. `DiagnosePanel` posts to `/capabilities/
{id}/diagnose` and surfaces the healthy/unhealthy verdict plus suggestions.

Workspaces (`src/library/workspaces/`) edits repos (`RepoList`, with a
primary-repo radio), defaults (agent/model/isolation/template binding), and
shows `UsedBySessions` — a client-side reverse lookup over `GET /sessions`
filtered by `session.workspace`, since the Router has no server-side
`workspace -> sessions` field.

Pipeline templates (`src/library/templates/`) is a dual-mode visual/YAML
editor kept in sync by construction: `TemplateEditor` owns exactly one piece
of state, the YAML `source` string; visual mode (`VisualStageList`/
`StageCard`) is a pure projection derived via `templateSync.parse`, and
edits made there flow back through `templateSync.serialize` to replace
`source` wholesale — there's no second copy of the parsed model to drift out
of sync. `StageCard` exposes the full `pkg/template` grammar (name,
agent/command, when, on_fail.{loop_back,max_loops,then}). Validation posts
to `/templates/validate` (`useTemplateValidation.ts`) and surfaces every
`RouterValidationError` field error at once, routed onto the owning
`StageCard` by parsing its `stages[N].field` path — a seeded multi-error
template reports all of them together, not just the first. The YAML view
lazy-loads `@monaco-editor/react` the same way `DiffViewer` does (see Files
tab above); its tests mock the module and use `findByTestId`, not
`getByTestId`, since `Suspense` resolution is asynchronous even against a
mocked import.

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

Like the rest of the app, this surface talks to the Router through the
shared `router` (`RouterClient`/`FetchRouterClient`) singleton plus
`subscribeToEvents`. `retro.*` events aren't session-scoped
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

## Settings & first-run

`src/features/settings/SettingsSurface.tsx` (W6.10; MADS-UX §3.7/§3.9),
registered as `SURFACES.settings` — no demo mount needed, since the wizard
and every panel are exercised by component tests against stubbed `fetch`,
same as Retro. Owns a single shared `useConfig()` instance and a nav rail
of eight panels: Appearance (palette/theme, live-apply via `data-theme`/
`data-palette` on `document.documentElement`, persisted through
`PUT /config`), General (concurrency limit, log retention, editor command,
display mode, keybindings, plus a "run preflight" affordance), Profiles
(create/edit/delete, Activate), Providers (the capability-matrix table),
Credentials (masked list, add/delete, env-var import, vault-sources empty
state — see below), Pipeline (synthesis/agent-selection/model-tier settings
with static cost hints), Advanced (three feature-flag toggles), and
Updates/About. Restart-required fields (`concurrency_limit`,
`log_retention_days`, `feature_flags` — a UI-owned classification in
`restartRequired.ts`, not server metadata) show a badge on the owning
panel; every panel reads the same `useConfig().fieldErrors` map for
validate-all's inline, per-field errors.

Credential values never round-trip back to the renderer: `useCredentials`'s
state is `names: string[]` only, structurally unable to retain a value;
`CredentialsPanel`'s submit handler clears the value input synchronously,
before `add()`'s network call is awaited. Env-var import only ever moves
names over the wire — the values themselves are read server-side, inside
the Router process, straight from `os.Getenv`.

`src/onboarding/FirstRunWizard.tsx`, gated by `App.tsx`'s mount-time
`GET /config` fetch (`!config.onboarding_complete` renders the wizard in
place of the icon-rail shell entirely). Five steps under `onboarding/
steps/`: Welcome (static), Provider preflight (shares `usePreflight` with
Settings > General — against the stubbed preflight backend everything
reports healthy, so Continue is never gated), Credential import (shares
`useCredentials`, skippable), First workspace (`POST /workspaces`, plus an
optional default-provider `<select>` sourced from `router.listProviderNames()`
— leaving it at "No default" skips it), and a final inline step that `PUT`s
`onboarding_complete: true` and calls `onComplete()`. The created workspace's
id and the chosen provider (if any) are staged into the wizard's shared
`useConfig()` draft as soon as the workspace is created, so the final step's
`PUT /config` persists them as `default_workspace`/`default_provider`
alongside `onboarding_complete: true` — never clobbering either field if the
provider choice is left unset. `App.tsx` also applies the persisted
theme/palette and feature flags on every mount, so a returning user's choices
survive reload.

## Dashboard tab

```sh
npm run dashboard
```

Opens `dashboard.html`: the Dashboard tab (`src/dashboard/DashboardTab.tsx`)
mounted standalone against `FakeMetricsClient` and the golden aggregates
recorded in `src/dashboard/client/fixtures.ts` (copied verbatim from
`pkg/dataclient/metrics_test.go`'s expected outputs — see `src/dashboard/
HANDOFF.md` §5). Dashboard is also registered directly in the app shell
(`src/App.tsx`'s `RAIL_ITEMS`/`SURFACES`) — unlike the other tabs above, it's
a top-level `IconRail` destination rather than a per-session sub-tab, so it
mounts a real `FetchMetricsClient` there instead of the demo's fake one.

`DashboardTab` owns date-preset state (7d/30d/90d/all) and composes 8 leaf
views fed by two independent hooks: `useDashboardMetrics` (fetch-once-on-mount
for cost, agent leaderboard, task outcomes, capability usage, and run
history) and `useLiveActivity` (refetch-on-`session.*`/`pipeline.*`-event for
the live "active sessions / running pipelines" widget). Per the date preset:
only the cost-by-model chart (`CostChart/CostByModelChart.tsx`) and the run
history table (`RunHistory/RunHistoryTable.tsx`) re-slice client-side on the
selected range — the agent leaderboard, task outcomes, and capability usage
views have no time axis in their response shape and always render the full
fetched data.

`CostByModelChart` is a stacked bar (one bar per day, stacked by model) using
only `var(--chart-1/2/3)` — never a hex literal — with a fixed first-seen hue
order, a 4th+ model folded into an "Other" bucket, a per-day-column hover
tooltip, and a table-view toggle as the accessible fallback.
`TaskOutcomes/TaskOutcomesPanel.tsx` is a status-distribution bar that
deliberately does *not* use the `--chart-*` tokens — task status is a
severity encoding, so it reuses `--ok`/`--warn`/`--bad`/`--info` the same way
`Chip`'s `ChipTone` does, with the same legend + table-toggle treatment.

There's no cross-surface router in this codebase yet, so "drill-back to the
session's Pipeline tab" is implemented as a callback contract, not a
navigation: `RunHistoryTable` takes an `onDrillBack?: (params: { sessionId,
runSeq }) => void` prop and `DashboardTab` forwards its own `onDrillBack` prop
through unchanged. Actually opening `pipeline.html?session=...` (or a future
in-app router) is the caller's decision, out of scope for this module — see
`src/dashboard/HANDOFF.md` §7/§11.

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
Service directly. `src/router/client.ts` exports the `RouterClient` interface
— one typed method per endpoint — plus its `FetchRouterClient` (real HTTP)
and `FakeRouterClient` (fixture-backed, for tests) implementations, and a
`router: RouterClient` singleton (`new FetchRouterClient(ROUTER_BASE)`) that
call sites import directly; tests inject a `FakeRouterClient` instead, via
props or a local override. `routerGet`/`routerPost`/`routerPut`/
`routerDelete` are private transport helpers used only inside
`FetchRouterClient`'s method bodies — call sites never call them directly.
`src/router/events.ts` exports
`subscribeToEvents({ sessionId?, types }, onEvent): () => void`, a thin
wrapper over `EventSource` against `GET /events`, and remains the one
sanctioned idiom for live SSE updates alongside the `RouterClient` singleton
for request/response calls. The server always names the
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

`src/featureFlags.ts` exports a live `FLAGS` object (`multiRepo`,
`linkedSessions`, `remoteRegistry`) backed by `Config.feature_flags`
(W6.10). `App.tsx` calls `setFlags(...)` once on mount alongside
`applyTheme`; Settings > Advanced's toggles call it again after each
successful `PUT /config`. Consumers read `FLAGS.<name>` directly in their
render body (no hook) — `subscribeFlags(cb)` is available for anything that
needs to re-render on a change. Gate any flagged UI behind `FLAGS.<name>`.

`src/featureFlags.ts` is a temporary hardcoded stub (`export const FLAGS = {
multiRepo: false, remoteRegistry: false }`) standing in for a real `/config`
endpoint, which doesn't exist yet. Gate any flagged UI behind `FLAGS.<name>`;
swap the stub for a real fetch once the backend adds one, rather than
threading config through each surface individually. Library's Browse
Registry shelf is gated behind `FLAGS.remoteRegistry` (see Library above).

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
- `src/components/ErrorBoundary/ErrorBoundary.tsx` is the one error-boundary
  primitive future shell/surface work should reuse rather than rolling
  another `componentDidCatch` — `App.tsx` wraps only the active surface
  content in it, keyed by `activeId` so navigating away from a crashed
  surface remounts a clean instance. `App.tsx`'s mount-time config gate
  (`GET /config`) also has a real failure state now (`ConfigGateFailure`) —
  a down Router at mount shows a "can't reach the backend" screen with
  Retry, not a permanently blank window.
