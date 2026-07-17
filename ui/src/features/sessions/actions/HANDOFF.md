# HANDOFF: Session delete/close discoverability

Architect stage complete. This document is the design for the executor stage.
Do not redesign — implement what's below, using the judgment calls section only
where explicitly flagged.

## Problem

`SessionWorkspace.tsx`'s kebab menu renders **Close XOR (Resume + Delete)**
based on `session.activity`. Delete is only reachable when a session is
already inactive, and is invisible while active. Original intent: always show
all three lifecycle actions; disable the unavailable ones with an inline
reason. Discovery beats minimalism. Separately, rail rows only expose a
"Resume" quick-affordance — there's no way to Close or Delete without opening
the workspace first.

No backend/Go changes are needed. `RouterClient` (`ui/src/router/client.ts`)
already declares and implements `closeSession`, `resumeSession`,
`deleteSession`, and `FakeRouterClient` (`ui/src/router/fixtures.ts`) already
implements all three for tests. This is a UI-only change.

## New shared module: `ui/src/features/sessions/actions/`

Create three files here (this directory currently only contains this
HANDOFF.md):

### 1. `sessionLifecycle.ts` — pure helper, no React

`SessionActivity` (`ui/src/router/types.ts`) is exactly
`"active" | "inactive"` — a clean binary, so each action only has two cases.

```ts
import type { SessionActivity } from "../../../router/types";

export interface LifecycleActionState {
  disabled: boolean;
  reason?: string;
}

export function closeState(activity: SessionActivity): LifecycleActionState {
  return activity === "active"
    ? { disabled: false }
    : { disabled: true, reason: "Session is already inactive" };
}

export function resumeState(activity: SessionActivity): LifecycleActionState {
  return activity === "inactive"
    ? { disabled: false }
    : { disabled: true, reason: "Session is already active" };
}

export function deleteState(activity: SessionActivity): LifecycleActionState {
  return activity === "inactive"
    ? { disabled: false }
    : { disabled: true, reason: "Close the session first" };
}
```

(The "Session is already inactive" wording for Close is a judgment call — see
Open Decisions below. The Delete and Resume reason strings are exact text
from the task brief and must not be changed.)

### 2. `SessionActionsMenu.tsx` — shared popover content

This replaces the inline `<ul>` in `SessionWorkspace.tsx`'s kebab and becomes
the content of the new rail row "⋯" popover. It renders the `Panel`/
`PanelBody`/`<ul>` markup — same kit idiom as `SessionWorkspace.tsx`'s
existing kebab and `ui/src/conversation/Composer/MentionMenu.tsx` — but not
the trigger button or its `position: relative` wrapper. Each caller owns its
own trigger and wrapper div, matching how `SessionWorkspace` and
`MentionMenu` already each own their own trigger today.

```tsx
import { Panel, PanelBody } from "../../../components/Panel/Panel";
import type { SessionActivity } from "../../../router/types";
import { closeState, resumeState, deleteState } from "./sessionLifecycle";
import styles from "./SessionActionsMenu.module.css";

export interface SessionActionsMenuProps {
  activity: SessionActivity;
  onClose: () => void;
  onResume: () => void;
  onDeleteRequest: () => void;
  className?: string;
}

export function SessionActionsMenu({
  activity,
  onClose,
  onResume,
  onDeleteRequest,
  className,
}: SessionActionsMenuProps) {
  const close = closeState(activity);
  const resume = resumeState(activity);
  const del = deleteState(activity);
  return (
    <Panel className={className}>
      <PanelBody>
        <ul className={styles.menuList}>
          <li>
            <button
              type="button"
              className={styles.menuItem}
              disabled={close.disabled}
              onClick={onClose}
            >
              Close
              {close.reason && <span className={styles.reason}>{close.reason}</span>}
            </button>
          </li>
          <li>
            <button
              type="button"
              className={styles.menuItem}
              disabled={resume.disabled}
              onClick={onResume}
            >
              Resume
              {resume.reason && <span className={styles.reason}>{resume.reason}</span>}
            </button>
          </li>
          <li>
            <button
              type="button"
              className={styles.menuItem}
              disabled={del.disabled}
              onClick={onDeleteRequest}
            >
              Delete
              {del.reason && <span className={styles.reason}>{del.reason}</span>}
            </button>
          </li>
        </ul>
      </PanelBody>
    </Panel>
  );
}
```

