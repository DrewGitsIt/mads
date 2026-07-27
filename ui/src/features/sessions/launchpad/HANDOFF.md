# HANDOFF — Launchpad split Start button with link-at-create (tsk_419872a903b03526)

Architect stage output. UI-only task: the server side already landed in
tsk_dabe3ab30473b0dd (merged at d3ef241) — `POST /sessions` accepts optional
`links: []LinkRequest` where exactly one endpoint is
`contracts.NewSessionPlaceholder = "$new"` (pkg/contracts/link.go:115), creates
the links between `data.CreateSession` and `sup.SpawnSession`, rolls back the
whole create on validation failure, and returns the resolved `links` in the 201
body. Flag-off, a non-empty `links` field is a 400. This task threads the UI
through to that field.

Everything is gated on `FLAGS.linkedSessions` (ui/src/featureFlags.ts), read
directly per render — same pattern as SessionRail.tsx:43. No new flag.

## Shape of the feature

- The Start button in Launchpad becomes a split control when the flag is on:
  primary half submits as today; a downward-arrow half opens a one-item menu,
  "Link to session(s)…", which opens the existing `LinkChooserDialog` in a new
  *staged* mode.
- Staged mode shows a phantom holder row — "This session (not started yet)" —
  pre-selected as holder, plus a checkbox list of existing sessions to pick as
  subjects. Holder radio is fixed to the phantom row in v1 (other rows render
  their radios `disabled`, not removed — the reverse direction, new session as
  subject, is a later unlock of the same picker, not a redesign).
- Confirming does NOT call `createLink`. It returns
  `LinkRequest[]` with `granting_session_id: "$new"` to the Launchpad, which
  renders them as removable chips near the composer footer and relabels the
  button "Start & link".
- Submit includes `links: stagedLinks` in the create payload. Success clears
  chips; failure keeps them (whole create failed server-side, nothing to undo)
  and surfaces the server message via the existing error Toast.
- Removing all chips reverts the label to "Start session". Flag off: exactly
  today's single button, no arrow.

## Files to touch

### 1. `ui/src/router/types.ts`
- Add next to `LinkRequest` (~line 296):
  ```ts
  // mirrors pkg/contracts/link.go NewSessionPlaceholder — the to-be-created
  // session's slot in a staged LinkRequest on POST /sessions.
  export const NEW_SESSION_PLACEHOLDER = "$new";

  export type CreateSessionPayload = Partial<Session> & { links?: LinkRequest[] };
  ```

### 2. `ui/src/router/client.ts`
- `createSession` (interface line ~148, impl line ~645): payload type becomes
  `CreateSessionPayload`; return type becomes
  `Promise<{ session: Session; addr: string; links?: Link[] }>`. The response
  `links` are not consumed by the UI — rail badges come from `useSessionLinks`,
  which refetches on `link.created` events and on the session-id-set change
  (ui/src/features/sessions/state/useSessionLinks.ts), so the new session
  arrives already badged with no extra wiring. Type it anyway to match the API.

### 3. `ui/src/features/sessions/state/useSessionsList.ts`
- Widen `createSession` (lines 11 and 60) from `Partial<Session>` to
  `CreateSessionPayload`. No behavior change.

### 4. `ui/src/features/sessions/rail/LinkChooserDialog.tsx`
Add a staged mode to the existing dialog (do not fork it):
- New optional props:
  ```ts
  /** Launchpad staged mode: phantom "$new" holder, confirm returns requests instead of POSTing. */
  stagedHolder?: {
    label: string; // "This session (not started yet)"
    onStage: (requests: LinkRequest[]) => void;
  };
  /** Staged mode only: seeds when reopening to edit an existing staged selection. */
  initialSubjectIds?: string[];
  initialPreset?: "monitor" | "leader";
  ```
  `onCreated` should become optional (unused in staged mode). `routerClient`
  stays required — Launchpad already imports the `router` singleton.
- Staged mode behavior (active when `stagedHolder` is set):
  - Subject picking happens inside the dialog: render a checkbox list built
    from the `sessions` prop (rail mode passes pre-selected `sessionIds` and
    renders no checkboxes — unchanged). Internal `checkedIds` state, seeded
    from `initialSubjectIds`. Caller passes `sessionIds={[]}`.
  - Holder picker rows = phantom row first (label from `stagedHolder.label`,
    radio checked) then each checked session with radio `disabled`. Keep the
    single `.map` over rows so un-disabling later is the whole unlock.
  - `handleCreate` in staged mode: build
    `checkedIds.map((id) => ({ granting_session_id: NEW_SESSION_PLACEHOLDER, subject_session_id: id, preset }))`,
    call `stagedHolder.onStage(requests)`, no network, no `submitting` state.
  - Confirm button label: `Stage N link(s)`; disabled while `checkedIds` is
    empty. Preset cards and the constraint copy render as-is in both modes.

### 5. `ui/src/features/sessions/launchpad/Launchpad.tsx`
- New optional prop `sessions?: Session[]` (default `[]`) — the dialog's
  pickable pool. Optional keeps every existing test render-call compiling.
