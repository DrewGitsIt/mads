# Linked sessions L2b: per-turn digest delivery to monitors

Ref: `docs/LINKED-SESSIONS-DESIGN.md` decision 4 (the `read` grant's push
half). Builds on L1 (link records) and L2a (inject path), both already
landed on `main`.

## What shipped

**Contracts** (`pkg/contracts`):
- `LinkCadence` type (`link.go`): `CadencePerTurn` (default) / `CadenceManualOnly`,
  with a `LinkCadences` registry and `Valid()`, mirroring `Grant`'s shape.
  `Link.Cadence` and `LinkRequest.Cadence` (omitempty ⇒ server defaults to
  `per_turn`).
- `TurnEnded EventType = "session.turn_ended"` (`event.go`), payload
  `{"delta": "..."}` — the watched session's rendered turn delta.
- Golden fixture (`testdata/link.json`) and `link_test.go` updated for the
  new field.

**Data Service** (`services/data`):
- Migration `0013_link_cadence`: `ALTER TABLE links ADD COLUMN cadence TEXT
  NOT NULL DEFAULT 'per_turn'` up; plain `ALTER TABLE ... DROP COLUMN`
  down (confirmed supported by the SQLite driver in use — no table-rebuild
  fallback needed).
- `store/links.go`: `Create`/`Update` default an empty `Cadence` to
  `per_turn` and validate against `LinkCadences`; `writeLink`/`scanLink`
  thread the column.
- `api/links.go`: POST/PUT handlers pass `Cadence` through to the store.

**Session runner** (`services/session/internal/runner`):
- New file `digest.go`: `digestAccumulator`, an unexported per-turn buffer
  distinct from `synth.Engine`'s multi-turn buffer — it accumulates
  `recordUser`/`recordEvent` calls and `renderAndReset()`s on every turn end,
  giving each digest exactly one turn's activity. Guarded by its own mutex
  (separate from `Runner.mu`) since `recordUser` runs on the `Send`/`Inject`
  goroutine while `recordEvent` runs on the `pump`/`pumpPTY` goroutine.
