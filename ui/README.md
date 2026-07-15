# ui/

The Electron + TypeScript app shell and design system (docs/MADS-WORK-ITEMS.md
W6.1). This package owns the token system, type rules, and reusable component
kit every future surface composes from, plus the Electron scaffold that hosts
them. It now also owns the Conversation tab (W6.3; MADS-UX §3.3) and the
Files tab & merge-back flow (W6.5; MADS-UX §3.3/§5/§17), plus the
`RouterClient` HTTP wrapper both are built against — see `src/conversation/`,
`src/files/`, and `src/router/`. It does **not** yet own the Session list/
Launchpad surface or the app shell's tab registry (W6.2), so neither
`ConversationTab` nor `FilesTab` is wired into `App.tsx` yet; they're
reachable standalone via the `conversation` and `files` demo mounts below.
It also does not do per-platform packaging (electron-builder); that's later
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
