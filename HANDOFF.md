# Usage telemetry: stage attribution and cache tokens on usage.recorded

## What changed this pass

Implemented all four items from the architect's design doc (below the fold,
unchanged) as written — no deviations from the plan.

1. `services/pipeline/engine/engine.go`: `recordUsage` now takes `stage
   string, attempt int` alongside the existing `taskID`/`usage` params. The
   call site in `runAgent` (line 685) passes `st.Name` and the `attempt`
   already computed at line 663 — no new bookkeeping needed. The marshaled
   payload gained `"stage"` and `"attempt"` keys.

2. `providers/provider/event.go`: `Usage` gained `CacheCreationInputTokens`
   and `CacheReadInputTokens` (`omitempty` int, matching the existing
   token-count style).

3. `providers/claudecode/wire.go`: `wireUsage` gained the two matching JSON
   fields; `wireLine.events()`'s streaming turn-end path copies them into
   `provider.Usage`.

4. `providers/claudecode/claudecode.go`: `parseOneShot`'s non-streaming path
   copies the same two fields — both wire→provider.Usage conversion sites
   now agree.

5. `pkg/contracts/event.go`: `UsageRecorded`'s doc comment now lists the
   full payload shape (`stage`, `attempt`, both cache fields), states the
   additive/unknown-not-zero contract explicitly, and corrects a stale claim
   — pipeline emission has been live since `engine.go`'s original
   `recordUsage` landed; only `services/session`'s runner wiring is still
   future work.

## Tests

- `providers/claudecode/claudecode_test.go`:
  `TestOneShotParsesCapturedResult` now also asserts
  `CacheCreationInputTokens == 8827` and `CacheReadInputTokens == 28169` —
  the real captured fixture (`testdata/oneshot-result.json`) already
  contained both fields, so no fixture change was needed, just new
  assertions.
- `services/pipeline/engine/usage_test.go`: added
  `TestUsageRecordedNamesStageAndAttempt`. Reuses the same
  always-fails-`code_review` fake scenario as the existing
  `TestDefaultTemplateReviewLoopBack` (default template, `code_review`'s
  `on_fail.loop_back: executor, max_loops: 2`). Asserts every
  `usage.recorded` event's `stage` field is one of `executor`/`code_review`
  (never empty or unexpected), that `executor` posts 3 usage events (initial
  run + 2 loop-back re-runs), and that `code_review`'s own `attempt` climbs
  `[1, 2, 3]` across its three runs — `code_review` is the stage whose
  `on_fail` key increments (engine.go's `e.loops[taskID+"|"+st.Name]`), so
  its own re-entries are the ones where `attempt` is guaranteed to actually
  increment under the existing loop-counter semantics; `executor`'s attempt
  stays 1 each time since only the *failing* stage's key increments, not the
  loop-back target's — this is pre-existing behavior, not something this
  task changed.
- Old-event parse tolerance (criterion 4) needed no new production code or
  test: `services/data/internal/store/metrics.go`'s `usagePayload` and
  `pkg/dataclient`'s reader already decode into local structs that don't
  declare the new fields, and Go's `json.Unmarshal` already ignores
  unknown/missing JSON keys. `pkg/dataclient/metrics_test.go` already
  exercises payloads shaped exactly like pre-this-change events (no `stage`/
  `attempt`/cache keys) through the real `Cost` query path, and that test
  still passes unmodified — confirming the tolerance rather than asserting
  it freshly.

Ran `go build ./...`, `go vet ./...`, and `go test ./...` (full repo) —
all pass.

## What I did not do

- No UI/cost-panel changes (explicitly out of scope per the task).
- Did not add cache-token fields to `providers/fake`'s `UsageOverride` — no
  acceptance criterion touches cache accounting through the fake provider,
  only the real Claude Code wire path (`providers/claudecode`).
- Did not touch `services/session`'s runner — it doesn't call a
  `recordUsage`-equivalent today; `UsageRecorded`'s doc comment already
  called this out as future wiring and still does.

## For the reviewer

- Core diff is five files: `pkg/contracts/event.go`,
  `providers/provider/event.go`, `providers/claudecode/{wire,claudecode}.go`,
  `services/pipeline/engine/engine.go`, plus the two test files.
- Worth double-checking: the `attempt`-increments-per-stage semantics
  described above in the test rationale — it surprised me that the
  loop-back *target* (`executor`) doesn't see its own attempt climb, only
  the *failing* stage (`code_review`) does. That's `e.loops` keying on
  `st.Name` of whichever stage's `on_fail` fired, which was already true
  before this task and is unchanged; flagging it since "attempt increments
  on loop-back" reads ambiguously without knowing which stage the increment
  attaches to.

---

# Usage telemetry: stage attribution and cache tokens on usage.recorded (architect handoff)

## Goal

`usage.recorded` events currently carry no stage/attempt attribution, forcing
a fragile seq-adjacency join against `stage.completed`. Add `stage` and
`attempt` to the payload, and add the two cache-token fields Claude Code's
result line already reports but the adapter drops. Additive only — no
existing reader breaks.

## 1. Thread stage + attempt into recordUsage

`services/pipeline/engine/engine.go`:

- `runAgent` (line ~651) already computes `attempt` in scope (line 663:
  `attempt := e.loops[task.ID+"|"+st.Name] + 1`) and has `st template.Stage`
  as a parameter, so `st.Name` is right there too.
- The call site is line 685: `e.recordUsage(ctx, task.ID, res.Usage)`.
  Change it to pass `st.Name` and `attempt` through, e.g.
  `e.recordUsage(ctx, task.ID, st.Name, attempt, res.Usage)`.
- `recordUsage` (line 735) gains matching parameters and adds `"stage"` and
  `"attempt"` keys to the marshaled payload map, alongside the existing
  `model`/`input_tokens`/`output_tokens`/`cost_usd`.

recordUsage has exactly one call site — no other caller to update.

## 2. Cache tokens through the Claude Code wire path

`providers/provider/event.go`: add two fields to `Usage` (~line 49), mirroring
the existing `input_tokens`/`output_tokens` style:
```go
CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
CacheReadInputTokens      int `json:"cache_read_input_tokens,omitempty"`
```

`providers/claudecode/wire.go`:
- `wireUsage` (line 57) currently only has `InputTokens`/`OutputTokens`. Add
  `CacheCreationInputTokens int \`json:"cache_creation_input_tokens"\`` and
  `CacheReadInputTokens int \`json:"cache_read_input_tokens"\`` (real field
  names verified in `providers/claudecode/testdata/oneshot-result.json`,
  already present in that fixture).
- `wireLine.events()` (line ~118-123) builds `provider.Usage` from
  `l.Usage` for the streaming turn-end case — copy the two new fields
  through there too.

`providers/claudecode/claudecode.go`: `parseOneShot` (line 254) builds
`provider.Usage` from `l.Usage` for the one-shot JSON path (lines 269-272)
— copy the two new fields through there as well. Both conversion sites need
the change; missing either one leaves a gap depending on streaming vs.
one-shot mode.

## 3. Payload doc comment

`pkg/contracts/event.go` (~line 216-224): `UsageRecorded`'s doc comment spells
out the payload shape. Update it to mention `stage`, `attempt`,
`cache_creation_input_tokens`, `cache_read_input_tokens` alongside the
existing fields, and note they're additive (old events lack them).

## 4. Backward compatibility — verify, don't add new code

`services/data/internal/store/metrics.go`'s `usagePayload` struct (line 28)
and `pkg/dataclient`'s equivalent (check `metrics_test.go` for the reader
shape) decode into local structs that don't declare the new fields — Go's
`json.Unmarshal` already ignores unknown/missing keys, so no reader change
is needed. Do NOT add `omitempty`-driven zero-value defaulting logic on the
*producer* side to fake "unknown" semantics — the requirement is just that
old events (which predate these keys) still decode without error, which
they already will. Nothing here needs new production code; confirm with a
test instead (see below).

