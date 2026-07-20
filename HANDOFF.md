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

- **No manual browser smoke test.** This environment has no browser tool,
  and exercising the real flow requires a live Router (+ Data + vault)
  backend that isn't available here — there's no mock/fixture Router server
  checked into the repo to point the dev server at. I did not fabricate a
  "verified in browser" claim. Verification instead relied on:
  - `npx tsc --noEmit -p tsconfig.json` — clean.
  - `npm run lint` — clean.
  - `npx vitest run` — 205 test files / 1088 tests passing, including all
    six acceptance-criteria scenarios (one-page render + deep link scroll,
    scroll-spy incl. bottom-of-page, switch diff/restart-badge/toast+undo,
    drift bar on field-edit and credential-toggle with all three actions,
    credential dimming/never-echo/delete-warns-with-bound-profiles).
  - **Reviewer should do a manual pass in a browser against a real backend
    before merge**, particularly to check both themes visually (CSS was
    written against existing tokens/patterns but not rendered).
- **Did not touch `docs/mockups/`** — no mockup file exists, nothing to
  reconcile against it.

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