- New state: `stagedLinks: LinkRequest[]`, `menuOpen`, `linkDialogOpen`.
- In `styles.sendGroup`, flag on: replace the single Button with a split
  control — primary Button (label `stagedLinks.length > 0 ? "Start & link" :
  "Start session"`, same `disabled`/`onClick`) plus an arrow Button
  (`aria-label="More start options"`, `aria-haspopup="menu"`) as the `anchor`
  of the existing `Popover` component (ui/src/components/Popover). Menu has
  one `role="menuitem"` entry, "Link to session(s)…", disabled with a hint
  when `sessions.length === 0`; clicking it closes the menu *before* opening
  the dialog (both listen for document-level Escape — don't leave them
  stacked). Flag off: render exactly the current single Button so existing
  submit/keyboard tests pass untouched.
- Staged chips: when `stagedLinks.length > 0`, render a chip row inside
  `styles.box` under `styles.actions` (near the composer footer). One chip per
  link — `Leader of <name>` / `Monitoring <name>`, names resolved from the
  `sessions` prop with the "Untitled session" fallback — each with a remove
  button (`aria-label="Remove link to <name>"`). Model it on
  `AttachmentChips.tsx`; a sibling `StagedLinkChips.tsx` is the right size.
- Dialog render:
  ```tsx
  {FLAGS.linkedSessions && linkDialogOpen && (
    <LinkChooserDialog
      sessionIds={[]}
      sessions={sessions}
      routerClient={router}
      stagedHolder={{
        label: "This session (not started yet)",
        onStage: (reqs) => { setStagedLinks(reqs); setLinkDialogOpen(false); },
      }}
      initialSubjectIds={stagedLinks.map((l) => l.subject_session_id)}
      initialPreset={stagedLinks[0]?.preset}
      onClose={() => setLinkDialogOpen(false)}
    />
  )}
  ```
  Reopening seeds the current selection; confirming replaces `stagedLinks`
  wholesale.
- `submit()`: spread `...(stagedLinks.length > 0 ? { links: stagedLinks } : {})`
  into the `createSession` payload; on success also `setStagedLinks([])`. On
  failure keep the chips — the server rolled back the whole create, so the
  staged state is still what the user wants to retry after fixing the cause.
  The existing catch/Toast already shows `err.message`; `RouterValidationError`
  reaches it too (its `.message` is generic — acceptable v1, the dialog isn't
  open at submit time to host field errors).

### 6. `ui/src/features/sessions/SessionsSurface.tsx`
- Pass `sessions={sessions}` to `<Launchpad>` (line ~131).

### 7. `docs/LINKED-SESSIONS-DESIGN.md`
- Replace lines 119–120 ("Link creation UX: multi-select sessions in the rail →
  Link → choose preset. Sessions must already exist.") with the two paths:
  (a) rail multi-select over existing sessions, unchanged; (b) Launchpad split
  "Start session" button → "Link to session(s)…" stages leader/monitor links
  against the `"$new"` placeholder, submitted via POST /sessions `links` — new
  session as holder only in v1, holder-picker shape retained so
  new-session-as-subject is a later unlock.

### 8. CSS
- `Launchpad.module.css`: `.splitButton` (join the two buttons visually),
  menu/menu-item styles for the Popover content, `.linkChips` row.
- `LinkChooserDialog.module.css`: subject checkbox list, disabled holder-row
  treatment, phantom-row emphasis.

## Tests

- `Launchpad.test.tsx` — follow SessionRail.test.tsx's pattern of mutating
  `FLAGS.linkedSessions` directly; reset it in `afterEach` (module-global —
  a leak fails unrelated tests):
  1. Flag off → no "More start options" button (acceptance criterion).
  2. Flag on → arrow → menu → dialog; check a session, confirm → chip renders
     and button reads "Start & link".
  3. Submit sends `links: [{ granting_session_id: "$new", subject_session_id:
     "s1", preset: "leader" }]` and clears the chips on success.
  4. Removing every chip reverts the label and the payload carries no `links`.
  5. Enter-to-submit includes staged links (keyboard path shares `submit()`).
- `LinkChooserDialog.test.tsx` — staged mode: phantom row present and
  preselected, other holder radios disabled, confirm calls `onStage` with
  placeholder requests and performs no fetch; rail-mode tests unchanged.

## Risks / non-obvious constraints

- **Server-side link creation is the point.** Do not "improve" this into
  client-side `createLink` after create — `setupOrchestrate` runs once at child
  boot, so post-create linking races the spawn and the leader silently misses
  its orchestrate tool. That race is why the payload field exists.
- The payload shape is a generic `LinkRequest[]` where either endpoint may be
  `"$new"` — the v1 UI only ever emits `granting_session_id: "$new"`, but don't
  narrow the types to that.
- A staged subject can be deleted between staging and submit; the server 400s
  and rolls back the whole create by design. Chips survive so the user can
  remove the stale one. Acceptable v1; don't add pre-validation.
- Flag toggled off in Settings after staging → server 400 with a clear message;
  the Toast covers it. Not worth extra handling.
- ui-smoke e2e has ~11 pre-existing baseline failures unrelated to this diff —
  compare against baseline before treating a red run as caused by this change.
- Delete this file before merge (pipeline convention).
