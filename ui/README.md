# ui/

The Electron + TypeScript app shell and design system (docs/MADS-WORK-ITEMS.md
W6.1). This package owns the token system, type rules, and reusable component
kit every future surface composes from, plus the Electron scaffold that hosts
them. It also does not do per-platform packaging (electron-builder); that's
later work, not gated by this package.

Sessions (W6.2) is the first real surface: session list rail + Launchpad
(hero composer, metadata pills, suggestion chips, provisioning strip). See
`src/features/sessions/`. Every future surface (Retro, Library, etc.) follows
the same pattern: one entry in `App.tsx`'s `SURFACES` map
(`{ id: Component }`), registered against `IconRail`'s existing item ids —
that map is the single app-shell registration point, kept to a one-line diff
per surface so concurrent surface work doesn't collide on a shared file.

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
`SURFACES` map resolves for the active rail item — Sessions today, a teaching
empty state for any rail item without a registered surface yet (Retro,
Library).

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

## Test it

```sh
npm run lint    # eslint (typescript-eslint + eslint-plugin-react-hooks)
npm run test    # vitest: tokens, contrast, every component
```

`src/tokens/tokens.test.ts` pins every hex/rgba value for all 6 palette×theme
combinations to `docs/palette-mockups.html` as a regression guard, asserts
`tokens.css` and `tokens.ts` stay byte-identical across all 6, and runs a
WCAG AA contrast check (relative luminance, no dependency) over every
documented fg/bg pair in each combination. Every component has a
`*.test.tsx` alongside it (component-level, `@testing-library/react` +
happy-dom).

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
