# HANDOFF: Engine — emit usage.recorded events so cost metrics have data

## What was done

`pkg/contracts.UsageRecorded` was already registered with its payload shape documented;
`services/data`'s `Metrics.Cost` already aggregates it. This closes the gap: nothing was
emitting the event, and the provider layer wasn't carrying cost through to the engine.

1. **`providers/provider/event.go`** — added `Usage.CostUSD float64` (`json:"cost_usd,omitempty"`).
   Shared by `OneShotResult.Usage` and `Event.Usage` (turn-based sessions); only the
   one-shot path is wired to it this task (session-turn `EventTurnEnd` cost emission is
   still future wiring per the type's own doc comment — out of scope here).

2. **`providers/claudecode/claudecode.go`** — `parseOneShot` now sets `Usage.CostUSD` from
   `l.TotalCostUSD` **unconditionally**, not nested inside the `l.Usage != nil` guard —
   `total_cost_usd` is a top-level result-line field, independent of whether the nested
   `usage` object is present. Updated `TestOneShotParsesCapturedResult` to assert the cost
   from the pinned real fixture (`0.0212739`).

3. **`providers/fake/scenario.go`** — added `Rule.Usage *UsageOverride` (`UsageOverride{Model,
   InputTokens, OutputTokens, CostUSD}`). A `nil` override changes nothing (deterministic
   length-based accounting, cost 0, same as before); a non-nil override replaces `Model`/
   token fields only when non-zero, and always sets `CostUSD` (0 is itself a valid scripted
   cost). No `Validate()` change needed.

4. **`providers/fake/fake.go`** — `turnPlan` carries the matched rule's `usage *UsageOverride`
   through to `Fake.OneShot`, applied via a new `applyUsageOverride` helper. Scoped to
   `OneShot` only: `fakeSession.finish()` (the interactive-session turn-end path) is
   untouched — it doesn't have `plan` in scope, threading it through would touch 3 call
   sites, and nothing downstream consumes session-turn cost yet. Flag this if a future task
   needs scriptable cost for interactive-session tests too.

5. **`services/pipeline/engine/engine.go`** — added `engine.recordUsage`, called right after
   a successful `OneShot` in `runAgent` (before the pass/fail-marker check, since the
   one-shot cost money either way). It posts `contracts.UsageRecorded` with payload
   `{model, input_tokens, output_tokens, cost_usd}` via `dataclient.Client.Append`,
   attributed to the run's session and the task. **Deviation from best-effort via the
   existing silent `e.event()` helper**: the task text explicitly calls for "log +
   continue," so `recordUsage` is a separate method that `log.Printf`s on a failed post
   rather than swallowing it silently like the other lifecycle events do.

6. **Tests** — new file `services/pipeline/engine/usage_test.go` (kept separate from the
   already-large `engine_test.go`):
   - `TestUsageRecordedFeedsCostMetrics`: two independent trivial-labeled tasks against the
     seeded `default-implementation` template, with `fake.Scenario` rules scripting exact,
     *different* cost/tokens/model per stage (`executor` vs `code_review`) so `by_day` (two
     buckets, one per model) and `by_session` (one bucket, summed) are asserted against
     exact expected numbers, not just "non-zero."
   - `TestUsageRecordFailureDoesNotBlockStage`: fronts the real `datatest` server with a
     reverse-proxy `httptest.Server` that returns 500 only for `POST /events` carrying a
     `usage.recorded` envelope, forwarding everything else untouched. Confirms the task
     still completes (`StatusDone`) and — to prove the test isn't vacuously passing —
     that no cost landed in `GET /metrics/cost` for the session.

7. **`services/pipeline/README.md`** — added a critical-paths bullet naming both tests.

## What was not done

- Session-turn (`EventTurnEnd`) cost emission from `services/session`'s runner — explicitly
  future wiring per `contracts.UsageRecorded`'s doc comment, not this task's scope.
- Scriptable usage for the fake's interactive-session path (`fakeSession.finish`) — see
  deviation note above.
- The npm/UI half of `make check` (`ui-deps` + `npm run lint`/`test`) was not run: no UI
  files were touched by this change, and `ui/node_modules` isn't present in this sandbox
  (would require a fresh `npm ci`, network-dependent). Ran `go build ./...`, `go test -race
  ./...` (all packages green), and `golangci-lint run ./...` (0 issues) instead.

## Reviewer should scrutinize

- The `recordUsage` vs. reusing the silent `e.event()` helper choice (point 5 above) — a
  case could be made either way; I judged the task's explicit "log + continue" wording as
  the deciding factor.
- `UsageOverride` as a pointer field on `Rule` (nil = unscripted) vs. flat fields directly
  on `Rule` — either is fine; pointer-nil more cleanly distinguishes "not scripted" from
  "scripted zero cost" without relying on the coincidence that unscripted cost also
  defaults to 0.
- Float comparisons in the new tests use `math.Abs(a-b) > 1e-9`, matching
  `pkg/dataclient/metrics_test.go`'s convention (`0.10 + 0.10 + 0.20 + 0.20` is not exactly
  `0.60` in float64).
