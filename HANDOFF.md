# Executor Handoff: Settings as One Page

## What was done

Rebuilt `ui/src/features/settings/` from tab-routed sub-panels into a single
continuously-scrolling page, per the architect handoff and task acceptance
criteria (no `docs/mockups/profiles-ux-v2.html` exists — task description +
acceptance criteria were used as spec, as the architect handoff itself notes).

- **`SettingsSurface.tsx`**: rewritten as one scrollable page. Renders all
  eight sections (`profiles`, `general`, `pipeline`, `appearance`,
  `credentials`, `advanced`, `providers`, `updates`) in document order inside
  a single scrolling container, wires `ScrollSpyNav` (rail highlight +
  click-to-scroll), mounts `DriftBar` above the sections, and scrolls to the
  section matching `location.hash` on mount (deep-link support).
- **`panels/` → `sections/`**: all eight `*Panel.tsx` files renamed and
  folded into `*Section.tsx` (no conditional/routed rendering). Old
  `ProfilesPanel` (name+description cards with Activate/Edit/Delete) is gone;
  `ProfilesSection` now shows profile cards with an ACTIVE badge that open
  `ProfileSwitchDialog` on click.
- **`ScrollSpyNav` + `useScrollSpy`/`computeActiveSection`**: active section
  = last section whose top has passed the ~30%-viewport reading line; at
  page bottom the last section is forced active even if it can't reach the
  reading line (covered by a dedicated regression test).
- **`ProfileSwitchDialog`**: diff dialog showing only changed fields grouped
  by section, RESTART badge on fields in the (Go-canonical) restart set,
  credential changes shown by name with "active → inactive" / "inactive →
  active" wording. Apply switches, refetches config, and reports whether a
  restart-affecting field changed.
- **`DriftBar` + `useDrift`**: MODIFIED bar on any field edit or credential
  toggle while a profile is active; Update profile / Save as new profile /
  Revert all implemented and clear the bar.
- **`CredentialsSection`**: all vault credentials always listed; ones not
  bound to the active profile are dimmed with a badge and a toggle that
  flips drift (not a vault call) rather than being hidden. Add/delete
  remain vault-global with the existing never-echo-values UX.
- Toast on profile switch reuses the existing unused `Toast` component
  (local `useState`, no new provider) with an Undo action that restores the
  previous config, plus an "applies on mads restart" note when relevant.
- New Config fields with no prior UI (`cost_alert_cents`, `fast_model`,
  `synthesis_debounce_ms`) now have input rows (`fast_model`/`cost_alert_cents`
  in Pipeline & synthesis, `synthesis_debounce_ms` in General — see
  deviations below).

## Deviations from the architect handoff

- **`RESTART_REQUIRED_FIELDS`**: architect handoff listed
  `concurrency_limit, log_retention_days, feature_flags`. The actual Go
  source (`pkg/contracts`) defines the restart set as `concurrency_limit,
  log_retention_days, synthesis_debounce_ms` — `feature_flags` is not in it.
  Implemented to match Go, not the handoff's list, since this drives a
  user-facing "restart required" claim that must match backend behavior.
- **Field→section grouping**: the handoff sketched `fast_model,
  cost_alert_cents` under General and `concurrency_limit, log_retention_days,
  feature_flags` under Advanced. The implementation (`sections.ts`'s
  `FIELD_SECTION` map) instead groups `concurrency_limit`,
  `log_retention_days`, `synthesis_debounce_ms` under General (they're
  already-existing General-panel fields) and `cost_alert_cents`/`fast_model`
  under Pipeline & synthesis (cost/model-tier fields), with only
  `feature_flags` in Advanced. This was decided in an earlier stage of this
  same session, before this executor picked the work back up — flagging it
  here since it doesn't match the handoff's sketch.
- **Hash sync is one-directional**: clicking a rail item updates
  `location.hash` via `history.replaceState`; scrolling does *not*
  continuously rewrite the hash. The handoff's structure sketch says "update
  hash on scroll" — that was deliberately left out to avoid a
  scroll-position/hash-write feedback loop, and isn't required by any
  acceptance criterion (only "deep links scroll correctly" is required, which
  is satisfied and tested).

## What was not done

- **Did not touch `docs/mockups/`** — no mockup file exists, nothing to
  reconcile against it.

## Update from a later executor pass: real-browser verification found and fixed a severe scroll-spy bug

A prior pass of this stage (the "What was not done" section above, now
superseded) claimed no browser tool was available and relied on
tsc/lint/vitest alone. That was wrong — this repo ships its own Playwright
e2e harness (`ui/e2e/`, `playwright.config.ts` + `globalSetup.ts` boot a
real Router+Data-Service topology per run) and `@playwright/test`/browsers
were already installed. Following the standing instruction to verify UI
changes in a real browser before claiming completion, this pass:

1. Ran `npm run build` (the Router serves `ui/dist`, not a live dev
   server — a stale/missing `dist` shows a "UI not built" placeholder,
   which is what a first e2e attempt hit).
2. Ran the pre-existing `e2e/settings.spec.ts` against the rebuilt UI —
   passed unmodified, confirming the tab→nav-button change didn't break the
   appearance/preflight journey it covers.
3. Drove the app directly in a real Chromium browser (via a temporary,
   now-deleted `e2e/_tmp-verify.spec.ts`) through: bottom-of-page
   scroll-spy, `#credentials` deep link, editing a field while a profile is
   active (drift bar), the profile-switch diff dialog, and both light and
   dark themes. Screenshots were inspected directly.

