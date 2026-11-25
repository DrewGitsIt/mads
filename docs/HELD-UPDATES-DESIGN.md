# Held link updates — design (agreed 2026-11-05)

Mockup: `docs/mockups/held-updates.html`, section **C (revised ×2)**. The
other sections (A, B, D) are reference only.

## Problem

A link holder (e.g. a Follow leader) that is asleep gets no digests: they are
deferred into the link mailbox (`link_digests.deferred = 1`) and flushed as a
separate forced turn when its child next starts (`job.FlushDeferred`, wired
through `Supervisor.SetOnSessionStart` in `services/router/main.go`). The user
couldn't tell any of this:

- the rail said **Idle** for a session with no child (Idle/Sleeping were merged
  on 2026-10-22, `railGroups.ts` `RailStatusKind` comment);
- the rail's ⏸ is drawn on every card, so a sleeping card looked pausable,
  i.e. running;
- the conversation showed nothing of the held updates, while the older
  NOOP pill ("3 updates from X · no action needed") read like part of the rail's
  "5 updates waiting".

## Decisions

1. **Remove the rail card's ⏸.** ✓ (Done + sleep) covers it; the workspace
   kebab keeps "Sleep now" (also the drift-recovery path).
2. **Bring back a Sleeping status** on rail cards: a session with
   `activity === "inactive"` that would otherwise read "Idle" reads "Sleeping"
   (with blocked tasks: "Sleeping · N blocked tasks").
3. **Rename the NOOP pill** to "N earlier updates from X · no action needed".
4. **Held updates are listed in the conversation** (end of stream; no
   divider — see "As built"), grouped by source session, one compact row
   per update, click to expand.
