# Executor handoff: Linked sessions L3 — implicit hand-raiser over own pipeline runs

## What was built

Implements docs/LINKED-SESSIONS-DESIGN.md decision 6 (+ 2026-07-16 amendment) per the
architect's HANDOFF.md spec, which this replaces.

**Data model.** `pkg/contracts/link.go`: added `Link.Implicit bool` — marks a link the
system created without user action. Migration `0014_link_implicit` adds the column
(`ALTER TABLE links ADD COLUMN implicit INTEGER NOT NULL DEFAULT 0`); `MaxVersion`
bumped 13→14.

**Store.** `services/data/internal/store/links.go`: threaded `implicit` through every
link SELECT/INSERT/scan. Added `Links.CreateImplicitHandRaiser(ctx, sessionID, actor)`
and `Links.RemoveImplicitHandRaiser(ctx, sessionID, actor)` — a dedicated path (not
`Create`/`Delete`) since `Create` rejects self-links by design. Builds a
self-referential `GrantingSessionID == SubjectSessionID == sessionID` link with
`PresetGrants[PresetHandRaiser]` ({read, inject}), `Implicit: true`. Create is
idempotent (returns the existing implicit link if one is already present); remove is a
no-op if absent.

**API.** `services/data/internal/api/links.go`: `POST/DELETE /sessions/{id}/hand-raiser`,
hardcoded actor `"system"` (Router-only, no user-facing route reaches these).
`resolveGrants` (existing) already refused `hand_raiser` from the general `/links`
route — no change needed there.

**dataclient.** `pkg/dataclient/dataclient.go`: `CreateHandRaiser`/`RemoveHandRaiser`.

**Supervisor.** `services/router/internal/supervisor/supervisor.go`: detects both
documented edges under the existing `s.mu` lock, then grants via a detached goroutine
(`context.Background()`, matching the existing onExit pattern):
- Edge A: `SpawnPipeline` — a run starting for a session with an already-live
  interactive child.
- Edge B: `SpawnSession` — a session's interactive child activating while a run is
  already in flight for it.

`onPipelineExit` revokes the implicit hand-raiser (if the session has no other live
pipeline) *before* the `expected` early-return, so both normal completion and
crash/cancel tear it down. Grant/revoke are best-effort — feature-flag-gated,
logged-and-swallowed on error, never fail the pipeline lifecycle.

**Digest delivery.** New `services/router/internal/job/run_digest.go`:
`GenerateRunDigest(sessionID, taskID, toStatus)` mirrors `GenerateDigest`'s shape but
targets `Implicit`-only links (not every read-holder on the session — a deliberate,
narrower filter than L2b's, so it doesn't silently start delivering to ordinary
Monitor links on the same subject). Compresses "task %q is now %s" (+ the *last*
blocker-typed note's content, scanned backwards through `task.Notes`, when landing on
`blocked`) via the Fast Model, delivers with `Attribution.SourceName: "your pipeline
run"` — a fixed string rather than the session's own `Name`, since granting == subject
here and the session's own name would read as a strange self-reference.

`services/router/internal/digestwatch/watcher.go`: added `RunWatcher` alongside the
existing `Watcher` in the same package (package doc comment updated to describe both
bridges). Subscribes to `task.status_changed` globally, decodes `{"from","to"}`,
filters to `to ∈ {done, blocked}`, spawns `job.GenerateRunDigest`. Wired into
`services/router/main.go` next to the existing turn-digest watcher.

## Tests added

- `pkg/contracts/link_test.go`: `Implicit` round-trip.
- `services/data/internal/store/links_test.go`: create/idempotent/remove/remove-when-absent.
- `services/data/internal/api/links_test.go`: hand-raiser route round trip.
- `services/router/internal/job/run_digest_test.go`: delivers to the implicit
  hand-raiser with correct attribution; blocker note folded into the prompt on
  `blocked`; a non-implicit read link on the same subject is *not* targeted.
- `services/router/internal/digestwatch/watcher_test.go`: `RunWatcher` spawns on a
  `done` transition; ignores non-terminal transitions.
- `services/router/internal/supervisor/supervisor_test.go`: Edge A, Edge B, no grant
  without a live child, and revoke on `TerminatePipeline`.

`make check` (lint + full Go test suite + UI vitest suite) is green.

## What was not done / scope notes

- No UI work — this task is data/backend plumbing only, per the acceptance criteria.
- Did not generalize "any monitor over a live-pipeline session also gets task
  digests" — `GenerateRunDigest` intentionally targets `Implicit` links only. Flagged
  in the code as a reasonable fast-follow, not required here.
- Did not add a test that races Edge A and Edge B against each other concurrently
  (e.g. `SpawnSession` and `SpawnPipeline` firing in the same instant) — the
  `CreateImplicitHandRaiser` idempotency guard in the store makes this safe (a second
  concurrent grant call returns the existing link), but there's no test exercising the
  race directly.

## What a reviewer should scrutinize

- `onPipelineExit`'s ordering: the revoke call sits *before* the `expected`
  early-return so it fires on every exit path. Worth double-checking no other early
  return in that function bypasses it.
- The blocker-note lookup in `run_digest.go` scans `task.Notes` backwards for the last
  entry with `Type == NoteBlocker` (not just checking whether the very last note
  happens to be one) — this fixes a bug I caught in the architect's original snippet
  during implementation.
- `SourceName: "your pipeline run"` is a fixed string, a judgment call for how L4
  eventually renders self-referential attribution — flagged as not load-bearing in a
  code comment.

Task: tsk_db5e40ad752ec65a