**This surfaced a real, severe bug invisible to the unit-test suite:**
`useScrollSpy` (`ui/src/features/settings/nav/useScrollSpy.ts`)'s effect
depended only on `[containerRef, sectionRefs]` — both stable ref *objects*
whose identity never changes. `SettingsSurface` renders a loading
placeholder before the real page mounts, so on first render
`containerRef.current` is `null` and the effect's guard clause returns
early without attaching a scroll listener. Because ref-object identity
doesn't change when `.current` is later assigned a real DOM node, the
effect never reran — scroll-spy was permanently frozen on whatever section
happened to be active during the very first (pre-mount) render, regardless
of actual scroll position. This is exactly the class of bug the
acceptance criteria's "regression test required for the bottom-of-page
case" was meant to catch, but the existing `scrollSpy.test.ts` only unit-
tests the pure `computeActiveSection`/`isAtBottom` functions — no test
exercised the DOM-wiring hook itself.

**Fix**: `useScrollSpy` now takes a third `ready: boolean` parameter,
included in the effect's dependency array; `SettingsSurface` passes
`!config.loading && !!config.draft` (mirroring the already-correct pattern
used by the neighboring `scrolledToHash` effect, which already depended on
real state rather than ref identity). Added
`ui/src/features/settings/nav/useScrollSpy.test.ts`, a `renderHook`-based
regression test that reproduces the exact mount-timing scenario (hook
first rendered with `ready: false` and a null container, then rerendered
with `ready: true` once the container/sections are attached with geometry
placing the true scroll position at the bottom) and asserts the last
section becomes active — the bottom-of-page case specifically.

Also fixed two stale doc-comment references in
`ui/src/features/settings/state/useConfig.ts` (`AppearancePanel` →
`AppearanceSection`, `ProfilesPanel` → `ProfilesSection`, left over from
the `panels/` → `sections/` rename).

**Verification after the fix** (same real-browser flow as above, rebuilt):
scroll-spy correctly highlights "Updates & About" at true page bottom
(confirmed via real DOM `scrollTop`/`scrollHeight` diagnostics, not just
the assertion), `#credentials` deep link scrolls and highlights correctly,
editing Concurrency limit while "quiet-hours" is active shows the
"Modified" drift bar with a RESTART badge, the switch dialog to
"busy-hours" shows the correct field diff (`Concurrency limit 2 → 5`) with
its own RESTART badge, and both light and dark themes render with correct
contrast (screenshots inspected directly, not just asserted).

**One environment gotcha worth flagging for future sessions**: this
worktree has an unrelated, already-running `./bin/router` process bound to
`localhost:8080` (started by an earlier turn in this same session). The
app's `ROUTER_BASE` (`ui/src/router/client.ts`) falls back to
`http://localhost:8080` when `VITE_ROUTER_URL` isn't set at build time —
so a `npm run build` without that env var, when tested against Playwright's
own ephemeral (randomly-ported) e2e topology, will silently talk to that
*other*, unrelated router process instead of the one actually serving the
test page, because the ambient process happens to occupy the hardcoded
default port. Worse, that ambient router's static assets are apparently a
Go-embedded snapshot from whenever its binary was last built, not a live
read of `ui/dist` — so testing against it directly serves *stale* UI code
regardless of subsequent `npm run build` runs, which cost real time to
diagnose here. The reliable pattern (used for the fix verification above):
build with `VITE_ROUTER_URL` set to the e2e harness's own `routerURL`
fixture value inside a `test.beforeAll`, so the built bundle's fetches
target the same ephemeral instance serving the page. This is a pre-existing
application/environment quirk, not something introduced or fixed by this
task — flagging it since it will bite the next person who tries to
manually verify this UI in a browser in this environment.

## Points for reviewer scrutiny

1. **`ProfileSwitchDialog.test.tsx`'s credential-diff test fixture** — the
   original test data (an earlier-session artifact) asserted both
   "active → inactive" and "inactive → active" wording could appear from a
   `currentConfig` with `credential_bindings` undefined. Per
   `isCredentialActive`'s documented semantics (undefined/empty bindings =
   all credentials implicitly active), that data could never produce an
   "inactive → active" diff entry. Fixed the test's fixture (explicit
   `credential_bindings: ["anthropic"]` on `currentConfig`) rather than
   touching `profileDiff.ts`, since the diff logic was already correct and
   tested elsewhere. Worth a second look to confirm this reasoning holds.
2. **Mutable ref workaround in `SettingsSurface.tsx`** — `useScrollSpy`'s
   exported `SectionRefs` type uses `RefObject<HTMLElement | null>`, whose
   `.current` is read-only under `@types/react` 18.3.31. Rather than change
   the shared hook, `SettingsSurface` declares a local
   `MutableSectionRefs = Record<SectionId, {current: HTMLElement | null}>`
   type and passes those objects both into `useScrollSpy` and as JSX
   `ref` props. Functionally fine (verified by the passing deep-link test,
   which asserts `scrollIntoView` is called on the right section), but it's
   a slightly unusual TypeScript pattern worth a sanity check.
3. **Two pre-existing test files in this branch** (`DriftBar.test.tsx`,
   `ProfileSwitchDialog.test.tsx`, both untracked artifacts from earlier in
   this pipeline session) originally used `@testing-library/jest-dom`
   matchers (`toBeInTheDocument`, `toBeEmptyDOMElement`), which isn't a
   project dependency — confirmed via `git stash` + repo-wide grep that no
   other test file uses this syntax. Rewrote both to the codebase's
   plain-assertion convention (`.toBeTruthy()`, `container.innerHTML`).
   Behavior of the tests is unchanged, only the assertion syntax.
