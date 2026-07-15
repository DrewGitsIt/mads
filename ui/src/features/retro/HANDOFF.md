# HANDOFF — W6.7 Retro surface

Architect stage output. Executor: read this fully before writing code. This
task has fewer backend gaps than W6.5 did — most of what's needed already
exists — but it has two real interpretive judgment calls (§6, §11) baked
into the acceptance criteria's own wording. Read those before assuming the
obvious reading is the intended one.

## 0. Coordination risk (read first)

W6.7 depends only on **W6.1 (App shell)** and **W5.5 (Retro engine)**, both
merged into `main` per `docs/MADS-WORK-ITEMS.md`. **W6.2 (Sessions), W6.3
(Conversation), W6.5 (Files)** are also already merged (see the git log this
branch was cut from). Concretely, that means:

- `App.tsx`'s `SURFACES` map exists and already has an unwired `retro` rail
  item (`RAIL_ITEMS` includes `{ id: "retro", label: "Retro", icon: "R" }`,
  but `SURFACES` only maps `sessions` today). Wiring this surface in for
  real is a genuine one-line diff — do it, don't build a standalone demo
  mount (see §8; this differs from W6.3/W6.5's demo-mount workaround, which
  existed only because the `SURFACES` map didn't exist yet when those were
  written).
- `ui/src/router/{client.ts,events.ts,testFakeEventSource.ts}` both Router
  client idioms already coexist. The task brief says "as W6.2" explicitly —
  use `routerGet`/`routerPost`/`subscribeToEvents`/`FakeEventSource`, **not**
  `RouterClient`/`FetchRouterClient`/`FakeRouterClient`. Retro is a top-level
  rail surface like Sessions, not a session-workspace tab like
  Conversation/Files, so this isn't just brief-literalism — it's the
  idiom that actually fits.
- W6.6 (Pipeline tab) and W6.4 (Tasks tab) are **not** dependencies and can
  be ignored regardless of their merge state.

Re-check `main` before writing code in case anything above has drifted, but
I checked all of it directly against the real tree for this design.

## 1. Backend reality check

Read the actual Go source, not just `docs/MADS-UX.md` (§3.4's route list at
line 125 is aspirational in the same way prior HANDOFFs have flagged — see
`[[mads-ux-spec-gaps]]`). Status as of this design:

**Router (`services/router/internal/api/retros.go`) — exists today:**
- `POST /retros` (`handleStartRetro`) — validates `session_ids` non-empty,
  mints `retroID` via `job.NewRetroID()`, spawns `job.GenerateRetro`, returns
  `202 {"job_id", "retro_id"}` immediately (both IDs, before generation
  starts — the client can correlate `retro.*` events right away, no race).
- `POST /jobs/{id}/cancel` (`handleCancelJob`) — generic job cancellation,
  `204` on success, `404 {"error": ...}` if the job is already finished. Not
  retro-specific; the retro job's `job_id` (not `retro_id`) goes here.
- `POST /retros/{id}/follow-up-session` (`handleRetroFollowUpSession`) —
  builds one `Task` per **non-rejected** DPA (`Status == DPARejected` is the
  only exclusion — accepted *and* edited both count), creates a
  `Session{Tags: ["retro"], RetroID: retroID}`, spawns it, imports the
  tasks, links `FollowUpSessionID` back onto the retro (log-and-continue on
  link failure), returns `201 {"session", "addr", "tasks"}`. `400` if zero
  eligible DPAs.

**Does NOT exist on the Router yet — only on the Data Service
(`services/data/internal/api/retros.go`), unproxied:**
- `GET /retros`, `GET /retros/{id}` — needed for the history list and for
  loading a completed retro's DPAs (see below, `retro.completed`'s payload
  doesn't carry them).
- `POST /retros/{id}/dpas/{dpaId}/accept`, `.../reject` — needed for the
  status-chip toggle.
- `PATCH /retros/{id}/dpas/{dpaId}` (edit) — **deliberately not proxying
  this one**, see §10 (Edit is out of scope for this pass).

