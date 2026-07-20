# Session auto-naming: Fast Model title after first turn, stop storing truncated raw text

Task: tsk_b976633c600ffa47

No architect HANDOFF.md existed for this task; implemented directly from the
task description and acceptance criteria.

## What was done

**Stop truncating at creation:**
- `ui/src/features/sessions/launchpad/Launchpad.tsx`: `deriveSessionName` no
  longer slices/ellipsizes (removed `MAX_NAME_LENGTH=60` logic) — it stores
  the full first line of the composer text.
- `ui/src/conversation/Header/ConversationHeader.module.css`: added CSS
  truncation (`overflow: hidden; white-space: nowrap; text-overflow:
  ellipsis; max-width: 40ch; min-width: 0`) to `.name`, since it previously
  relied on the server-side truncation that's now gone. The rail
  (`SessionRail.module.css`) already had adequate `-webkit-line-clamp: 2`
  CSS truncation and needed no change.

**Auto-title job:**
- `pkg/contracts/session.go`: added `SessionNameSource` (`derived` / `user`
  / `generated`) and `Session.NameSource`.
- `services/data/internal/store/sessions.go`: validates `NameSource`,
  defaults empty to `derived` on `Create`.
- `services/router/internal/job/title.go` (new): `GenerateSessionTitle` job
  body, modeled on `summary.go`/`digest.go` — loads the session, no-ops if
  `NameSource != Derived`, asks the Fast Model for a JSON `{"title": ...}`
  from the turn delta, cleans the result (single line, trimmed trailing
  punctuation, capped at 50 runes), and renames the session with
  `NameSource: Generated`. Unconfigured Fast Model backend
  (`fastmodel.ErrUnusableOutput`) is a silent no-op, same stance as
  `GenerateSessionSummary`.
- `services/router/internal/digestwatch/watcher.go`: `Watcher.Run` now also
  spawns `job.GenerateSessionTitle` alongside the existing `GenerateDigest`
  spawn on every `session.turn_ended` envelope — no new subscription or
  `main.go` wiring needed.
- `ui/src/features/sessions/launchpad/Launchpad.tsx` / `router/types.ts`:
  session creation now sends `name_source: "derived"` explicitly.

**Turn-detection approach:** there's no turn-counter anywhere in the
contracts/dataclient surface, so instead of adding one, the job gates purely
on `NameSource == Derived`. Since `Watcher` fires on every turn for every
session, this job runs on every turn too, but only ever *succeeds* in
renaming once — the first time the Fast Model returns usable output — after
which `NameSource` flips to `Generated` and all later runs no-op. If no Fast
Model backend is ever configured, this job harmlessly retries every turn
forever (matches existing `GenerateDigest`-with-linked-sessions-off
precedent — no errors, no side effects).

## What was NOT done

- **The UI's rename affordance does not persist to the backend at all** —
  `ConversationHeader.tsx`/`ConversationTab.tsx`'s rename is local React
  state only. There is no `router.updateSession()` call anywhere in `ui/src`
  and no PATCH/PUT `/sessions/{id}` route in the Router API. This is a
  **pre-existing gap**, not introduced by this task, and out of scope per
  "smallest change / no drive-by refactors" — fixing it would mean adding a
  new API route and wiring, well beyond "auto-naming." The acceptance
  criterion "renaming manually ... the job never overwrites a user-set name
  (tested)" is satisfied by a Go-level test
  (`TestGenerateSessionTitleNeverOverwritesNonDerivedName`) that sets
  `NameSource: User` directly via `dataclient`, proving the job's guarantee
  holds — but there is no end-to-end UI test of a human clicking rename,
  because that path doesn't reach the backend yet.
- No new integration test at the `digestwatch` level proving the full
  `turn_ended` → job-spawned → session-renamed path; relied on the existing
  job-level tests (`title_test.go`) plus pre-existing watcher wiring tests
  (`watcher_test.go`, confirmed compatible — its non-JSON `stubBackend`
  replies cause the title job to gracefully no-op via `ErrUnusableOutput`,
  not breaking any assertions).

## What the reviewer should scrutinize

- The "respect the human" guarantee is currently only meaningful at the
  backend/API level, not end-to-end from the UI, because of the pre-existing
  rename-doesn't-persist gap above. If/when that gap is closed, double-check
  the UI sends `name_source: "user"` on manual rename.
- `cleanGeneratedTitle` in `title.go` uses rune-based slicing (not byte
  slicing) to avoid splitting multi-byte UTF-8 characters when capping at 50
  characters.
- The perpetual-retry-on-every-turn behavior when the Fast Model is
  unconfigured (or the session is stuck on `Derived` for some other reason)
  is a deliberate no-op-forever choice matching existing digest-job
  precedent, not a bug.

## Verification

- `go build ./...` clean.
- `go test ./services/... ./pkg/... ./providers/...` all green (includes
  new `title_test.go`, 5 tests: golden path rename, cap/single-line
  cleanup, never-overwrites-non-derived (table test over User/Generated),
  skips-without-backend, no-op-for-deleted-session).
- `npx tsc --noEmit -p tsconfig.json` (in `ui/`) clean.
- `npx vitest run` (in `ui/`): 199 files, 1044 tests, all green — includes
  updated `Launchpad.test.tsx` (full-first-line assertion, `name_source`
  in the create payload, new long-first-message test).
