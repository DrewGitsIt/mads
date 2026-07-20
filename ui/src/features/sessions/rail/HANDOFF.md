# HANDOFF: Link-mode entry + selection flow feeding LinkChooserDialog

**Task:** Rail: link-mode entry + selection flow feeding LinkChooserDialog (#2 link UX)

## What changed

`SessionRail.tsx` / `SessionRail.module.css` / `SessionRail.test.tsx`. This
picks up the scope gap the prior "Option C card interaction" HANDOFF flagged:
the always-on multi-select (`checkedIds`, any row's 🔗 toggles its own flat
membership, bar appears at 2+) is replaced with the anchor-based link mode from
`docs/mockups/LINK-FLOW-HANDOFF.md` / `docs/mockups/sessions-ux.html` step 1.

- New `LinkModeState { anchorId, selectedIds }` replaces `checkedIds`. Clicking a
  top-level row's 🔗 action (`onStartLinkMode`) sets that row as the anchor and
  seeds `selectedIds` with just its id.
- While link mode is active, every top-level row's hover-only `.actionCluster`
  (Resume/Link/Archive) is swapped for a persistent `.linkModeIndicator`:
  - Anchor row → "Anchor" badge, inert (its own click no-ops).
  - Same-workspace rows (`session.workspace === anchor.workspace`, comparing the
    `Session.workspace` field, not `worktree_dir` — see note below) → a
    (visual, non-independently-focusable) checkbox reflecting selection; the
    **whole row** is the tap target (`activateOrToggle` toggles instead of
    navigating), so tapping doesn't also fire `onSelect`/`onResume`.
  - Cross-workspace rows → dimmed (`opacity: 0.5`), `aria-disabled`, `tabIndex=-1`,
    a "different workspace" tag instead of a checkbox, and clicks no-op.
- A sticky floating bar (`.linkBar`) shows `"{n} selected"`, a "Choose mode"
  button (disabled until 2+ selected — need at least one subject) that opens the
  existing `LinkChooserDialog` unchanged, and "Cancel" which exits link mode.
  `LinkChooserDialog` still gets `sessionIds={[...selectedIds]}` — insertion
  order puts the anchor first, matching its existing "holder defaults to
  `sessionIds[0]`" behavior.
- Nested (subject) rows are **not** part of link mode — they always render their
  normal Unlink action regardless of whether link mode is active elsewhere in the
  rail, same restriction as before (only top-level rows had a Link button).
- `FLAGS.linkedSessions` gating is unchanged and untouched: it defaults to
  `false` in `featureFlags.ts` and I couldn't find any server-side override
  (`grep`ped Go for `linked_sessions`/`LinkedSessions` — nothing), so it's off
  in the current build unless a user has flipped it in Settings → Advanced.
  With it off, `linkedSessionsEnabled` short-circuits every link-mode branch, so
  no checkbox/anchor/dimming/floating-bar chrome renders at all (verified by
  test).

## Workspace vs. worktree — scope decision, reviewer should scrutinize

The design mockup (`LINK-FLOW-HANDOFF.md`) and this task's acceptance criteria
both say "cross-workspace," but `LinkChooserDialog`'s own constraint copy talks
about "different worktrees of the same repo," and `docs/LINKED-SESSIONS-DESIGN.md`
states the real constraint as worktree-based. `Session` carries both a
`workspace` field (a bundle of repos/defaults, per `pkg/contracts/registry.go`)
and a per-session `worktree_dir`/`worktree_branch` (each session gets its own
isolated worktree, so two linkable same-workspace sessions will almost always
have *different* `worktree_dir` values). I used `session.workspace` for the
dimming boundary because (a) it's what the mockup's mock data literally keys
off, (b) comparing `worktree_dir` would dim nearly every other card, defeating
the same-workspace tap-to-add flow entirely, and (c) the server still validates
and returns `RouterValidationError` (already surfaced as field errors in
`LinkChooserDialog`) if a link is actually invalid — the rail's dimming is a
pre-flight nicety, not the enforcement point. If "workspace" here was meant more
literally as "worktree," the comparison in `RailRowGroup` (`isCrossWorkspace`)
is the one line to change.

## Tests

Rewrote the old checked-toggle/bar tests into: entering link mode sets an
anchor + shows the floating bar with "Choose mode" disabled at 1 selected;
tapping a same-workspace card's row body adds/removes it (and doesn't fire
`onSelect`); a dedicated cross-workspace test asserts the dimmed card gets no
checkbox and can't be added while a same-workspace card still can; Cancel exits
link mode and restores the per-row link button; the create-link flow now drives
through tap-to-add + "Choose mode" instead of two independent toggles. Kept the
existing flag-off test (zero checkboxes, no `/to link selection$/` button) and
the holder-badge/nesting/unlink tests unchanged since link mode doesn't touch
nested rows.

Full suite (`npx vitest run`, 211 files / 1173 tests) is green, `tsc --noEmit -p
tsconfig.json` and `eslint .` on the touched directory are clean.