`pkg/dataclient/dataclient.go` already fully wraps all of these
(`Retros`, `Retro`, `AcceptDPA`, `RejectDPA`, `EditDPA`,
`SetRetroFollowUpSession`) — this is a thin-proxy job, same judgment call
W6.4 made for its Router gaps: cheap to close, in scope. Add 4 new handlers
(§2), not 5 — Edit is excluded on purpose.

**Existing test file gotcha:** `services/router/internal/api/retros_test.go`
currently has this comment on `TestRetroDPAAcceptRejectEdit`:

> Accept/reject/edit transitions are exercised against datatest directly
> (the Data Service, not the Router — the Router has no routes of its own
> for these; only the Data Service's HTTP API and its store are involved).

Once accept/reject are proxied, this comment is **half stale** — true for
Edit, false for Accept/Reject. Update it, and add real Router-level HTTP
tests for the two new routes (mirroring `TestRetroFollowUpSession`'s
`postJSON(t, srv.URL+...)` pattern) rather than only testing them via
`dataclient` directly.

**Event payload shapes — read carefully, this drives the whole generation
UI (`pkg/contracts/event.go` lines 94–114, `services/router/internal/job/
retro.go`, `services/data/internal/store/retros.go` `Create`):**

| Event | Emitted by | Payload |
|---|---|---|
| `retro.started` | Router job, immediately | `{retro_id, session_ids}` |
| `retro.progress` | Router job, twice | `{retro_id, step}`, `step` is literally `"gathering"` then `"generating"` (`pkg/retro/retro.go` lines 92–109) — **not** a 3-value enum |
| `retro.completed` | **Data Service**, inside `Retros.Create`'s transaction (not the job body) | `{retro_id, dpa_count}` — **no `dpas` array**, the UI must `GET /retros/{id}` after this fires |
| `retro.failed` | Router job | `{retro_id, error}` |
| `retro.cancelled` | Router job, on a fresh 10s-timeout context (ctx is already dead) | `{retro_id}` — always fires on cancel, even though nothing was persisted (generation makes no writes until the very end) |
| `retro.dpa_accepted` / `retro.dpa_rejected` | Data Service | `{retro_id, dpa_id}` |
| `retro.dpa_edited` | Data Service | `{retro_id, dpa_id}` — no diff, re-fetch |
| `retro.follow_up_created` | Data Service | `{retro_id, session_id}` |

Two consequences for the UI design (§4):
1. **The "3-step generation UI" in the task brief maps to `retro.started` +
   the two `retro.progress` steps** (Starting → Gathering → Generating), not
   three distinct `retro.progress` values. There's no backend gap here, just
   a naming translation the UI owns.
2. **A `GET /retros/{id}` round-trip is required after `retro.completed`**
   to get the DPA list at all — this is why the "does not exist yet" Router
   gap above is not optional, it's load-bearing for the acceptance criteria.

**`subscribeToEvents` wildcard gotcha** (`ui/src/router/events.ts`): passing
`types: ["retro.*"]` filters correctly server-side but silently receives
nothing client-side — `addEventListener` is called with the literal strings
in `params.types`, and the SSE frame's `event:` name is always the concrete
type. Pass an explicit enumerated array, mirroring `SESSION_EVENT_TYPES` in
`ui/src/features/sessions/state/useSessionsList.ts`.

**Retro events are not session-scoped.** `Envelope.session_id` is empty for
every `retro.*` event; correlation is via `payload.retro_id`. Subscribe
globally (no `sessionId` filter param) and filter client-side by comparing
`(envelope.payload as {retro_id?: string})?.retro_id` against the tracked
retro ID.

## 2. Go changes (`services/router/internal/api/retros.go` + `api.go`)

Add to `retros.go`, matching the existing file's style
(`writeJSON`/`writeError` from `api.go`, same as `handleRetroFollowUpSession`
already does):

```go
func (s *server) handleListRetros(w http.ResponseWriter, r *http.Request) {
	retros, err := s.data.Retros(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, retros)
}

func (s *server) handleGetRetro(w http.ResponseWriter, r *http.Request) {
	retro, err := s.data.Retro(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, retro)
}

func (s *server) handleAcceptDPA(w http.ResponseWriter, r *http.Request) {
	if err := s.data.AcceptDPA(r.Context(), r.PathValue("id"), r.PathValue("dpaId")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleRejectDPA(w http.ResponseWriter, r *http.Request) {
	if err := s.data.RejectDPA(r.Context(), r.PathValue("id"), r.PathValue("dpaId")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Register in `api.go` next to the existing retro routes:

```go
mux.HandleFunc("GET /retros", s.handleListRetros)
mux.HandleFunc("GET /retros/{id}", s.handleGetRetro)
mux.HandleFunc("POST /retros/{id}/dpas/{dpaId}/accept", s.handleAcceptDPA)
mux.HandleFunc("POST /retros/{id}/dpas/{dpaId}/reject", s.handleRejectDPA)
```

204/404/500 follow `writeError`'s existing mapping
(`dataclient.ErrNotFound` → 404, etc.) — no new error-handling code needed.
Add Go tests in `retros_test.go`: list/get happy path, get-404 for an
unknown ID, and HTTP-level accept/reject (see §1's stale-comment note).

## 3. Module layout

New feature module, sibling to `features/sessions/` (README: "Every future
surface (Retro, Library, etc.) follows the same pattern... See
`src/features/sessions/`"):

```
ui/src/features/retro/
  RetroSurface.tsx / .module.css / .test.tsx
                                    top-level surface (registered in
                                    App.tsx SURFACES.retro); owns which of
                                    picking/generating/reviewing phase is
                                    showing (§5)
  HANDOFF.md                        (this file)
  events.ts                         RETRO_GENERATION_EVENT_TYPES,
                                    RETRO_LIST_EVENT_TYPES consts — single
                                    source of truth for the enumerated
                                    event-type arrays (§1 wildcard gotcha)

  picker/
    SessionPicker.tsx / .module.css / .test.tsx
                                    checkboxes + kind-filter tabs
                                    (All/Work/Retro-generated) +
                                    clear-selection
    ChipStrip.tsx / .module.css / .test.tsx
                                    read-only recap chips for the sessions
                                    a run/review covers (§6)
    sessionKind.ts / .test.ts       pure: classifySession, filterByKind

  ObservationsInput.tsx / .module.css / .test.tsx
                                    plain textarea, controlled by
                                    RetroSurface

  generation/
    GenerationProgress.tsx / .module.css / .test.tsx
                                    3-step indicator (Starting/Gathering/
                                    Generating) + Cancel button
    stepOrder.ts / .test.ts         pure: stepStatus(stepIndex, phase)
                                    -> pending|active|done

  dpa/
    DPACard.tsx / .module.css / .test.tsx
                                    problem/evidence/root_cause/
                                    remediation_layer/suggested_action +
                                    status Chip toggle, wrapped in <Draft>
    dpaEligibility.ts / .test.ts    pure: acceptedDpaCount(dpas)

  FollowUpCta.tsx / .module.css / .test.tsx
                                    "Create follow-up session (N accepted)"
                                    button, or a "Follow-up session created"
                                    indicator if retro.follow_up_session_id
                                    is already set

  history/
    RetroHistoryList.tsx / .module.css / .test.tsx
                                    GET /retros results, click -> review

  state/
    useRetroGeneration.ts / .test.ts   job lifecycle state machine (§4)
    useRetroDetail.ts / .test.ts       fetch one retro + optimistic
                                        accept/reject toggle (§4); shared by
                                        the post-generation review and
                                        history click-through
    useRetroHistory.ts / .test.ts      GET /retros + refetch on
                                        RETRO_LIST_EVENT_TYPES
    useSessionsForPicker.ts / .test.ts GET /sessions + refetch on
                                        session.* (§4, duplication note)
```

No `retro.html` / demo mount / `npm run retro` script — unlike
Files/Conversation, the `SURFACES` map already exists and already has the
rail slot (§0), so this wires in for real from day one (§8).

## 3a. New TS types (`ui/src/router/types.ts`)

No `Retro`/`DPA` types exist yet. Mirror `pkg/contracts/retro.go` exactly
(field names/optionality in lockstep, same rule the Conversation/Files
types followed):

```ts
// mirrors pkg/contracts/retro.go DPAStatus
export type DPAStatus = "accepted" | "rejected" | "edited";

// mirrors pkg/contracts/retro.go RemediationLayer
export type RemediationLayer =
  | "agent_config"
  | "tool_config"
  | "prompt_engineering"
  | "task_decomposition"
  | "dependency_structure"
  | "pipeline_template"
  | "human_process";

// mirrors pkg/contracts/retro.go DPA. ID is unique within its owning Retro
// only (minted "dpa_1", "dpa_2", ... server-side).
export interface DPA {
  id: string;
  problem: string;
  evidence: string;
  root_cause: string;
  remediation_layer: RemediationLayer;
  suggested_action: string;
  status: DPAStatus;
}

// mirrors pkg/contracts/retro.go Retro. `dpas` is present but can be an
// empty array (never null — Data Service always writes []DPA, never nil)
// before generation finishes; the review UI only ever renders this after
// GET /retros/{id} following retro.completed, so treat it as always
// populated at the point this UI reads it.
export interface Retro {
  id: string;
  session_ids: string[];
  user_observations?: string;
  dpas: DPA[];
  follow_up_session_id?: string;
  created_at: string;
  updated_at: string;
}
```

## 3b. New fixtures (`ui/src/router/fixtures.ts`)

Add to the existing `fixtureSessions` map (needed for kind-filter testing —
`tags: ["retro"]` marks a session as retro-generated, matching
`Session.tags` already on the type):

```ts
"session-retro-generated": {
  id: "session-retro-generated",
  created_at: "2026-07-14T00:00:00Z",
  updated_at: "2026-07-14T00:00:00Z",
  name: "Follow-up: retry backoff DPAs",
  activity: "inactive",
  workspace: "mads",
  tags: ["retro"],
  retro_id: "retro-1",
},
```

New exports, alongside the existing fixture exports:

```ts
export const fixtureRetro: Retro = {
  id: "retro-1",
  session_ids: ["session-active", "session-inactive"],
  user_observations: "Retries kept looping without backoff; flaky test masked a real race.",
  dpas: [
    {
      id: "dpa_1",
      problem: "Ingest worker retried without backoff, hammering the queue.",
      evidence: "session-active transcript, tool_use Edit at ingest.go:42",
      root_cause: "No backoff policy configured on the retry loop.",
      remediation_layer: "tool_config",
      suggested_action: "Default all retry loops to exponential backoff with jitter.",
      status: "accepted",
    },
    {
      id: "dpa_2",
      problem: "Flaky test masked a real refresh/writer race.",
      evidence: "session-inactive transcript, pinned-clock repro",
      root_cause: "No FOR UPDATE fence around the metrics view refresh.",
      remediation_layer: "prompt_engineering",
      suggested_action: "Steer investigator agents to add a regression test under -race before closing.",
      status: "rejected",
    },
    {
      id: "dpa_3",
      problem: "No workspace convention for retry policies.",
      evidence: "Both DPAs above stem from ad hoc retry code.",
      root_cause: "Missing workspace-level steering file for retry/backoff conventions.",
      remediation_layer: "human_process",
      suggested_action: "Add a retry-policy steering file and reference it from agent prompts.",
      status: "accepted",
    },
  ],
  created_at: "2026-07-14T12:00:00Z",
  updated_at: "2026-07-14T12:05:00Z",
};

export const fixtureRetroList: Retro[] = [fixtureRetro];
```

`fixtureRetro`'s three DPAs deliberately span 3 different
`remediation_layer` values and both `accepted`/`rejected` statuses, so a
single fixture exercises the kind-agnostic rendering in `DPACard` and gives
scenario 2 in §7 a pre-rejected DPA to flip back to accepted, not just an
accepted-to-rejected path.

## 4. State design

**`useRetroGeneration`** — owns the job lifecycle only, not the DPA data:

```ts
export type GenerationPhase =
  | "idle" | "starting" | "gathering" | "generating"
  | "completed" | "failed" | "cancelled";

export interface UseRetroGeneration {
  phase: GenerationPhase;
  error: string | null;
  retroId: string | null; // set as soon as POST /retros resolves
  start: (sessionIds: string[], userObservations: string) => Promise<void>;
  cancel: () => void;
  reset: () => void;
}
```

`start()`: `routerPost<{job_id, retro_id}>("/retros", {session_ids,
user_observations})`, store both IDs, set `phase = "starting"`, subscribe
via `subscribeToEvents({types: RETRO_GENERATION_EVENT_TYPES}, onEvent)`
(global, no `sessionId`), filter every event by `payload.retro_id ===
trackedRetroId` before acting. `retro.started` → `"starting"`;
`retro.progress {step:"gathering"}` → `"gathering"`; `{step:"generating"}` →
`"generating"`; `retro.completed` → `"completed"` (no fetch here — see
`useRetroDetail`); `retro.failed` → `"failed"` + `error`; `retro.cancelled`
→ `"cancelled"`.

`cancel()`: `routerPost(\`/jobs/${jobId}/cancel\`)`, fire-and-forget (catch
and surface errors, but don't optimistically flip phase) — wait for the real
`retro.cancelled` event, which the backend always emits on a cancelled run
(§1). This keeps the UI honest about backend state and makes the
FakeEventSource-driven test in §7 deterministic.

`reset()`: back to `idle`, clears `retroId`/`jobId`/`error`, unsubscribes.

**`useRetroDetail(retroId: string | null)`** — fetch-once + optimistic
toggle, reused by both "just generated" and "picked from history":

```ts
export interface UseRetroDetail {
  retro: Retro | null;
  loading: boolean;
  toggleDpa: (dpaId: string) => void; // fire-and-forget, optimistic
}
```

`toggleDpa` flips a DPA between `accepted`⇄`rejected` (never touches
`edited` — see §10): if current status is `"rejected"`, POST `.../accept`;
otherwise POST `.../reject`. Update `retro.dpas` locally before the request
resolves; revert that one DPA's status on failure. No live event
subscription in this hook — single-user local desktop app, and the calling
surface already owns the optimistic state; keep it simple.

**`useRetroHistory`** and **`useSessionsForPicker`** both mirror
`ui/src/features/sessions/state/useSessionsList.ts`'s
fetch-once-then-refetch-on-event shape exactly (`routerGet` + `useEffect` +
`subscribeToEvents` + refetch-on-any-relevant-event, not per-event payload
parsing). `useSessionsForPicker` duplicates ~30 lines of `useSessionsList`
rather than importing across feature-module boundaries — no sibling feature
module (`files/`, `conversation/`) imports another's state today, so this
keeps the same isolation. Flagged as minor, deliberate duplication in §11,
not an oversight.

`events.ts`:

```ts
// Job-lifecycle events for one in-flight generation run. Concrete literal
// strings only — see ui/src/router/events.ts: subscribeToEvents attaches
// addEventListener per literal in this list; "retro.*" would filter
// correctly server-side and receive nothing client-side.
export const RETRO_GENERATION_EVENT_TYPES = [
  "retro.started",
  "retro.progress",
  "retro.completed",
  "retro.failed",
  "retro.cancelled",
];

// Refetch triggers for the history list and any open detail view.
export const RETRO_LIST_EVENT_TYPES = [
  "retro.completed",
  "retro.dpa_accepted",
  "retro.dpa_rejected",
  "retro.dpa_edited",
  "retro.follow_up_created",
];
```

## 5. `RetroSurface` composition

`RetroSurface` owns three pieces of local state that outlive any single
`useRetroGeneration` run: `selectedSessionIds: string[]`, `observations:
string`, and `viewedRetroId: string | null` (set either by a completed
generation or by clicking a history row). Derive a `surfacePhase` from
`useRetroGeneration().phase`:

- `idle` / `cancelled` / `failed` → **picking**: `SessionPicker` +
  `ObservationsInput` + "Generate retro" button (disabled until
  `selectedSessionIds.length > 0`) + `RetroHistoryList` below. On
  `cancelled`/`failed`, call `generation.reset()` once, but **do not**
  clear `selectedSessionIds`/`observations` — this is exactly what makes
  "cancel mid-generation leaves the picker restored" a real, checkable
  transition (§6) rather than a no-op.
- `starting` / `gathering` / `generating` → **generating**: `ChipStrip`
  (read-only, `selectedSessionIds` resolved against the fetched session
  list) + `GenerationProgress` (`onCancel={generation.cancel}`).
- `completed` → set `viewedRetroId = generation.retroId` once (on the
  transition, not every render) → **reviewing**.
- Clicking a `RetroHistoryList` row also sets `viewedRetroId` and forces
  **reviewing**, independent of `generation.phase`.
- **reviewing**: `ChipStrip` (from `retro.session_ids`, not
  `selectedSessionIds` — important once reviewing a history item that isn't
  the just-generated one) + `retro.dpas.map(dpa => <DPACard dpa=...
  onToggle={toggleDpa} />)` + `FollowUpCta` + a "Start new retro" button
  that clears `viewedRetroId`, calls `generation.reset()`, and clears
  `selectedSessionIds`/`observations` (this is the one place selection
  should actually clear — starting fresh, not cancelling).

## 6. The chip-strip timing decision — resolves a real spec ambiguity

Both `docs/MADS-WORK-ITEMS.md` ("collapses to chip strip **post-**
generation") and `docs/MADS-UX.md` §3.4 ("collapses to a summary chip strip
**once a retro is generated**") read literally as: the full picker stays
visible throughout the 3-step progress, and only collapses after
completion. Taken literally, that makes the acceptance criterion "cancel
mid-generation leaves the picker restored" a no-op — nothing collapsed, so
there's nothing to restore.

**Resolving this in favor of collapsing on Generate-click** (§5): the chip
strip appears the moment generation starts and persists through completion
and into the review phase. This is still consistent with the spec prose
under a natural reading — "once a retro is generated[, and from then on],
[the picker shows as] a chip strip" — it's just also true slightly earlier.
It's the only reading under which the cancel-restores-the-picker acceptance
criterion is a meaningful, testable UI transition rather than always
trivially true. Flagged for human confirmation in §11 — this is the biggest
interpretive call in this design.

## 7. Testing generation against `FakeEventSource`

Directly mirrors `ProvisioningStrip.test.tsx`'s pattern
(`ui/src/features/sessions/launchpad/ProvisioningStrip.test.tsx`):
`vi.stubGlobal("EventSource", FakeEventSource)`, stub `fetch` for the
`POST /retros` response, render, grab `FakeEventSource.instances[0]`, and
drive it with `.emit(type, JSON.stringify(envelope))` calls carrying the
right `payload.retro_id`.

`RetroSurface.test.tsx` (component-level, providers/fake-shaped fixtures —
see §11 for what "against the real retro engine on providers/fake" means
here) should cover, in one flow per the acceptance criteria:

1. **Full round-trip**: stub `fetch` — `GET /sessions` returns fixture
   sessions, `POST /retros` returns `{job_id, retro_id}`, `GET
   /retros/:id` (fired after `retro.completed`) returns a fixture `Retro`
   with 2+ DPAs. Check the boxes for two sessions, click Generate, assert
   the picker is gone and `ChipStrip` + `GenerationProgress` show. Emit
   `retro.started` → `retro.progress{gathering}` → `retro.progress
   {generating}` → `retro.completed` (matching `retro_id`), assert each
   step lights up in order, then assert DPA cards render with the fixture
   data.
2. **Chip-toggle accept/reject**: from the state after (1), click a DPA's
   status chip, assert `POST /retros/:id/dpas/:dpaId/reject` fires and the
   chip flips tone/label; assert `FollowUpCta`'s "(N accepted)" count
   decrements immediately (optimistic).
3. **Follow-up CTA**: click it, stub `POST .../follow-up-session` to
   `201`, assert the response's `session`/`tasks` are surfaced (a callback
   prop is enough — actually navigating to the new session is out of scope,
   §10).
4. **Cancel mid-generation**: start generation, emit `retro.started` only
   (stop before completion), click Cancel, assert `POST /jobs/:id/cancel`
   fires; emit `retro.cancelled`; assert the full `SessionPicker` is back
   with the **same two checkboxes still checked** and `ObservationsInput`'s
   text intact — this is the literal check for "leaves the picker
   restored."

Also unit-test `sessionKind.ts`, `dpaEligibility.ts`, and `stepOrder.ts` as
pure functions, same rigor as `attribution.test.ts` in W6.5.

No Playwright — see §11's note on the `docs/MADS-WORK-ITEMS.md` wording
mismatch.

## 8. `App.tsx` wiring

```ts
import { RetroSurface } from "./features/retro/RetroSurface";
// ...
const SURFACES: Partial<Record<string, ComponentType>> = {
  sessions: SessionsSurface,
  retro: RetroSurface,
};
```

One-line addition plus the import, exactly the "one-line shell
registration" the task brief calls for. No changes to `RAIL_ITEMS` — the
`retro` item already exists.

## 9. Test-to-acceptance-criteria map

- "Retro round-trip at component level against the real retro engine on
  providers/fake: generate → DPA cards" → `RetroSurface.test.tsx` scenario
  1 (§7).
- "chip-toggle accept/reject" → scenario 2.
- "follow-up CTA count updates" → scenario 2 (count decrement) + scenario 3
  (creation call).
- "cancel mid-generation leaves the picker restored" → scenario 4.
- "make check passes" → holds by construction: nothing here needs a real
  browser/display/Playwright, same reasoning as every prior landed HANDOFF
  in this package.

## 10. Non-goals (explicitly out of scope, matches the task brief's Scope
list, not `docs/MADS-UX.md` §3.4's fuller prose)

- **DPA Edit.** MADS-UX §3.4 says "the status chip is the toggle... plus
  Edit"; the task brief's Scope list only lists the accept/reject toggle.
  Building Edit means a form for 5 DPA fields plus wiring `PATCH
  .../dpas/{dpaId}` — real scope, not a one-liner. Not proxying that route
  either (§1/§2). If Edit lands later, `contracts.DPAEdit`'s shape is
  already known (`pkg/contracts/retro.go`) and the Router proxy is a
  20-minute add.
- **Picker "stats"** (`docs/MADS-UX.md` line 125's "`GET /sessions` (picker
  w/ stats)", i.e. `open_task_count` per row, à la
  `useOpenTaskCounts.ts`). Not in the task brief's Scope list; the picker
  needs `id`/`name`/`tags` only.
- **Retro-of-retros as a distinct UI mode.** Already correctly not a
  separate mode per spec — selecting `retro`-tagged sessions in the picker
  is sufficient; `pkg/retro/retro.go`'s `renderSession` handles the
  prompt-shaping server-side. Nothing to build beyond the kind filter
  itself.
- **Navigating to the newly created follow-up session** after the CTA
  succeeds — no session-workspace shell exists to navigate into yet (same
  gap W6.3/W6.5 hit). Surface the created session/tasks via a callback prop
  and a success `Toast`; actual navigation is follow-up work once a
  session-workspace route exists.
- Any Go changes beyond the 4 handlers in §2 — no Edit proxy, no changes to
  `pkg/retro`, `job/retro.go`, or the Data Service store.

## 11. Risks / judgment calls for human review

1. **The chip-strip timing decision (§6)** is the single biggest
   interpretation call in this doc — both spec documents' literal wording
   supports "collapse only after completion," and I resolved it the other
   way specifically to make an acceptance criterion meaningful. Confirm
   this is the intended v1 behavior, not a misreading.
2. **"Against the real retro engine on providers/fake"** — read as
   "fixtures shaped like `pkg/retro`'s actual golden output," not a literal
   live Go-server hit from vitest. There's no Playwright anywhere in this
   repo (`ui/README.md` line 148, confirmed via full-repo grep) and every
   prior landed HANDOFF in this package tests at the component level
   against stubbed `fetch`/`FakeEventSource` — this directly contradicts
   `docs/MADS-WORK-ITEMS.md`'s W6.7 "Done when" line ("Playwright retro
   round-trip"), which I'm treating as stale against the task brief's
   explicit "vitest component tests, NO Playwright" instruction. Confirm
   that's the right call, not something to escalate before building.
3. **Edit is dropped from scope (§10)** — MADS-UX explicitly calls it out
   ("plus Edit"), the task brief's Scope list doesn't. If a human reviewing
   this expected Edit to ship in this pass, flag it before the executor
   starts rather than after.
4. **4 of 4 new Router routes are thin proxies with no new logic** — low
   risk, but note the stale test comment in §1 needs a real edit, not just
   new tests appended.

## 12. README

Per the task brief's UI conventions (as W6.2/W6.3): add a "Retro surface"
section to `ui/README.md` mirroring the "Sessions" section's shape (owns
`src/features/retro/`, registered in `SURFACES.retro`, no demo mount
needed). Update the opening paragraph's surface-ownership list (currently
calls out Sessions, Files, Conversation) to include Retro, and update the
"Run it alone" section's "Sessions today, a teaching empty state for any
rail item without a registered surface yet (Retro, Library)" sentence — it
needs to say "Sessions and Retro today... (Library)" once this lands.

