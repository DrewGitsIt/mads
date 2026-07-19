# Task: Session close: rail moves the session to Recent immediately (tsk_5cfe56360d366386)

## Context: this is a redo

A prior executor pass on this task committed `5bca417` ("Fix SSE bus resume
cursor to be per-type, not a single scalar") and left a HANDOFF.md claiming
`STAGE: pass`. The code reviewer failed that stage: the e2e assertion added
passed identically against both the pre-fix and post-fix `events.ts`, so it
never actually located the bug, and the events.ts fix — while real — wasn't
shown to cause the reported symptom given the rail's actual subscription
wiring. This HANDOFF replaces that one; `5bca417` is left in the branch
history as a legitimate, independent fix, not reverted.

## What I found

Confirmed the reviewer's finding by re-deriving it independently: both
`App.tsx`'s `useCommandPaletteData()` and `SessionsSurface.tsx` mount their
own separate `useSessionsList()` instance, and both subscribe with the full
`SESSION_EVENT_TYPES` set from t=0 (`App.tsx`'s `activeId` defaults to
`"sessions"`, so `SessionsSurface` — and thus the rail's own instance — is
mounted from the start). That means the specific failure mode `events.ts`'s
fix addresses (a later subscriber widening the server-side type filter to a
type the shared connection never carried) never actually happens for the
rail today — there's no widen to race. I verified this empirically too: with
a correctly-built stack (see "build gotcha" below), the close journey
behaves identically whether `events.ts` has the scalar cursor (pre-fix) or
the per-type cursor (post-fix). Both are consistent — the events.ts change
is a real, isolated defect, but not this bug's cause.

**Actual root cause**: `useSessionsList.refetch()`
(`ui/src/features/sessions/state/useSessionsList.ts`) has no request-
ordering guard. It's called unconditionally on every `session.*` SSE event,
and — because two independent `useSessionsList` instances are mounted
simultaneously (the rail's own and the command palette's) — a single event
fires more than one overlapping `GET /sessions` call. Nothing guarantees
those requests resolve in the order they were issued. A slower,
earlier-issued request (e.g. from an unrelated `session.created` a moment
before) can resolve *after* a faster, later-issued one (e.g. the close's own
`session.activity_changed`-triggered refetch) and silently overwrite the
just-set correct state with the stale snapshot it captured. Because no
further SSE event necessarily arrives afterward, nothing re-triggers a
correction — the rail is stuck showing the old activity until something else
causes a fresh, unraced fetch, e.g. a full page reload. This matches the
reported symptom ("stays stale... until the whole UI is restarted")
precisely.

I confirmed the backend chain the architect listed (kebab close ->
`router.closeSession` -> `POST /sessions/{id}/terminate` ->
`Supervisor.TerminateSession` -> child exit handling ->
`dataclient.UpdateSession` -> `session.activity_changed`) is correct and
synchronous with no race — `TerminateSession`/`onSessionExit`/
`reconcileInactiveSession` (`services/router/internal/supervisor/
supervisor.go`), `child.Spawn`/`Terminate` (`services/router/internal/
child/child.go`), and the session process's SIGTERM handler (`services/
session/main.go`) all block correctly. `go test -race ./...` passes clean.
The defect is entirely client-side.

## What I did

- Added a `latestRequest` ref-based monotonic request-id counter in
  `useSessionsList.ts`. Each call to `refetch()` captures its own id at
  entry; all three state-mutating branches (`setSessions`, `setError`,
  `setLoading`) are gated on `requestId === latestRequest.current` in
  addition to the existing `mounted.current` check, so only the most
  recently *issued* request's response can ever commit to state, regardless
  of resolution order.
- Added a focused unit test to `useSessionsList.test.ts` using a
  deferred-promise pattern to force an earlier-issued request to resolve
  after a later one, proving the stale-overwrite bug and the fix.
  Confirmed via `git stash`/`git stash pop` on just this file: the test
  fails without the fix (`expected 'active' to be 'inactive'`) and passes
  with it.
- Added a new e2e test to `ui/e2e/sessions.spec.ts`,
  `"sessions journey: close survives an out-of-order rail refetch"`, that
  reproduces the race against the real backend: `page.route()` intercepts
  every `GET /sessions`, capturing (via `route.fetch()`) and holding each
  response instead of letting it land. It triggers an unrelated
  `session.created` (via `createSessionNoSpawn`, which writes directly to
  the Data Service, bypassing the page's own fetch) to produce an
  earlier-issued "batch A" of held requests, then closes the real session
  via the kebab to produce a later-issued "batch B", then releases batch B
  first and asserts the rail regroups the row under Recent, then releases
  batch A (the stale batch) and asserts the rail does *not* regress.
  I deliberately didn't assume exactly one `GET /sessions` per triggering
  action — since both `useSessionsList` instances (rail + command palette)
  refetch independently, one SSE event produces a *batch* of requests, so
  the test waits for the held-request count to stabilize rather than
  hard-coding an exact count.
- Confirmed via the same stash technique (applied to the e2e run): this new
  e2e test times out/fails against the pre-fix `useSessionsList.ts` (rail
  stuck showing the session active) and passes with the fix.
- Left the pre-existing bounding-box regroup assertion in `"sessions
  journey: close, resume affordance, delete"` untouched — it's valid
  coverage of the non-racy path but, as the reviewer found, doesn't
  discriminate pre-fix from post-fix code, so it isn't a substitute for the
  new test.

## What I deliberately did NOT do

- Did not revert or modify `5bca417`'s `events.ts` change. It's a real,
  independently-valid fix for a genuine defect class (a scalar cursor
  masking never-delivered events of a newly-widened type); it's just not
  what causes today's reported symptom. Reverting it would be removing a
  legitimate fix, and keeping it costs nothing.
- Did not touch the redundant double-mount of `useSessionsList` (one in
  `SessionsSurface`, one in `useCommandPaletteData`). It's what turns a
  single SSE event into a *batch* of refetches, which is what makes the
  race easy to hit in practice, but de-duplicating the two hook instances
  into one shared source would be a much larger structural change than this
  bug needs — the request-ordering guard fixes the actual defect
  regardless of how many redundant refetches are in flight. Worth flagging
  as a follow-up (fewer redundant network calls generally), not fixing
  here.
- Did not add any general polling/resilience fallback for the SSE
  connection. Out of scope for this bug; the fix is purely about not
  letting an out-of-order response clobber fresh state.

## What the reviewer should scrutinize

- **The e2e test's batching logic.** Rather than asserting an exact request
  count per action, it polls until the held-request count stabilizes
  (`waitForRequestBatch`, quiet period 300ms). This is robust to the current
  reality of two independent `useSessionsList` mounts, but is inherently a
  bit more permissive/racy-feeling than a hard-coded count — worth checking
  the timing constants are generous enough not to flake under load, and
  that the quiet-period approach doesn't mask a *third* mount being added
  later without anyone noticing the assumption changed.
- **`page.allowStatus(sessionId)` in the new test.** Closing the session
  terminates its child; `ConversationTab`'s still-mounted live-output SSE
  subscription then 404s against the now-childless session — same expected
  race documented by the pre-existing "close, resume affordance, delete"
  and "wakes a dormant session" tests. I followed the same pattern; worth
  confirming it's not masking something new.
- **Why the fix lives in `useSessionsList.ts` and not in a more general
  "cancel stale requests" utility.** I judged a shared abstraction was not
  warranted for a single call site — if a similar pattern shows up
  elsewhere (`useAllTasks`, `useOpenTaskCounts`, etc.), it may be worth
  factoring out then, not now.

## Test results

- `npx tsc --noEmit` (ui) — clean.
- `npm run lint` (ui) — clean.
- Full UI vitest suite — 199 files / 1033 tests pass, including the new
  `useSessionsList.test.ts` case and the unmodified `events.test.ts` (11
  tests, all pass — confirms the SSE bus's single-shared-connection/widen-
  reconnect behavior, the closest codified form of the "<=2 control-plane
  streams per window" census guard, is unaffected).
- `go build ./...`, `go vet ./...`, `go test -race ./...` — all pass, no
  backend changes were made.
- `make ui-smoke` (full Playwright suite, correctly built with
  `VITE_ROUTER_URL=""`) — 28/28 tests pass, including the new race test and
  the pre-existing close/resume/delete journey.
- New e2e test independently confirmed to fail (timeout, rail stuck showing
  the session active) against the pre-fix `useSessionsList.ts` via
  `git stash`, and pass after `git stash pop` restored the fix — same
  before/after discipline applied to the unit test.

STAGE: pass