`onClose`/`onResume`/`onDeleteRequest` are always wired by the caller
regardless of disabled state — the `disabled` attribute is what actually
prevents the click, so callers don't need to guard.

`SessionActionsMenu.module.css`: copy `.menuList`/`.menuItem`/`.menuItem:hover`
from `SessionWorkspace.module.css` verbatim (do not import cross-module —
each component module owns its own CSS in this codebase), then add:

```css
.menuItem:disabled {
  color: var(--text3);
  cursor: default;
}

.menuItem:disabled:hover {
  background: none;
}

.reason {
  display: block;
  font: 400 var(--fs-chip) var(--sans);
  color: var(--text3);
}
```

`.menu` (the `position:absolute; top:100%; right:0; ...` positioning rule)
stays local to each caller's own CSS module, since each trigger sits in a
different place (workspace top bar vs. rail row) — do not move it here.

### 3. `SessionDeleteConfirmDialog.tsx` — shared confirm dialog

Thin wrapper hard-coding the exact existing copy from
`SessionWorkspace.tsx`, so both callers get byte-identical text:

```tsx
import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";

export interface SessionDeleteConfirmDialogProps {
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function SessionDeleteConfirmDialog({
  open,
  onConfirm,
  onCancel,
}: SessionDeleteConfirmDialogProps) {
  return (
    <ConfirmDialog
      open={open}
      title="Delete this session?"
      description="Deleting permanently removes this session, its tasks, file-change history, and commit records. Provider transcripts and the event log survive. This can't be undone."
      confirmLabel="Delete"
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
```

`ConfirmDialog`'s backdrop is `position: fixed; inset: 0;` (see
`ConfirmDialog.module.css`), so it renders correctly regardless of where in
the tree it's mounted — safe for `SessionRail` to own a single instance for
whichever row is mid-delete.

## Changes to `SessionWorkspace.tsx`

Keep all existing state (`menuOpen`, `confirmingDelete`, `actionError`) and
handlers (`closeSession`, `resumeSession`, `confirmDelete`) as-is — only swap
the rendered markup:

- Replace the inline `{menuOpen && <Panel>...}` block (lines 148–183) with:
  ```tsx
  {menuOpen && session && (
    <SessionActionsMenu
      className={styles.menu}
      activity={session.activity}
      onClose={closeSession}
      onResume={resumeSession}
      onDeleteRequest={() => {
        setMenuOpen(false);
        setConfirmingDelete(true);
      }}
    />
  )}
  ```
  Guard on `session` (not just `menuOpen`) since `activity` is required —
  matches the existing `session?.activity === ...` guards being replaced.
- Replace the inline `<ConfirmDialog ... />` (lines 191–198) with:
  ```tsx
  <SessionDeleteConfirmDialog
    open={confirmingDelete}
    onConfirm={() => void confirmDelete()}
    onCancel={() => setConfirmingDelete(false)}
  />
  ```