## 13. Executor notes (implementation pass)

Built exactly per §§1–12 above; no drive-by refactors. Summary of what
landed and what a reviewer should scrutinize:

- **Full module built as specified**: `events.ts`, `picker/` (SessionPicker,
  ChipStrip, sessionKind), `ObservationsInput`, `generation/`
  (GenerationProgress, stepOrder), `dpa/` (DPACard, dpaEligibility),
  `history/RetroHistoryList`, `state/` (useRetroGeneration,
  useSessionsForPicker, useRetroHistory, useRetroDetail), `FollowUpCta`,
  `RetroSurface`. Every file has a sibling `.test.tsx`/`.test.ts`. Router
  Go proxies + TS types/fixtures from earlier in this pass were unchanged
  in this stage.
- **`App.tsx` wiring**: one-line import + `retro: RetroSurface` in
  `SURFACES`, as specified in §8.
- **README**: added a "Retro surface" section, updated the opening
  ownership paragraph and the "Run it alone" sentence per §12.
- **Deviations from the literal doc, both minor**:
  - `computeSurfacePhase` (in `RetroSurface.tsx`) treats generation phase
    `"completed"` as still "generating" until `viewedRetroId` is actually
    set, to avoid a one-frame flash back to the picker between the
    `retro.completed` event and the effect that commits `viewedRetroId`.
    Not spelled out verbatim in §5's phase table but consistent with its
    intent.
  - The Toast shown after follow-up-session creation was retitled to
    "Tasks imported" (description: "N tasks imported") instead of
    duplicating `FollowUpCta`'s own "Follow-up session created" status
    div — the original draft rendered that exact phrase twice
    (once from `FollowUpCta`'s internal created-state, once from the
    Toast), which is both a real UI redundancy and made the phrase
    ambiguous to query in tests.
- **Test-source gotcha found and fixed**: `useSessionsForPicker` and
  `useRetroHistory` both subscribe to SSE on mount (per §4), so by the
  time `RetroSurface.test.tsx` clicks "Generate", `FakeEventSource
  .instances` already has 2+ entries ahead of the generation hook's own
  subscription. Fixed by selecting the instance via
  `.find(i => i.url.includes("retro.started"))` rather than
  `instances[0]` — a test-file-only fix, no hook/component changes.
- **§11 risk items 1–4 stand as open questions for review** — none were
  resolved unilaterally beyond what's already written there.
- **Verification performed**: `tsc --noEmit`, `npm run lint` (0
  warnings after fixing one `react-hooks/exhaustive-deps` warning in
  `RetroSurface.tsx` with a justified inline disable, precedent at
  `TerminalView.tsx:76`), `npm run test` (341/341 passing, 48 in
  `features/retro`), and `make check` from repo root (golangci-lint 0
  issues, `go test -race ./...` green, UI lint/test green).
- **Not done, out of scope**: everything in §10 (DPA Edit, picker stats,
  follow-up-session navigation), as specified.
