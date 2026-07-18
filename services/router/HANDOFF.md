# HANDOFF: Terminate must reconcile orphaned-active sessions

## Executor notes (implementation complete)

Implemented exactly as designed below — no deviations from the file list,
signatures, or call-site logic. Summary of what landed:

- `Leases.ActiveForSession` (store), `GET /sessions/{id}/lease-active` (data
  api), `dataclient.SessionActiveLease` (client) — the new read path, wired
  end to end.
- `reconcileInactiveSession`, `TerminateSession`'s no-child branch, and
  `ReconcileOrphanedSessions` (boot sweep) in supervisor.go, exactly as
  specified.
- Boot sweep wired into `main.go` right after `ctx, stop := signal.NotifyContext(...)`,
  before the digest watchers start, as directed.
- All tests from the "Tests" section below, plus one addition: the Resume
  end-to-end proof (originally sketched as `TestSessionResumeAfterOrphanReconciliation`)
  landed as `TestTerminateOrphanThenResumeEndToEnd` in
  `services/router/internal/api/sessions_test.go`, using the existing
  `newTestServerWithData` helper from `events_test.go` (same package,
  already builds a Supervisor wired to `testChildBin` — no new scaffolding
  needed).
- Both READMEs updated per the "Docs" section.

`make check` (lint, `go test -race ./...`, UI test suite) passes clean.

**Worth scrutinizing:** the asymmetry called out in "Risks" below —
`TerminateSession`'s no-child path reconciles unconditionally (no lease
check), while the boot sweep gates on one. This is intentional (explicit
user action vs. unattended startup sweep) but is exactly the kind of thing
a reviewer might flag as inconsistent without this note.

---

## The bug, precisely

`Supervisor` state (`sessions`/`pipelines` maps) is in-memory only. Every
Router restart forgets every tracked child while `Session.Activity` in the
Data Service still says `active` (it was never told otherwise — the child
that would have flipped it just vanished along with the Router process).
`TerminateSession` (`services/router/internal/supervisor/supervisor.go:119`)
returns `nil` when `s.sessions[sessionID]` is `nil` — a silent no-op — so
clicking "Close" on one of these orphaned-active sessions does nothing: no
activity flip, no `session.activity_changed` event, the UI just sits
waiting for an SSE event that will never come.

## Design decision: task write-leases are the "is an engine alive" signal

The boot sweep must not flip a session to inactive if a CLI `mads run`
(`cmd/mads/run.go`) is genuinely driving it — that process is never spawned
or tracked by the Router (`services/router/internal/supervisor` has no
knowledge of it at all) and uses `dataclient.New(*data, "pipeline")`
directly against the Data Service.

Two candidate signals were considered:

- **Recent pipeline events** (e.g. recent `task.status_changed` rows) —
  rejected. `services/pipeline/engine/engine.go:357-390` documents exactly
  why this is unsafe: an agent stage can run silently for the task's full
  duration with zero intermediate writes, so "no recent event" does not
  mean "no live engine." Using it would risk false-positive reconciliation
  (killing a genuinely running session).
- **Task write-leases** (`task_leases` table, `services/data/internal/store/leases.go`)
  — chosen. This is spec §15's own liveness mechanism (the "parking
  meter"), and critically the engine **heartbeats it independently of task
  writes**: `engine.go` renews the lease at `TTL/3` for the entire duration
  a task runs, specifically because writes alone aren't a frequent-enough
  signal. A live, unexpired lease on any task under a session is the most
  honest, lowest-latency proof that something is actively working under
  it — regardless of which actor string (`"pipeline"`, a session ID, a run
  ID) holds it.