## Tests

**Wire fixture** (`providers/claudecode/claudecode_test.go`,
`TestOneShotParsesCapturedResult`, ~line 43): the fixture
(`testdata/oneshot-result.json`) already contains
`"cache_creation_input_tokens":8827` and `"cache_read_input_tokens":28169`.
Add assertions that `res.Usage.CacheCreationInputTokens == 8827` and
`res.Usage.CacheReadInputTokens == 28169` to the existing test — no new
fixture needed.

**Engine stage attribution** (new test in
`services/pipeline/engine/usage_test.go` or `engine_test.go`): use the
`harness` + `h.events(t, "usage.recorded")` helper (see
`TestDefaultTemplateReviewLoopBack` in `engine_test.go` for the pattern) to
fetch posted envelopes and decode `stage`/`attempt` from the JSON payload.
Drive a loop-back like `TestCommandFailureFeedbackLoop` does — a small
inline template with an agent stage whose `on_fail.loop_back` points at
itself — and a `fake.Rule` that fails the first call and passes once the
retry's prompt contains "Address this feedback" (same trick that test
already uses). Assert: first `usage.recorded` event for the stage has
`attempt: 1`, the retry has `attempt: 2`, and both name the correct stage.

**Old-event parse tolerance**: add or extend a test around
`services/data/internal/store/metrics.go`'s `usagePayload` (or the
dataclient-side reader) that decodes a payload missing the new keys and
confirms it doesn't error and the new fields read as zero/unknown — not
silently treated as "zero cost" in any place that distinguishes the two
(there currently isn't such a place, so this is mostly a regression guard).

## Out of scope

- No UI cost-panel changes.
- `providers/fake`'s `UsageOverride` (used by existing engine usage tests)
  doesn't need cache-token fields — none of the acceptance criteria touch
  cache accounting through the fake provider, only through the real
  Claude Code wire path.
- `services/session`'s runner doesn't call `recordUsage`-equivalent code
  today (per `UsageRecorded`'s doc comment, that wiring is still future
  work) — nothing to change there.

