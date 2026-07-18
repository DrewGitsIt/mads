# Duplicate-request storm: 89 GETs to /sessions/{id} in ~17ms

## Root cause (found by code inspection, not by running the reviewer's trace tool — see note below)

`ConversationTab.tsx`'s `WORKSPACE_SESSION_EVENT_TYPES` subscription
(current lines ~150-161) has an unconditional catch-all:

```ts
useEffect(() => {
  return subscribeToEvents(
    { sessionId, types: WORKSPACE_SESSION_EVENT_TYPES },
    (envelope) => {
      if (envelope.type === "link.message_injected") {
        void routerClient.getTranscript(sessionId).then(setMessages);
      } else {
        refetchSession();   // <-- fires for EVERY other type in the array
      }
    },
  );
}, [refetchSession, routerClient, sessionId]);
```

`WORKSPACE_SESSION_EVENT_TYPES` (`ui/src/router/sessionEventTypes.ts`) is a
big union that includes types that have nothing to do with the `Session`
object — `task.created`, `task.updated`, `task.status_changed`,
`note.created`, `stage.started/completed/failed/skipped`, `file.changed`,
`pipeline.*` — because it's also the shared subscription
`usePipelineRunning`/the Pipeline tab's fold rides on (one SSE connection
per window, see the SSE-consolidation work that landed just before this
task, `2a506c7`/`760572c`). Since `760572c`, a burst of those pipeline/task/
file envelopes (e.g. a work item landing and touching many files, or a
pipeline run creating/updating several tasks in quick succession) is
delivered to every subscriber of the shared bus almost back-to-back. Every
single one of those envelopes falls into ConversationTab's `else` branch
and calls `refetchSession()` → `routerClient.getSession(sessionId)` —
one duplicate GET per unrelated event, all within the same tick/burst.
That's the 89-in-17ms shape: not a render loop, not multiple hooks each
calling `getSession` — a single hook whose event handler isn't scoped to
the event types it actually cares about.

**This is a known-fixed anti-pattern one file over.** `SessionWorkspace.tsx`
(lines ~73-89) subscribes to the *exact same* `WORKSPACE_SESSION_EVENT_TYPES`
array but scopes its refetch explicitly:

```ts
if ((RUN_EVENT_TYPES as string[]).includes(envelope.type)) {
  setRunning(envelope.type === "pipeline.started");
} else if ((SESSION_SHELL_REFETCH_EVENT_TYPES as string[]).includes(envelope.type)) {
  refetchSession();
}
// Anything else in WORKSPACE_SESSION_EVENT_TYPES (task.*/stage.*/
// file.changed/note.created) is there only for usePipelineRun's shared
// subscription — this shell has nothing to do with it.
```

`SESSION_SHELL_REFETCH_EVENT_TYPES` (same file,
`ui/src/router/sessionEventTypes.ts`) is already exported and already the
correct "does the Session object itself need refetching" subset
(`session.*` lifecycle + `link.message_injected` + `merge.*`).
ConversationTab was just never updated to use it — a straightforward
oversight, not an intentional difference in behavior between the shell and
the tab.

## Fix (source-level, no RouterClient dedupe/cache needed)

The fan-out here is **not structural** — it's one hook with an unscoped
`else`. A RouterClient-layer in-flight coalescer would paper over this
correctly but is unnecessary machinery for what's a one-line scoping bug;
don't add it.

In `ui/src/conversation/ConversationTab.tsx`:
1. Import `SESSION_SHELL_REFETCH_EVENT_TYPES` alongside the existing
   `WORKSPACE_SESSION_EVENT_TYPES` import from `../router/sessionEventTypes`.
2. Change the subscription handler's `else` to
   `else if ((SESSION_SHELL_REFETCH_EVENT_TYPES as string[]).includes(envelope.type)) { refetchSession(); }`
   — mirroring `SessionWorkspace.tsx`'s pattern verbatim, including a
   comment noting the remaining types in `WORKSPACE_SESSION_EVENT_TYPES`
   (task.*/stage.*/file.changed/note.created/pipeline.*) are only there for
   other subscribers sharing this bus and this tab has nothing to do with
   them.

No other file needs to change. `link.message_injected` still refetches the
transcript via its own `if` branch first (unaffected — it's checked before
the `SESSION_SHELL_REFETCH_EVENT_TYPES` membership check, same as today).

## Regression guard

Add to `ui/src/conversation/ConversationTab.test.tsx`, following the
existing `FakeEventSource`/`source.emit(...)` pattern already used in this
file (see the provisioning test) and in
`ui/src/features/sessions/workspace/SessionWorkspace.test.tsx`'s
`pipeline.started`/`pipeline.completed` test:

1. Render `ConversationTab` with a `FakeRouterClient`, `vi.spyOn(client,
   "getSession")`, wait for initial mount to settle, then `mockClear()` the
   spy (drop the one legitimate mount-time call).