The query needed ("does this session have any task with a currently
unexpired lease") isn't exposed today — no dataclient method reads lease
state, only claims/releases it. This requires one small new read-only
endpoint end-to-end. Full shape below.

**Note the asymmetry between the two reconciliation call sites:**
- **`TerminateSession`** (explicit user "Close" click) reconciles
  unconditionally — no lease check. It's a direct, authoritative user
  action; "idempotent truth-enforcement" per the task brief, not something
  that should be silently vetoed by background engine activity.
- **Boot sweep** (automatic, on every Router start) *does* gate on the
  lease check — nobody asked for this one, so it must never step on a
  live external run.

## Files to touch

### 1. `services/data/internal/store/leases.go` — new read method

Add alongside `ExpireStale`/`ReleaseAll`:

```go
// ActiveForSession reports whether any task under sessionID currently has
// an unexpired write-lease — the signal that a live engine (an interactive
// child, or a CLI `mads run` with no other visible liveness signal) is
// working under this session right now, independent of which actor string
// holds the lease. The heartbeat in services/pipeline/engine/engine.go
// keeps this honest even when a task is silent for its whole run: writes
// alone are not a frequent enough signal (see that file's LeaseTTL comment).
func (s *Leases) ActiveForSession(ctx context.Context, sessionID string) (bool, error) {
	var n int
	err := s.log.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_leases tl
		 JOIN tasks t ON t.id = tl.task_id
		 WHERE t.session_id = ? AND tl.expires_at > ?`,
		sessionID, s.now().Format(time.RFC3339Nano),
	).Scan(&n)
	return n > 0, err
}
```

Matches `Sessions.List`'s existing style of a direct `s.log.db.QueryContext`
read (`services/data/internal/store/sessions.go:160-185`) — no `WithTx`
needed, it's read-only. String comparison against `expires_at` mirrors
`ExpireStale`'s own approach a few lines up in the same file.

### 2. `services/data/internal/api/sessions.go` — new route

`sessionRoutes(mux, log)` currently only takes `*store.Log` and builds its
own `store.NewSessions(log)` locally; every `/sessions/{id}/...` route lives
in this file as an anonymous closure (not a `server` method), unlike the
`/tasks/...` lease routes in `api.go`. Follow that local convention — build
a second, equally-cheap `store.NewLeases(log)` inside `sessionRoutes` and
add:

```go
leases := store.NewLeases(log)
mux.HandleFunc("GET /sessions/{id}/lease-active", func(w http.ResponseWriter, r *http.Request) {
	active, err := leases.ActiveForSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": active})
})
```

No `decode`/`actor(r)` needed — pure read, no mutation, no attribution.

### 3. `pkg/dataclient/dataclient.go` — new client method

Add near `Sessions` (after it, before the "Session file tracking" section
comment):

```go
// SessionActiveLease reports whether any task under sessionID currently
// has a live write-lease — the Router boot sweep's guard against
// reconciling a session that's genuinely active under an engine it has no
// other way to see (e.g. a CLI `mads run`).
func (c *Client) SessionActiveLease(ctx context.Context, sessionID string) (bool, error) {
	var out struct {
		Active bool `json:"active"`
	}
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/lease-active", nil, &out)
	return out.Active, err
}
```

### 4. `services/router/internal/supervisor/supervisor.go` — the core fix

Add a private helper (place it near `forgetSession`/`forgetPipeline`, or
right after `TerminateSession`):

```go
// reconcileInactiveSession enforces the truth that no child is tracked
// under sessionID: if the record still says active, flips it to inactive.
// dataclient.UpdateSession itself emits session.activity_changed when
// Activity changes (services/data/internal/store/sessions.go Update) — no
// manual Append needed here, same as onSessionExit's crash-path flip
// below. Also spawns the same session-summary job a real exit would (spec
// §16: every active→inactive transition triggers one), matching
// onSessionExit's unconditional spawn. No-op if the session is already
// inactive, or doesn't exist.
func (s *Supervisor) reconcileInactiveSession(ctx context.Context, sessionID string) error {
	sess, err := s.data.Session(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Activity != contracts.SessionActive {
		return nil
	}
	sess.Activity = contracts.SessionInactive
	if err := s.data.UpdateSession(ctx, &sess); err != nil {
		return err
	}
	if s.jobs != nil {
		if _, err := s.jobs.Spawn(ctx, job.SessionSummaryKind, sessionID, job.GenerateSessionSummary(sessionID)); err != nil {
			log.Printf("supervisor: spawn session summary for %s: %v", sessionID, err)
		}
	}
	return nil
}
```

Change `TerminateSession`:

```go
// TerminateSession stops sessionID's child (expected-exit path: SIGTERM,
// grace period, then SIGKILL). Returns once the child's exit handling
// (including its lease ReleaseAll) has completed. If no child is tracked
// under sessionID, this is truth-enforcement, not a no-op: the session
// record may still say active from before a Router restart orphaned it
// (Supervisor state is in-memory only) — reconcileInactiveSession flips it
// to inactive so the UI and summary job react exactly as a real exit
// would. Does not itself emit an activity_changed "why" beyond that — the
// caller (api layer) is responsible for anything further (e.g.
// SessionDeleted).
func (s *Supervisor) TerminateSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	p := s.sessions[sessionID]
	s.mu.Unlock()
	if p == nil {
		return s.reconcileInactiveSession(ctx, sessionID)
	}
	return p.Terminate(ctx, terminateGrace)
}
```

**Do not** touch `onSessionExit`'s existing crash-path Activity flip
(lines ~258-261) to reuse this helper — that code path already
unconditionally spawns the summary job a few lines further down for every
exit (expected or not); routing it through `reconcileInactiveSession` too
would double-spawn. Leave it exactly as-is; the new helper is only for the
two orphan-reconciliation call sites (Terminate, boot sweep).

Add the boot sweep (exported, called once from `main.go`):

```go
// ReconcileOrphanedSessions flips every session marked active with no
// tracked child back to inactive — the boot-time fix for sessions
// orphaned by a prior Router process's restart (Supervisor state is
// in-memory only, spec §15). Guards against a live external engine (a CLI
// `mads run`, never spawned or tracked by this Supervisor) by checking for
// an unexpired task write-lease under the session first: a live lease
// means leave it alone and log, since flipping it would fight a genuinely
// running pipeline. Best-effort — logs and continues past per-session
// errors rather than aborting the sweep or failing Router startup.
func (s *Supervisor) ReconcileOrphanedSessions(ctx context.Context) {
	sessions, err := s.data.Sessions(ctx, string(contracts.SessionActive))
	if err != nil {
		log.Printf("supervisor: boot sweep: list active sessions: %v", err)
		return
	}
	for _, sess := range sessions {
		s.mu.Lock()
		_, tracked := s.sessions[sess.ID]
		s.mu.Unlock()
		if tracked {
			continue // a real child is live for this session; nothing to do
		}
		live, err := s.data.SessionActiveLease(ctx, sess.ID)
		if err != nil {
			log.Printf("supervisor: boot sweep: check active lease for %s: %v", sess.ID, err)
			continue // can't prove it's safe to flip; leave active rather than risk killing a live run
		}
		if live {
			log.Printf("supervisor: boot sweep: session %s has a live task lease (external engine); leaving active", sess.ID)
			continue
		}
		if err := s.reconcileInactiveSession(ctx, sess.ID); err != nil {
			log.Printf("supervisor: boot sweep: reconcile %s: %v", sess.ID, err)
			continue
		}
		log.Printf("supervisor: boot sweep: reconciled orphaned-active session %s to inactive", sess.ID)
	}
}
```

### 5. `services/router/main.go` — call the sweep at startup

After `sup := supervisor.New(...)` / `jobs.SetResolver(sup)`, and once `ctx`
exists (`ctx, stop := signal.NotifyContext(...)`), run the sweep
synchronously but bounded, before the server starts accepting connections —
best-effort, matching this file's existing credstore/vault-refs "never
fatal" philosophy:

```go
sweepCtx, sweepCancel := context.WithTimeout(ctx, 30*time.Second)
sup.ReconcileOrphanedSessions(sweepCtx)
sweepCancel()
```

Place it after `defer stop()`, before `watcher := digestwatch.NewWatcher(...)`.

### 6. Resume — verify, don't necessarily change

`handleResumeSession` (`services/router/internal/api/sessions.go:60-81`)
already checks both `sess.Activity == contracts.SessionActive` (409) and
`s.sup.SessionAddr(id)` tracked (409). This is *why* Resume is currently
broken on an orphaned-active session: the stale `Activity == active`
record blocks it even though nothing is tracked. Once `TerminateSession`
(or the boot sweep) reconciles Activity to inactive, Resume's existing
logic should just work — no code change expected here. Write the
end-to-end test below to prove it rather than assuming.

## Tests

### `services/data/internal/store/leases_test.go`

`TestActiveForSessionReflectsLiveLease` — using `newLeaseStore`/`mkTask`
(existing helpers in this package):
- Two tasks in different sessions (`mkTask(t, tasks, contracts.Task{Title: "A", SessionID: "ses_x"})`).
- Claim one with a long TTL; assert `ActiveForSession("ses_x")` → `true`,
  `ActiveForSession("ses_y")` (the other session, untouched) → `false`.
- Swap `leases.now` forward past the TTL (same pattern as
  `TestClaimOverExpiredLease`/`TestClaimRenewRelease`); assert
  `ActiveForSession("ses_x")` → `false` once expired.

### `services/data/internal/api/leases_test.go`

`TestSessionLeaseActiveOverHTTP` — using `newTestServer`/`doJSON` (existing
helpers): create a task under a session, `GET /sessions/{id}/lease-active`
→ `{"active": false}`; claim it; same GET → `{"active": true}`.

### `services/router/internal/supervisor/supervisor_test.go`

Follow the file's existing conventions exactly (`newSupervisor`,
`datatest.Start`, `eventually`, real `job.Runner` with `fake`/`fastmodel`
where a summary needs proving — see `TestTerminateSessionTriggersSessionSummary`
for the pattern).

- **`TestTerminateReconcilesOrphanedActiveSession`** — `data.CreateSession`
  directly (never call `sup.SpawnSession`, simulating the post-restart
  orphan: record active, nothing tracked). `sup.TerminateSession(ctx,
  sess.ID)` → nil error. Assert `data.Session(...).Activity ==
  contracts.SessionInactive`, and assert a `session.activity_changed`
  event landed (`admin.Events(ctx, 0, sess.ID,
  []string{"session.activity_changed"})`, non-empty — no polling needed,
  `UpdateSession` has already returned by the time `TerminateSession`
  does).
- **`TestTerminateOrphanedActiveSessionTriggersSessionSummary`** — same
  orphan setup, but wire a real `job.Runner` (mirror
  `TestTerminateSessionTriggersSessionSummary`'s fake-scenario setup) and
  assert the summary eventually lands, proving the "summary job reacts
  exactly as for a real exit" requirement, not just the activity flip.
- **`TestBootSweepReconcilesOrphanedActiveSession`** — three sessions,
  one `ReconcileOrphanedSessions(ctx)` call:
  - Session A: `data.CreateSession` only (orphan, no lease) → after the
    sweep, `Activity == Inactive`.
  - Session B: `data.CreateSession` + `data.CreateTask` (in_progress) +
    `Claim` the task with a comfortably-long TTL (e.g. 30s) under some
    holder — simulating a live CLI `mads run` — → after the sweep,
    `Activity` is unchanged (still `Active`). This is the "never fight a
    live pipeline engine" proof the task explicitly asks for.
  - Session C: `sup.SpawnSession(ctx, sess.ID)` (a real tracked child, no
    lease at all) → after the sweep, still `Active` — proves the
    `tracked` check short-circuits before ever consulting leases,
    independent of whether this exact ordering is reachable at real Router
    boot (defensive correctness, not just today's call site).
- **`TestSessionResumeAfterOrphanReconciliation`** (or place in
  `services/router/internal/api/sessions_test.go` instead — it's really an
  API-layer proof, and that package already has the HTTP-level scaffolding
  for hitting `/sessions/{id}/terminate` and `/sessions/{id}/resume`
  directly; prefer that location). Build a server the way
  `newTestServer`-adjacent tests that need direct data access do (own
  `datatest.Start` + `dataclient.New` + `supervisor.New`, e.g. mirror
  the pattern already used around `services/router/internal/api/api_test.go:250`
  and `:305`): `data.CreateSession` directly (orphan again — active, no
  spawn), `POST /sessions/{id}/terminate` → 204, `GET /sessions/{id}` →
  `Activity == inactive`, `POST /sessions/{id}/resume` → 200 with a
  non-empty `addr`. This is the concrete end-to-end proof that Resume,
  unmodified, starts working again once Terminate stops being a no-op.

### SSE — no new test needed

The task calls for proving the event reaches the UI "via the existing SSE
path test." `session.activity_changed` rides the same generic Data
Service event log every other event type does, and Router's `GET /events`
is a byte-transparent proxy with **no server-side type allowlist**
(`services/router/internal/api/events.go` — `types` is passed straight
through to `dataclient.Stream`). `TestStreamEventsFanoutFilters` /
`TestStreamEventsReconnectWithCursorNoGaps`
(`services/router/internal/api/events_test.go`) already prove arbitrary
event types reach subscribers correctly; nothing new is needed there. Cite
this in the summary rather than writing a redundant SSE test.

## Docs

- **`services/router/README.md`**:
  - Update the `POST /sessions/{id}/terminate` bullet (around line 97) to
    mention the orphan-reconciliation behavior — it's no longer purely "the
    child exit hook flips activity"; Terminate itself now enforces it when
    nothing is tracked.
  - Update the "Session summaries are triggered by a child-exit hook, not
    an event subscription" paragraph (~line 409) — `Activity` now flips to
    `Inactive` in a **third** place (`reconcileInactiveSession`, called
    from `TerminateSession`'s no-child branch and the boot sweep), not just
    the two currently documented. Keep the "why not just subscribe to
    `session.activity_changed`" reasoning intact; add the orphan case as
    what it now also covers.
  - Add a short "Critical paths" bullet (near the lease/supervision bullets
    around line 320) documenting the boot sweep: what it does, why it
    checks leases before flipping, and point at
    `TestBootSweepReconcilesOrphanedActiveSession` as the proof.
- **`services/data/README.md`**: add `GET /sessions/{id}/lease-active` to
  the "Lease routes" section (~line 68), one line, matching the existing
  terse bullet style (`{"active": bool}`, no actor/attribution needed since
  it's read-only).

## Risks / things worth scrutinizing

- **Boot sweep runs on every Router start, including normal ones with zero
  orphans.** It does one `GET /sessions?activity=active` plus one
  lease-check round trip per active session — fine at expected session
  counts, but it's an O(active sessions) synchronous blocking call before
  the server starts serving. If that ever becomes a problem, the fix is
  making it non-blocking (fire-and-forget goroutine) rather than removing
  the bound — but that trades "orphans are fixed before anyone can hit
  Close/Resume against a stale record" for faster startup; don't make that
  tradeoff silently.
- **The lease check is a point-in-time read, not a lock.** A CLI `mads run`
  could theoretically start claiming a lease on an orphaned session in the
  small window between the boot sweep's list-active-sessions read and its
  per-session lease check/flip. Extremely unlikely in practice (the sweep
  runs once, synchronously, at the very start of Router startup, before
  anything is likely racing it) — worth a one-line note in the sweep's
  comment, not worth engineering around.
- **`TerminateSession`'s unconditional reconciliation on the no-child path
  means a user clicking "Close" always wins, even against a live external
  `mads run`.** This is deliberate per the task brief (explicit user action
  is truth-enforcement), but it's worth calling out explicitly in the PR/
  summary so it's a known, intentional asymmetry with the boot sweep, not
  something a reviewer flags as an inconsistency.