- Remove the now-unused `ConfirmDialog`/`Panel`/`PanelBody` imports from this
  file (kept only if still used elsewhere in the file — they aren't).
- `.menuList`/`.menuItem`/`.menuItem:hover` become dead CSS in
  `SessionWorkspace.module.css` once the shared component takes over — remove
  them; keep `.kebabWrap`/`.menu`.

## Changes to `SessionRail.tsx`

Add per-row lifecycle actions without disturbing the existing `onResume`
quick-affordance (see Open Decisions — keep both).

- `SessionRailProps`: add `onClose?: (id: string) => void` and
  `onDelete?: (id: string) => void`, passed through to every `SessionRow` call
  site (both the `active` and `recent` `.map(...)` blocks).
- `SessionRow` needs its own open/closed menu state and delete-confirm state.
  Since `SessionRow` is currently a stateless function (not a component with
  hooks), give it `useState` for `menuOpen` — one open menu per row is fine,
  no need to lift state to the parent for the menu itself.
- Delete confirmation, however, **must** be a single instance owned by
  `SessionRail` (not per-row), so add `const [confirmDeleteId, setConfirmDeleteId] = useState<string>()`
  in `SessionRail` and pass `onDeleteRequest={() => setConfirmDeleteId(session.id)}`
  down to each row instead of letting each row own its own dialog. Render one
  `<SessionDeleteConfirmDialog>` at the bottom of `SessionRail`'s returned JSX,
  wired to `onConfirm={() => { onDelete?.(confirmDeleteId!); setConfirmDeleteId(undefined); }}`
  and `onCancel={() => setConfirmDeleteId(undefined)}`.
- In `SessionRow`, add a new trigger next to (not replacing) the existing
  `resumeAffordance` button:
  ```tsx
  <div className={styles.rowMenuWrap}>
    <button
      type="button"
      className={styles.rowMenuTrigger}
      aria-label={`${session.name || "Session"} actions`}
      onClick={(e) => {
        e.stopPropagation();
        setMenuOpen((x) => !x);
      }}
    >
      ⋯
    </button>
    {menuOpen && (
      <SessionActionsMenu
        className={styles.rowMenu}
        activity={session.activity}
        onClose={() => {
          setMenuOpen(false);
          onCloseProp?.(session.id);
        }}
        onResume={() => {
          setMenuOpen(false);
          onResume?.(session.id);
        }}
        onDeleteRequest={() => {
          setMenuOpen(false);
          onDeleteRequest(session.id);
        }}
      />
    )}
  </div>
  ```
  (Naming: rename the destructured `onClose`/`onDelete` props coming from
  `SessionRow`'s own props to avoid shadowing — e.g. keep the prop named
  `onClose`/`onDelete` in `SessionRailProps` but pass a bound
  `onDeleteRequest={() => setConfirmDeleteId(session.id)}` callback down to
  `SessionRow` instead of the raw `onDelete`, per the confirm-dialog lifting
  above.)
  `e.stopPropagation()` on the trigger matches the existing
  `resumeAffordance` pattern — prevents the row's own `onSelect` click handler
  from also firing.
- `SessionRail.module.css`: add, mirroring the `.resumeAffordance` opacity
  hover-reveal pattern:
  ```css
  .rowMenuWrap {
    position: relative;
  }

  .rowMenuTrigger {
    flex: none;
    opacity: 0;
    background: transparent;
    border: none;
    color: var(--text2);
    padding: 2px 6px;
    cursor: pointer;
    font: 500 var(--fs-sm) var(--sans);
    transition: opacity 0.15s;
  }

  .row:hover .rowMenuTrigger,
  .row:focus-within .rowMenuTrigger {
    opacity: 1;
  }

  .rowMenuTrigger:hover {
    color: var(--text);
  }

  .rowMenu {
    position: absolute;
    top: 100%;
    right: 0;
    margin-top: var(--space-1);
    z-index: 10;
    box-shadow: var(--shadow);
    width: 180px;
  }
  ```

## Changes to `SessionsSurface.tsx`

Currently wires only `onResume` on `<SessionRail>` with a `resumeError`
toast. Add `onClose`/`onDelete` with the identical catch→toast pattern.
Generalize `resumeError` → `actionError` (single shared error-toast state
covers all three actions — matches how `SessionWorkspace.tsx` already uses
one `actionError` for all three of its own handlers, so this keeps the two
call sites consistent):

```tsx
<SessionRail
  sessions={sessions}
  openTaskCounts={openTaskCounts}
  selectedId={selectedId}
  onSelect={...}
  onResume={(id) => {
    router.resumeSession(id).catch((err: unknown) => {
      setActionError(err instanceof Error ? err.message : String(err));
    });
  }}
  onClose={(id) => {
    router.closeSession(id).catch((err: unknown) => {
      setActionError(err instanceof Error ? err.message : String(err));
    });
  }}
  onDelete={(id) => {
    router.deleteSession(id).catch((err: unknown) => {
      setActionError(err instanceof Error ? err.message : String(err));
    });
  }}
/>
...
{actionError && (
  <Toast tone="bad" title="Session action failed" description={actionError} />
)}
```

Rename the `resumeError` state variable and its setter throughout this file;
update the `Toast title` from "Couldn't resume session" to something action-
neutral like "Session action failed" (matches the wording already used in
`SessionWorkspace.tsx`, for consistency).

## Test changes

### `SessionWorkspace.test.tsx`

Two existing tests directly encode the bug and must be rewritten to assert
presence-with-disabled-and-reason instead of absence:

- `"kebab shows Close for an active session, not Resume or Delete"` →
  rewrite to assert: Close is enabled; Resume is present but `disabled` with
  text "Session is already active"; Delete is present but `disabled` with
  text "Close the session first".
- `"kebab shows Resume and Delete for an inactive session, not Close"` →
  rewrite to assert: Resume and Delete enabled; Close present but `disabled`
  with reason text (whatever wording is chosen — see Open Decisions).

Use `screen.getByRole("button", { name: "Close" })` style queries and assert
`.toBeDisabled()` / check the reason `<span>` text, rather than
`getByText`/`queryByText`, since all three menu items are now always in the
DOM and some existing `getByText("Delete")`-style queries could otherwise
become ambiguous once every state is always rendered. Double check the
remaining tests (click handlers, delete confirm/cancel flow) don't break due
to this — they exercise the enabled-state path per session, which is
unchanged.

### `SessionRail.test.tsx`

Existing test `"shows a resume affordance only for non-active sessions when
onResume is passed"` should keep passing unmodified (existing affordance is
preserved). Add new tests:

- The "⋯" trigger renders for every row (active and inactive).
- Clicking it opens a `SessionActionsMenu` with all three actions, disabled
  state matching the row's `session.activity`.
- Clicking enabled Close/Resume calls `onClose`/`onResume` with the row's
  `session.id`.
- Clicking enabled Delete opens the shared `SessionDeleteConfirmDialog`;
  confirming it calls `onDelete` with the correct `session.id` (test with two
  rows to confirm the single lifted dialog instance targets the right row);
  canceling calls neither.

### `ui/e2e/sessions.spec.ts`

Extend the third test, `"sessions journey: close, resume affordance,
delete"` (currently lines 60–125), in place — per the brief's literal
phrasing "Extend the lifecycle arc in the sessions journey."

- Line 97 (`await expect(page.getByRole("button", { name: "Close",
  exact: true })).toHaveCount(0);`) currently asserts Close is entirely
  absent once inactive — this directly encodes the old bug and must change to
  assert Close is present-but-disabled with its reason text instead.
- Add, while the session is still active (before the existing Close step):
  assert the kebab's Delete item is visible, `disabled`, and shows "Close the
  session first"; optionally also assert Resume is `disabled` with "Session
  is already active".
- Add, after the session is inactive: a delete-from-rail flow that does
  **not** open the workspace — use the rail row's new "⋯" trigger, click
  Delete, assert the confirm dialog appears with the same copy already
  asserted elsewhere in this spec (`toContainText("event log survive")`
  style), confirm it, and assert the session disappears from the rail.

## Open decisions (executor's call, not fully prescribed by the brief)

1. **Close-disabled reason wording**: the brief specifies exact reason text
   for Delete-disabled ("Close the session first") and Resume-disabled
   ("Session is already active"), but not for Close-disabled-while-inactive.
   Proposed above: "Session is already inactive," for symmetry. Fine to use
   as-is; not mandated.
2. **Keep vs. remove the existing rail "Resume" quick-affordance** once the
   "⋯" menu also offers Resume. Recommend **keep both** — lower regression
   risk, brief doesn't mandate removing it, and it stays as a zero-click
   affordance for the single most common action.
3. **e2e test structure**: extending the existing third test in place (as
   designed above) vs. adding a fourth test. Either is defensible; this
   handoff assumes in-place extension.

## Definition of done

- `make check` (lint + `go test ./...` + `npm run lint` + `npm run test`)
  passes.
- `make ui-smoke` (Playwright e2e, including the extended `sessions.spec.ts`)
  passes.
- All three lifecycle actions always visible in the kebab, disabled states
  carry reason text.
- Rail rows offer the same three actions via a "⋯" trigger, without
  requiring the workspace to be opened.
- Delete confirmation copy/markup is not duplicated — both call sites render
  `SessionDeleteConfirmDialog`.

## Executor notes

Implemented as designed above, verbatim, with two deviations the reviewer
should scrutinize (both are bug fixes the design's code samples didn't
anticipate, not redesigns):

1. **`SessionRow`'s row-menu clicks were bubbling into the row's own
   `onSelect`.** `SessionRow`'s outer `<div role="button" onClick={...
   onSelect}>` wraps `.rowMenuWrap`; the design's trigger button had its own
   `e.stopPropagation()`, but the *menu items themselves* (Close/Resume/
   Delete, rendered by `SessionActionsMenu` once open) didn't — so clicking
   any of them also fired `onSelect(session.id)` on the way up. Fixed by
   moving `stopPropagation` to `.rowMenuWrap`'s own `onClick`, which covers
   the trigger and every menu item with one handler; removed the
   now-redundant one on the trigger itself.
2. **Query ambiguity from `SessionRow`'s outer `div[role="button"]`.**
   Because that div has no `aria-label`, its accessible name is computed
   from *all* descendant text — once the row gained a nested `⋯ actions`
   button (and, when its menu is open, "Resume"/"Delete" text), any
   substring/regex role query for "Resume" or "Delete" anywhere on the page
   also matches the row div itself, and (in the full e2e suite, where
   parallel tests share one backend and thus one another's rail rows) other
   sessions' resume affordances too. Scoped every such query to the open
   menu's own `<ul>` (`within(screen.getByRole("list"))` in unit tests,
   `page.getByRole("list")` in `sessions.spec.ts`). This also broke one
   *pre-existing, unrelated* assertion in `sessions.spec.ts`'s first test
   (`rail.getByRole("button", { name: goal })`, matching both the row and
   the new trigger) — fixed it in place using the `getByText(goal, {
   exact: true })` pattern the third test already used for the same reason,
   rather than leaving that journey red.

Also: this repo has no `@testing-library/jest-dom` installed (`toBeDisabled`
doesn't exist here — confirmed via `grep -rn toBeDisabled`), so unit-test
disabled-state assertions use the `(el as HTMLButtonElement).disabled`
pattern already established elsewhere in this codebase (e.g.
`TaskCard.test.tsx`, `PublishDialog.test.tsx`) rather than jest-dom
matchers.

Everything else — `sessionLifecycle.ts`'s reason strings, the "keep both"
call on the rail's existing Resume quick-affordance, the Close-disabled
wording ("Session is already inactive"), and the in-place extension of the
third `sessions.spec.ts` test rather than a fourth — follows the Open
Decisions section as proposed, unchanged.

Verified green: `go test -race ./...`, `golangci-lint run ./...`, `npm run
lint`, `npm run test` (847 tests), and `npm run test:e2e` (all 13 specs, run
twice back-to-back to rule out the flakiness the parallel-session query
ambiguity above could have caused).
