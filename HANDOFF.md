# Lifecycle controls that work: Close keyed to real state, Restart agent with feedback, recover the stream after bounce

Task: tsk_8c8618c17c68dbb7

## What was done

1. **Close is never gated on `session.activity` anymore.**
   - `sessionLifecycle.ts`: `closeState()` dropped its `activity` parameter
     and always returns `{ disabled: false }`. `resumeState`/`deleteState`
     untouched — they weren't part of the bug.
   - `SessionActionsMenu.tsx`: calls `closeState()` with no arg.
   - `SessionRail.tsx`: the rail card's Close button is now rendered
     whenever `onClose` is provided, not gated behind `!isPaused`. Resume's
     gating (still `isPaused`) was left as-is — not part of the report.
   - Rationale: `Supervisor.TerminateSession` has no activity guard — it's
     safe/idempotent against a live child or an already-inactive record
     alike. The record is a cache that can drift from the real child
     state, so it must never be what hides the one control that recovers
     from that drift.

2. **Restart Agent now behaves like its sibling actions.**
   - `SessionWorkspace.tsx`: new `restarting` state; `restartAgent()`
     awaits `routerClient.bounce()`, disables/relabels the menu item to
     "Restarting Agent…" while in flight, routes rejection into the
     existing `actionError` toast, and on success calls `refetchSession()`
     plus `requestStreamReattach(sessionId)`.
   - `SessionActionsMenu.tsx`: new `restartingAgent?: boolean` prop drives
     the disabled/label state of the Restart Agent button.

3. **Stream reattaches after a successful bounce even if the record still
   says "inactive".**
   - New `ui/src/conversation/streamReattachSignal.ts`: a `window`
     `CustomEvent` scoped by `sessionId`, mirroring the existing
     `shellToggleSignal.ts` pattern (used because `SessionWorkspace` and
     `ConversationTab` are several component layers apart with no shared
     state to lift into).
   - `ConversationTab.tsx`: new effect listens for the signal and forces
     `setStreamBroken(false); setStreamGeneration(g => g + 1)`
     unconditionally — independent of the existing passive
     `streamBroken && activity === "active"` reconnect effect, which can
     never fire in the drifted state.

4. **Test coverage** added/rewritten across `SessionWorkspace.test.tsx`,
   `SessionRail.test.tsx`, and `ConversationTab.test.tsx`:
   - Close is offered and functional while the cached record says
     inactive (both rail and workspace kebab).
   - Close/terminate failure surfaces in the action-error toast.
   - Restart Agent shows a pending state and clears it on completion.
   - Restart Agent failure surfaces in the action-error toast rather than
     being swallowed.
   - Restart Agent success refetches the session and re-attaches the
     stream (new `EventSource` opened), even with `activity: "inactive"`
     cached.
   - `ConversationTab` force-reattaches only for a matching `sessionId`
     signal, ignoring signals for other sessions.

## What was not done

- No backend/Go changes. `Supervisor.TerminateSession` and
  `BounceSession` in `services/router/internal/supervisor/supervisor.go`
  already have the semantics this task assumes (terminate is
  state-agnostic; bounce gives up silently on terminate failure) — this
  was purely a UI trust problem, confirmed by reading the supervisor
  source before touching anything.
- The companion "activity-truth" problem (why the cached record drifts
  in the first place) is out of scope — this task only makes the UI
  stop trusting that record for lifecycle gating, not fix the drift's
  root cause.
- `resumeState`/`deleteState` gating logic was left untouched; the report
  only described Close as broken.

## What a reviewer should scrutinize

- **`streamReattachSignal.ts`'s cross-window-event approach.** It's
  scoped by `sessionId` for correctness, but in the current app shell
  only one `SessionWorkspace`/`ConversationTab` pair is ever mounted at a
  time (verified via `tabRegistry.tsx`/`SessionsSurface.tsx`). If that
  assumption changes (e.g. multi-pane), this still works but is worth a
  second look.
- **`SessionRail`'s "paused" row now shows both Resume and Close
  simultaneously.** That's intentional per the acceptance criteria (never
  hide the escape hatch), but it's a UX change worth a glance — the two
  buttons sit side by side on an inactive row now.
- `make check` passes cleanly (golangci-lint 0 issues, `go test -race
  ./...` all ok, `npm run lint` clean, 217 files / 1278 tests passed).