2. Find the shared bus's `FakeEventSource` instance (e.g.
   `FakeEventSource.instances.find((s) => s.url.includes("task.status_changed"))`).
3. Inside `act(...)`, emit 89 `task.status_changed` (or a mix of
   `file.changed`/`stage.completed`) envelopes back-to-back with
   increasing `seq`, same `session_id`.
4. Assert `getSessionSpy` was **not called** (bounded at 0 additional
   calls, not just "fewer than 89") — this is the arc that produced the
   storm, so it should produce none.

Optionally also assert the inverse on the same subscription (a
`session.activity_changed` envelope *does* still trigger exactly one
refetch) so the fix's scoping is verified both ways — though the existing
"provisioning strip clears on session.activity_changed" test already
exercises this path indirectly (it requires `refetchSession` to have run
for the session's `activity` to reach `"active"`).

## Note on "reproduce via the e2e trace tooling the reviewer used"

No committed trace/request-counting tool was found in this repo (checked
`ui/e2e/**`, `ui/e2e/lib/**`, `router/events.ts`, `router/client.ts`) or in
the flagging task's own branch (`mads/tsk_15aa8fee6de66967` — diffed against
its actual merge-base `24342db`, not `main`, since that branch has since
drifted; its diff only touches the dependency-picker feature + a narrower
`useSessionLinks.ts` effect-identity fix, unrelated to this storm). The
reviewer's 89-GET/17ms finding was presumably observed via ad hoc devtools/
Playwright trace inspection during manual review, not a reusable harness in
this repo. This task's own acceptance criteria explicitly allow the
component-test fallback ("or instrument fetch in a component test"), which
is the approach above — it exercises the real subscription/handler code
path deterministically instead of trying to reconstruct an ad hoc manual
repro.

## Files to touch

- `ui/src/conversation/ConversationTab.tsx` — scope the refetch branch (the fix).
- `ui/src/conversation/ConversationTab.test.tsx` — add the regression guard test.

## Risks / things to double check while implementing

- Double-check `SESSION_SHELL_REFETCH_EVENT_TYPES` import path/name hasn't
  drifted from `ui/src/router/sessionEventTypes.ts` before wiring it in.
- Make sure the `link.message_injected` check still comes *before* the
  `SESSION_SHELL_REFETCH_EVENT_TYPES` membership check, since that type is
  a member of both — transcript refetch must win, same as today.
- Run `make check` and `make ui-smoke` after the change (untouched by this
  design pass) to confirm no other test relied on the old catch-all
  refetching-on-everything behavior.

## Executor notes

Implemented exactly as designed above — no deviations.

- `ConversationTab.tsx`: imported `SESSION_SHELL_REFETCH_EVENT_TYPES`
  alongside `WORKSPACE_SESSION_EVENT_TYPES`; changed the subscription
  handler's unconditional `else { refetchSession(); }` to
  `else if ((SESSION_SHELL_REFETCH_EVENT_TYPES as string[]).includes(envelope.type)) { refetchSession(); }`,
  mirroring `SessionWorkspace.tsx` verbatim (including the trailing comment
  noting the remaining `WORKSPACE_SESSION_EVENT_TYPES` members are only
  there for `usePipelineRun`'s shared subscription). `link.message_injected`
  is still checked first, unchanged.
- `ConversationTab.test.tsx`: added two tests. (1) "does not refetch the
  session for a burst of pipeline/task/stage/file events unrelated to the
  Session object" — mounts, clears the spy, grabs the shared bus's
  `FakeEventSource` instance via `.url.includes("task.status_changed")`
  (same pattern as the existing provisioning-strip test), emits 89
  `task.status_changed` envelopes with increasing `seq` inside one `act()`,
  and asserts `getSession` was called exactly 0 times — this is the arc
  that produced the 89-GET storm, now bounded at 0. (2) "still refetches
  the session on a session.activity_changed event" — the inverse check,
  confirming the fix didn't over-scope and break the legitimate refetch
  path. Both pass; full suite is 26/26 for this file, 978/978 (195 files)
  for `make check`, and 21/21 e2e specs for `make ui-smoke`.

**What I did not do:** did not touch `ui/e2e/**` or add a new trace/
request-counting harness — per the design note above, none existed in this
repo to reproduce against, and the acceptance criteria explicitly allow the
component-test fallback used here. Did not add any RouterClient-layer
dedupe/coalescing — confirmed, per the design rationale, that the fan-out
was a single unscoped event handler, not a structural fan-out needing one.

**For the reviewer to scrutinize:** the root cause here was found by static
inspection (diffing `ConversationTab.tsx`'s handler against the already-
correct `SessionWorkspace.tsx` one), not by reproducing the original 89-GET
trace end-to-end — worth independently confirming the shape matches what
was actually observed (a burst of `task.*`/`stage.*`/`file.changed`
envelopes hitting a *mounted* Conversation tab) rather than some other path
reaching the same GET count.
