# ui/

The Electron + TypeScript app shell and design system (docs/MADS-WORK-ITEMS.md
W6.1). This package owns the token system, type rules, and reusable component
kit every future surface composes from, plus the Electron scaffold that hosts
them. It now also owns the Conversation tab (W6.3; MADS-UX §3.3), the Tasks
tab & Context Rail (W6.4; MADS-UX §3.3), and the `RouterClient` HTTP wrapper
they're built against — see `src/conversation/`, `src/tasks/`,
`src/context-rail/`, and `src/router/`. It does **not** yet own the Session
list/Launchpad surface or the app shell's tab registry (W6.2), so neither
`ConversationTab` nor `TasksTab` is wired into `App.tsx` yet; they're
reachable standalone via the `conversation`/`tasks` demo mounts below. It
also does not do per-platform packaging (electron-builder); that's later
work, not gated by this package.

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

The app shell currently renders an icon rail and a teaching empty state — no
real surfaces are wired up yet.

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
- `make build`/`make test` from the repo root run this package's build/lint/
  test as part of the standard gates (see root `Makefile`'s `ui-deps` target,
  which runs `npm ci`) — a fresh worktree needs no manual steps beyond that.
- `node-pty` is an `optionalDependency`, dynamically `import()`-ed only from
  `electron/pty/ptyHost.ts` (main process) inside a try/catch. Nothing under
  `src/**` may import it, even transitively — that's what keeps `npm run
  test` (and `make test`) safe to run on a machine with no display or native
  build toolchain.