5. **Held updates are attached to the next message automatically.** There is
   no "hold" state: an update either goes to the agent or is discarded.
   - A user send that wakes the session carries every held update in the
     **same turn**, ahead of the user's text.
   - "Send updates only" (and the kebab's "Wake now", and signal wakes)
     deliver them the way `FlushDeferred` does today: one turn, no user text.
   - × on a row discards that update; × on the composer chip discards all.
     Both offer Undo.
6. **Wire format — `attached` flag (option a).** Each attached update is a
   normal `<mads-message …>` block with `attached="true"`; the user's own text
   follows the last attached block, unwrapped. `linkwrap.Parse` treats text
   after an attached block as the human's (a segment with no Attribution), not
   as part of the block body. Messages without the flag parse exactly as before.
   The NOOP offer is **not** appended when updates ride with a human message.
7. **The router decides what is attached (option a)**: whatever is held at the
   moment of the waking send. It drains the held updates *before* waking the
   child, so the `FlushDeferred` start hook finds nothing (no race, no second
   turn). On forward failure the drained entries are re-queued (same as
   FlushDeferred's requeue). The runner, not the router, wraps (the existing
   "runner wraps at one point" rule, `pkg/linkwrap` package doc).
8. **Attach only on a send that wakes the session (option a).** Held updates
   only accumulate while the recipient is asleep; a send to an awake session is
   unchanged. (Leftovers from a failed flush keep today's retry-on-next-start.)
9. **Discard = soft delete (option a).** Discarded rows stay in `link_digests`
   with `discarded_at` set (excluded from counts, drains and flushes), purged
   after 30 days. Undo clears `discarded_at`. The held list shows
   "N discarded · Undo" until the next send; long-term audit is the event log
   (a `link.digest_discarded` / `link.digest_restored` event per action).
   No permanent in-timeline "discarded by you" chip (that would need a
   MADS-owned transcript store; the chat stream deliberately has none).

## Backend shape (step 2)

- **Data Service**
  - Migration: `link_digests.discarded_at TEXT NOT NULL DEFAULT ''`.
  - `DeferredCounts`, `DrainDeferred`, `drain`: never return discarded rows;
    a deferred drain deletes the link's discarded rows as well (see below).
  - `GET /sessions/{id}/held-updates` → deferred, non-discarded entries on
    every link where the session is the granting side, oldest first, each with
    `id`, `link_id`, `source_session_id`, `source_name` (from the stored
    Attribution, else the subject session's name), `text`, `created_at`, `noop`.
    Also returns `discarded` (same shape): the discarded rows still stored.
    A deferred drain (waking send or FlushDeferred) hard-deletes the link's
    discarded rows too, so "discarded" naturally means "discarded during this
    sleep" and disappears after the next send.
  - `POST /link-digests/discard {ids: [...]}` and
    `POST /link-digests/restore {ids: [...]}`; both emit the waiting-count
    change (`appendWaitingChanged`) so `session.updated` fires.
  - Purge discarded rows older than 30 days (for sessions that never wake; on discard is enough).
  - `contracts.LinkDigest` gains `ID int64` (json `id,omitempty`) so drained
    entries can be named.
- **Router**
  - Proxy the held-updates list and discard/restore routes (dataclient methods).
  - `POST /sessions/{id}/held-updates/deliver` = ensureSession + FlushDeferred
    (the "Send updates only" button).
  - `handleSessionMessages`: if no child is tracked (the send will wake it),
    drain held updates (all granting links) **before** `ensureSession`, then
    forward the message with them attached (`attached: []InjectPart`, built the
    same way FlushDeferred builds parts, incl. the anchor prefix when >1). On
    any failure to forward, re-queue them. Under the session's wake lock so a
    concurrent signal wake can't interleave.
- **Session runner**
  - `/messages` accepts `attached []contracts.InjectPart`.
  - `Send` / `SendOrQueue` take the attached parts: wire = each part
    `linkwrap.Wrap`ped with `Attached: true`, joined, then `\n\n`, then the
    user's text (re-root note handling unchanged, still outermost).
    `synthEngine.OnUserSend` and `digest.recordUser` get **only the user's
    text**; each part gets `recordInjectionAudit`. Live `user_message` frames
    must segment the same way the durable transcript does (`SegmentKey`).
- **pkg/linkwrap**
  - `contracts.Attribution.Attached bool` → `attached="true"` attribute.
  - `Parse`: after a block whose Attribution is Attached, trailing text is a
    human segment (`Segment{Text, Human: true}` or equivalent), not appended to
    the block and not a notice.
  - `Expand`: a human segment becomes a row with **nil Attribution** (the
    human's own), keyed by `SegmentKey`.
  - `SameSend`: an attached block followed by the human segment of the same
    send is one send (turn counting / `SplitTurns` count it once).

## UI shape (steps 1 and 3)

- Step 1: ⏸ removal, Sleeping status (+ StatusMark mark), pill rename.
- Step 3: held list in the stream, composer chip, attached-updates strip on
  the user's sent message (consecutive attached rows + the following human row
  render as one user bubble with an expandable "⌗ N updates attached" strip),
  Send updates only, Discard row/all with Undo toast, rail "N updates waiting"
  click → open session and scroll to the held list.

## As built (2026-11-05)

- Routes: `GET /sessions/{id}/held-updates` → `{held, discarded}`;
  `POST /link-digests/discard|restore {ids}` → `{changed}`;
  `POST /sessions/{id}/held-updates/deliver` → `{delivered}`. The Router
  strips any client-sent `attached` from a send; only held updates attach.
- The wake lock is held across drain, wake and forward of a waking send; a
  failed wake or a non-2xx from the child re-queues (on a context detached
  from the request).
- Cancelling a queued message that carries attached updates keeps them as
  an injection of their own.
- UI: no "Asleep since" divider (dropped 2026-11-05: the rail's Sleeping
  status, the list header and each row's own time already say it); the attached strip
  on a sent message has no per-update times (rows carry none); `[turn N] `
  anchor prefixes are stripped for display; the held list is newest first.
- Not gated on `FLAGS.linkedSessions`: without links there is never anything
  held.
