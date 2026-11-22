# Git state served from the Router (Phase B)

Status: design for build, 2026-11-21. Follows Phase A (branch `git-routes-a`:
request hygiene, aborts, shared changeset store, spawn-free repo roots).
Measured problem and harness: `docs/PERF-HARNESS-PLAN.md` ("Git routes").

## Goal

No browser request waits on git. The Router keeps each session's git-derived
state (branch facts, the changeset, the worktree counts), recomputes it in the
background when something that affects it happens, and tells the window over
the bus when a fresh copy is ready. `GET` answers from that state in
milliseconds, and an "updating" indicator shows while a recompute runs.
Agreed with Drew: showing the last computed state, marked "updating", for a
few seconds is acceptable; the indicator is part of this change. Actions
(Commit, Land, Push) keep checking fresh state before they act.

## What the Router keeps (`gitstate` service, Router memory)

Per session:

| Part | Source (unchanged compute) | Kept warm |
|---|---|---|
| `repos` | `repostate` branch facts per repo (today's `GET /repos`) | yes |
| `changeset` | `changes.Build` for baseline `start` | yes |
| `changeset` for `base`/`default` | same, other baseline | on demand, cached 5 min |
| `counts` | `RefreshCounts` (child's coalesced recount when a child is live, Router otherwise; Phase A's routing) | yes; result lands on the session record as today |

Each part carries `generation` (monotonic per session), `computed_at`, and
`updating` (a recompute is queued or running). One single-flight recompute per
(session, part); a trigger during a run marks it dirty for exactly one rerun.
Recompute runs under the Router's own context with a timeout, never a
request's, so a window going away can't cancel it (writes are unaffected: all
git here is read-only).

**Which sessions are kept warm:** active sessions, plus any session whose state
was read in the last 15 minutes (a window has it open). Others are dropped from
memory after 15 minutes idle and recomputed on the next read. This bounds
background CPU to what windows look at and what is running.

**Boot:** start empty; warm active sessions in the background. No replay of
the event log (the Router subscribes from the log head, like the UI bus).

## Triggers (server side)

The service subscribes to the Data Service from the head (live only) and
watches for, per session:

- `file.changed`: changeset + counts, debounced 1.5 s, capped at 5 s;
- `session.agent_state` to idle (turn end): changeset + counts;
- `commit.created`, `commit.undone`, `merge.completed`: all parts;
- `repo.changed` (the `repostate` ref watcher, in-process): repos + counts for
  sessions using that git dir;
- `POST /worktree/refresh`: counts (and repos), answered at once;
- a read of a part that is cold or older than its trigger state.

`session.updated` is **not** a trigger: the counts write produces it, and
listening to it would loop.

This service replaces the Router's 30-second sweep that re-lists every active
session's repos (`activeCheckouts`): the checkout list comes from the service's
own per-session repo targets, refreshed by the triggers above.

## Telling the window

New event `session.git_state_updated`, payload
`{session_id, parts: ["repos"|"changeset"|"counts"…], generation, changed: [...]}`,
appended to the event log when a **triggered recompute finishes**, whether or
not its result changed. `parts` lists what was recomputed; `changed` (a subset
of `parts`) lists what actually differs. It is small (no data), and at most one
per part per debounce window. (Emitting only on change was rejected in review:
the window marks parts "updating" when it sees a trigger, so an unchanged
result must still tell it the recompute is done, or the indicator sticks.)

The UI's shared stores (`repoStateStore`, the Phase A changeset store)
subscribe to it and refetch the cached `GET` (instant) only for parts in `changed`. They no longer refetch
on `file.changed`, `commit.*` or `repo.changed` directly; those only mark the
part "updating" in the window (see below). Counts keep arriving on
`session.updated` as today.

## Response shapes (additive or header-only; mesh forwards these to peers)

- `GET /sessions/{id}/changeset`: adds `updating: boolean` (and keeps
  `computed_at`). Cold (never computed): `files`, `turns`, `commits` empty,
  `computed_at` empty, `updating: true`; the UI shows its existing
  "Loading changes…" for that state.
- `GET /sessions/{id}/repos`: still a bare array (unchanged body). Adds
  headers `X-Mads-Updating: 1|0` and `X-Mads-Computed-At: <RFC 3339>`. Cold:
  `[]` with `X-Mads-Updating: 1`; the UI treats that as "loading", not
  "no repos" (Push is not offered until a computed answer arrives).
- `POST /sessions/{id}/worktree/refresh`: `202 Accepted`, no body (was `204`
  after the recount). The new counts arrive on `session.updated`.

## The "updating" indicator

One element, `data-testid="git-updating"`, in the session workspace (beside
the Changes tab label / dock work chip), with an accessible label "Updating
git state". Shown for the open session while any part is updating:

- set when a `GET` says `updating` (body field or header), or when the window
  sees a trigger event for the session (`file.changed`, turn end, `commit.*`,
  `merge.completed`, `repo.changed` for its git dir), or after a refresh `POST`;
- cleared when `session.git_state_updated` arrives with a newer generation for
  every part that was marked (changed or not; counts are cleared only this way,
  since they have no `GET`), or a `GET` returns `updating: false` (fallback:
  clear after 30 s so it can never stick).

Contract with the perf harness check g: visible within 2 s of a change,
cleared within 15 s, with values then matching a fresh compute.

## Actions stay fresh

Commit plan (`GET /commit-plan`), Push preview (`GET /push`), Land preview
(`GET /land`) and the Commit/Land/Push actions keep computing on demand
(user-initiated, one at a time). They are not served from the cache.

## Out of scope

Cheaper git itself (Phase C: untracked cache, fewer diffs, bounded
cherry-picks); the rail's live state for remote sessions (mesh follow-up).

## Decisions to review with Drew

1. A new durable event type (`session.git_state_updated`) rather than an
   ephemeral channel; one per finished triggered recompute (with a `changed`
   list), capped by the debounce: a handful of rows per active turn.
2. Warm set = active sessions + read in the last 15 minutes.
3. Cold reads answer at once with an empty, `updating` state rather than
   waiting for the first compute.
4. `POST /worktree/refresh` becomes `202` (fire and forget).
