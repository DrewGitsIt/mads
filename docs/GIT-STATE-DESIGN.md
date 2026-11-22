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
git here is read-only). At most 4 computes run at once across all sessions
(the boot warm-up included), and at most 2 for one session; the rest wait
their turn, still `updating`. Two lanes, decided when a slot is granted
(a read of a session promotes its computes already waiting): computes for a
session read in the last 60 s (a window shows it), whatever started them, go
ahead of every other waiting compute, and the other lane (warm-up, repo-wide
sweeps over sessions nobody shows) never holds more than 3 slots, so a shown
session's compute never waits behind a slow repo. A background compute
waiting more than 30 s is served like a shown one, so steady shown load
can't starve it. A
part whose compute failed is retried by a read at most once a minute while it
keeps failing (a trigger or an explicit refresh retries regardless).

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
- `repo.changed` (the `repostate` ref watcher, in-process): repos and counts
  for every kept session using that git dir; the `start` change set of a
  session read in the last 60 s always (a window shows it: a stash or a reset
  moves it without moving HEAD); for the others, change sets only where their
  refs moved. All mads worktrees of a repo share one git dir, so one session's
  commit reaches every session in the repo, and most change sets don't move
  with it. Each change set records the refs it was computed against, from the
  repos part: for `start`, each checkout's HEAD and branch (in-place sessions
  share a checkout, so another session's commit there moves theirs) and the
  repo's `refs/stash` (read from its loose ref file, no git spawned); for
  `base`, the session's own branch and HEAD and the base branch's head. The repos recompute this queues reads them
  again (repostate's branch facts), and its end recomputes `start` if its
  refs moved, and marks `base` stale if its did (`main`, whose ref repos
  doesn't read, is always marked stale; stale costs nothing until read).
  Unknown refs (a change set computed before its repos) count as moved. An
  unmoved `start` change set rides in the repos announcement as done
  (unchanged), so a window that marked it clears. An outside edit that moves
  no ref shows on the next open, focus, Retry or trigger. The UI marks all
  three parts on `repo.changed`, only for sessions the window shows;
- `POST /worktree/refresh` (see Response shapes): without a body (a window
  opening the session, or regaining focus, after Phase A's 30 s focus skip),
  counts, and repos and the changeset against `start` when the kept one is
  older than 30 s; with a body, the parts it names. Answered at once. Any
  caller (the route is View-class over mesh): a part is never recomputed more
  than once per 5 s by these asks, `max_age_ms: 0` included; a faster ask
  waits for the 5 s and every ask in between is that one compute (shown as
  updating meanwhile). Triggers keep their own debounce;
- a window opening a session's change set: the change-set store's first read
  after it gains a reader asks for a revalidate with `max_age_ms: 30000`
  (outside-the-agent edits send no `file.changed`);
- Retry in the Changes view: a revalidate of that baseline's change set, no
  max age;
- a recount whose counted repos (the session record's `repos` roots) include
  one the repos part doesn't hold: repos (the session works in a new repo; a
  live child's own turn-end recount usually wrote it first, so comparing the
  record before and after the Router's recount would miss it);
- a read of a part that is cold or stale.

`session.updated` is **not** a trigger: the counts write produces it, and
listening to it would loop.

This service replaces the Router's 30-second sweep that re-lists every active
session's repos (`activeCheckouts`): the checkout list comes from the service's
own per-session repo targets, refreshed by the triggers above.

## Telling the window

New event `session.git_state_updated`, payload
`{session_id, parts: ["repos"|"changeset"|"counts"…], generation, changed: [...], baseline?}`,
appended to the event log when a **triggered recompute finishes**, whether or
not its result changed. `parts` lists what was recomputed; `changed` (a subset
of `parts`) lists what actually differs. It is small (no data). (Emitting only
on change was rejected in review: the window marks parts "updating" when it
sees a trigger, so an unchanged result must still tell it the recompute is
done, or the indicator sticks.)

**One event per batch.** A session's finished parts are collected and
announced together once none of its kept parts (repos, the `start` change
set, counts) is still computing, or 5 s (the debounce cap) after the first
was collected, whichever is first (a timer, so it holds with no other end): a `file.changed` burst is one row listing
`["changeset","counts"]`, not one per part. A part that finishes while it is
queued again (a trigger in the debounce) is not announced; its queued run is
(for at most 5 s, so a steady stream still delivers results). A recompute
that only a read asked for and that failed with the same error as before is
not announced.

**`baseline`** (optional, added after review): set only for a change set
against `base` or `main`, which is computed on demand. Such an event covers
that baseline's change set alone (`parts: ["changeset"]`) and is never
batched with the others; without `baseline`, `changeset` means the `start`
change set. Readers that ignore the field treat it as `start` (an extra
cached read; their mark may clear early).

**Row estimate.** At most one row per session per fire: fires are at least
1.5 s apart (debounce) after a quiet spell and every 5 s (cap) under a steady
stream, so a session writing continuously adds ≤ 12 rows a minute, and a turn
with one burst of writes adds about 2 (the burst, the turn end; often one when
they fall in the same window), plus one per open, focus or Retry that
recomputes. Measured with the gitstate storm test (writes every 5 ms for 1 s,
debounce 20 ms, cap 100 ms): 9–10 rows, i.e. one per cap window, against 20
with one row per part.

A failed append is retried twice (250 ms, then 1 s); if it still fails, its
parts are owed again, so their next compute announces them (with "changed"
carried over).

The UI's shared stores (`repoStateStore`, the Phase A changeset store)
subscribe to it and refetch the cached `GET` (instant) only for parts in `changed`. They no longer refetch
on `file.changed`, `commit.*` or `repo.changed` directly; those only mark the
part "updating" in the window (see below). `repo.changed` marks repos,
changeset and counts, as the Router recomputes all three on it. Counts keep arriving on
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
  Mesh relays both headers for remote sessions (they are on the peer
  relay's allow-list, `relayedHeaders` in `services/router/internal/mesh`);
  the UI reads a missing `X-Mads-Updating` as "not updating" and a missing
  `X-Mads-Computed-At` as unknown.
- `POST /sessions/{id}/worktree/refresh`: `202 Accepted`, no body (was `204`
  after the recount). The new counts arrive on `session.updated`. Optional
  request body (added after review), `{"parts": ["repos"|"changeset"|"counts"…],
  "vs": "start"|"base"|"main", "max_age_ms": n}`: recompute the named parts
  (`vs` picks the change set's baseline, default `start`), leaving any whose
  kept answer is good and younger than `max_age_ms` (0 or absent: recompute
  regardless). An unknown part or baseline, or a negative age, is `400`. No
  body, or no `parts`: the bodiless refresh above. An older Router ignores the
  body and recounts. GET shapes are unchanged.

## The "updating" indicator

One element, `data-testid="git-updating"`, in the session workspace (beside
the Changes tab label / dock work chip), with an accessible label "Updating
git state". Shown for the open session while any part is updating:

- set when a `GET` says `updating` (body field or header), or when the window
  sees a trigger event for the session (`file.changed`, turn end, `commit.*`,
  `merge.completed`, `repo.changed` for its git dir), or after a refresh `POST`;
- cleared when `session.git_state_updated` arrives with a newer generation for
  every part that was marked (changed or not; counts are cleared only this way,
  since they have no `GET`), or a `GET` returns `updating: false`. Fail-safe:
  30 s after a mark it is cleared **and the part is re-read** (a `GET` of
  repos, or of each shown change set; cached and cheap, and a cold or stale
  part revalidates on the Router), so a lost announcement or an evicted
  session can't leave old data on screen looking fresh; the re-read marks it
  again if the Router still says updating.

The change set is marked per baseline: an event with `baseline` clears only
that baseline's mark, never `start`'s. When `start`'s recompute is announced
(after a trigger, which leaves the other baselines stale on the Router), a
shown `base`/`main` change set is re-read, which recomputes it behind the
answer and announces it with its `baseline`.

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
   ephemeral channel; one per finished batch of triggered recomputes (with a
   `changed` list), capped by the debounce: about 2 rows per turn with a
   burst of writes, ≤ 12 a minute under continuous writes (see "Row
   estimate").
2. Warm set = active sessions + read in the last 15 minutes.
3. Cold reads answer at once with an empty, `updating` state rather than
   waiting for the first compute.
4. `POST /worktree/refresh` becomes `202` (fire and forget).

## Changes after review

- `repo.changed` also recomputes the change set (and marks the other
  baselines stale); the UI marks it updating on `repo.changed` and refetches
  on the announcement.
- Outside-the-agent edits: a window opening a change set asks for a revalidate
  if older than 30 s; Retry revalidates regardless; the bodiless refresh (open,
  focus) recomputes the `start` change set when older than 30 s. Through the
  new optional body of `POST /worktree/refresh`.
- New repos are found by comparing the recount's repo roots with the repos
  part's, not the record before and after the Router's recount.
- Announcements: retried, then owed; one per batch; not while the part is
  queued again; not for a read-only retry ending in the same error. Error
  retries by reads limited to one a minute. Optional `baseline` field.
- At most 4 computes at once. `follow` logs head and stream failures, one line
  then at most one per 30 s, and its recovery.
- UI fail-safe re-reads instead of only clearing the mark.
- An evicted session keeps nothing: a trigger for it computes nothing, and its
  next read (the fail-safe's, at the latest) is cold, `updating`, and
  recomputes.
- Round 2: `repo.changed` recomputes a change set only where its recorded refs
  moved (repos and counts stay unconditional); two compute lanes (a session
  read in the last 60 s first, the others never holding the last slot) and at
  most 2 computes per session; the batch's 5 s cap is a timer; Retry marks
  before it asks; the bodiless refresh gives repos the same 30 s max age as
  the change set; explicit asks recompute a part at most once per 5 s, for
  every caller.
- Known, harmless: a repo leaving the session's set (no longer counted in)
  stays in the repos part until its next recompute; only new repos are
  looked for.
- Round 3: `repo.changed` always recomputes a shown session's `start` change
  set; `refs/stash` joins the `start` fingerprint and the session's own branch
  and HEAD the `base` one; an unmoved change set is announced as done; the UI
  marks the change set again on `repo.changed` for shown sessions; the lane is
  decided at grant time and a read promotes queued computes; background
  computes waiting over 30 s are promoted.