- `runner.go`: wired at all five `synthEngine` call sites (`Send`, `Inject`'s
  immediate branch, `onTurnEnd`'s queued-delivery branch, `pump`, `pumpPTY`).
  New `pushTurnEnded()` renders and resets the buffer and best-effort appends
  a `TurnEnded` envelope (log-and-skip on failure, same pattern as every
  other `r.data.Append` in this file). Called at the top of `onTurnEnd()`,
  **before** `busy` is cleared — this ordering is load-bearing: it prevents a
  concurrent `Send`/`Inject` for the next turn from recording into the
  not-yet-drained current turn's buffer.
- Tests: a turn's digest carries the reply text and is delivered as exactly
  one `TurnEnded` envelope; two sequential turns each get their own envelope
  with no cross-turn leakage.

**Router job** (`services/router/internal/job`):
- `job.go`: added `SessionResolver` interface (`SessionAddr(sessionID)
  (string, bool)`) and `Deps.Resolver` — defined in `job` rather than
  imported from `internal/supervisor` to avoid an import cycle (`supervisor`
  already imports `job`). `Runner.SetResolver` populates it; `*Runner.invoke`
  threads it into each job's `Deps`. Additive — no existing job body or
  `job.New(...)` call site changed shape.
- New file `digest.go`: `GenerateDigest(sessionID, delta string) Func`. Loads
  the watched session (no-op on `ErrNotFound`), no-ops if
  `FeatureFlags.LinkedSessions` is off, filters the session's links to
  `SubjectSessionID == sessionID && HasGrant(GrantRead) && Cadence !=
  CadenceManualOnly`. Early-exits before any Fast Model call if there are no
  targets or no resolver. One `DoText` compression call is shared across all
  targets (not one per target). Fan-out is per-link best-effort: a resolver
  miss or a delivery error is logged and skipped, never propagated — the job
  itself always returns nil for delivery-side failures.
- `deliverDigest`: a small standalone `net/http` POST to `addr + "/inject"`
  with `contracts.InjectRequest{Text, Attribution}`. Deliberately not
  `api.postJSONToChild` — that helper is coupled to an inbound
  `(ResponseWriter, *Request)` for proxying a response back to a caller,
  which doesn't exist inside a detached job goroutine.
- Tests (`digest_test.go`): per_turn read link gets exactly one delivery with
  correct `Attribution{LinkID, SourceName}`; manual-only and inject-only
  links get none; a manual-only-only subject makes zero Fast Model calls
  (the cheap early exit); one of two per_turn links' delivery failing (500)
  doesn't prevent the other's delivery and doesn't fail the job
  (`job.completed`, not `job.failed`).

**Router digest watcher** (new package `services/router/internal/digestwatch`):
- `Watcher.Run(ctx)` subscribes to `session.turn_ended` globally
  (`dataclient.Client.Stream(ctx, 0, "", []string{...})` — empty session ID
  means every session) and spawns a `GenerateDigest` job per envelope.
  Decode failures are logged and skipped; they don't stop the subscription.
- Wired into `services/router/main.go`: `jobs.SetResolver(sup)` right after
  `sup` is constructed; `watcher := digestwatch.NewWatcher(data, jobs)` and
  `go watcher.Run(ctx)` right after the existing `ctx, stop :=
  signal.NotifyContext(...)` — no reordering of that line was needed, since
  the watcher only needs `ctx` at the `Run` call site, which already sits
  after it.
- Tests (`watcher_test.go`): appending a `TurnEnded` envelope directly (no
  real session child) results in the reading link's monitor receiving a
  delivery; an unrelated event type spawns no job.

## What I did not do

- **No visual digest rendering.** `ChatRenderer.tsx` already renders any
  attributed message as "⇄ from Leader · {name}"; L2b reuses that path
  unchanged. Distinguishing digests from L2a injections visually is L4's job
  per the task's explicit non-goals.
- **No "catch me up" leader tool / new agent capability.** The Router's
  `GET /sessions/{id}/transcript` route has no grant-based ACL today, so a
  monitor's agent can already read the watched session's transcript with
  zero code changes — `GrantRead`'s doc comment already anticipates this.
  Giving an agent an actual tool to invoke that on its own initiative is
  L2c's leader-capability work.
- **No `Attribution` discriminator field** distinguishing digest deliveries
  from L2a injections at the data-model level — not required by this task's
  acceptance criteria; add if/when L4 actually needs it.
- **No full multi-process integration test** spawning real session children
  end-to-end. The four seams (runner emits envelope → watcher reacts → job
  fans out → delivery is best-effort) are each covered directly by their own
  package's tests; a `testchild`/`supervisor_test.go`-style full-stack test
  would be redundant with those and was treated as lower priority per the
  original task plan.

## What the reviewer should scrutinize

- **`onTurnEnd()`'s call ordering in `runner.go`**: `pushTurnEnded()` runs
  before `busy` is cleared. If a future edit reorders this, a concurrent
  `Send`/`Inject` for the next turn could start writing into the current
  turn's not-yet-rendered digest buffer, silently corrupting digest content
  across a turn boundary.
- **The digest buffer is never actually empty in normal use.** `recordUser`
  fires on every `Send`/`Inject`, before the turn starts, so even a
  zero-output agent turn still produces a non-empty delta (just the user's
  own line). The `renderAndReset() == ""` skip path in `pushTurnEnded()`
  exists for correctness/defense but is not exercised by the current test
  suite via a normal `Send`-driven turn — worth a synthetic unit test on
  `digestAccumulator` directly if that skip path needs direct coverage later.
- **`GenerateDigest`'s feature-flag check** lives inside the job body
  (`deps.Data.Config(ctx)`), not in `digestwatch.Watcher`, matching this
  task's suggested enforcement point. This means the watcher always
  subscribes and always spawns jobs regardless of the flag; the flag is
  checked (cheaply) once per spawned job instead. If linked sessions is off
  for the entire process lifetime, this is a small amount of always-on
  overhead (one `Config` fetch per turn end, of any session) — acceptable
  but worth knowing about if it needs tightening later.
- **`deliverDigest`'s status-code check is `http.StatusAccepted` (202)**,
  matching the session service's actual `/inject` handler response
  (`handleInject` in `services/session/internal/api/api.go`) — this is a
  slight deviation from the architect's draft handoff (which didn't specify
  a status), confirmed against the live handler rather than assumed.
- **Duplication between `job.deliverDigest` and
  `api.handleLinkInject`/`postJSONToChild`**: both POST an `InjectRequest` to
  a child's `/inject`, but from different execution contexts (job goroutine
  vs. HTTP handler). Left as intentional, small (~15 line) duplication per
  the architect's draft rather than a cross-package refactor.

## Verification

- `go build ./...`: clean.
- `gofmt -l` on every new/changed Go file: clean.
- `go test ./services/session/... ./services/router/... ./pkg/contracts/... ./services/data/...`: all pass.
- `make check` (full repo: Go tests across all services + UI vitest suite,
  772 tests): exits 0, no failures.

Task: tsk_8cc0ed4f2b6be0b9
