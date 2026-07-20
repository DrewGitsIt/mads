# HANDOFF: Chat transcript: fold tool results into their tool cards

Task: tsk_190b5bf9050db23e

## What was done

**Root cause.** `ChatRenderer.tsx` rendered any `TranscriptMessage` with a
`.tool` as its own `ToolCard`, and bare `tool_result` messages have a `Tool`
(carrying only the correlating ID, no name/input) — hence the reported
"no input" card after every real tool card. Two latent backend bugs fed the
same symptom on a *live* session's transcript specifically: `runner/
transcript.go` was dropping `Tool.ID` and `Timestamp` when proxying a live
provider's messages, and `providers/claudecode` never propagated the wire's
`is_error` onto `tool_result` messages.

**Fix, frontend (the actual folding).** New `ui/src/router/toolPairing.ts`
exports `foldToolResults(messages)`: walks the transcript, and when it hits
a `tool_result` looks up its owning `tool` by ID and merges the result into
`tool.result` (`{ text, is_error, timestamp }`), dropping the standalone
`tool_result` entry. An orphaned `tool_result` (no matching `tool_use`, or
no ID at all) is dropped rather than rendered, since it has nothing of its
own to show. Unresolved `tool` messages pass through as the *exact same
object reference* they came in as — this matters because the drill-in
feed's liveness check compares turns by `===`.

`ChatRenderer.tsx` now folds `messages` before mapping and passes
`timestamp` into `ToolCard`. `ToolCard.tsx` gained a `timestamp` prop, a
collapsed-row status chip (ok/error, via the existing `Chip` `tone="ok"`/
`"bad"`) plus a duration hint when both timestamps are present, and an
expanded "result" section (capped at 2000 chars with a "Show more"/"Show
less" toggle) styled with the `--bad`/`--bad-soft` tokens when `is_error`.

**Converged pairing.** `feedFormat.ts`'s `pendingTool` (used by the pipeline
drill-in's `AgentFeedView`) previously did its own positional backward scan.
It's rewritten to call `foldToolResults` and then find the trailing
unresolved `tool` in the folded array — same observable behavior on every
existing `AgentFeedView.test.tsx` case (verified by hand and by the still-
green test suite), now ID-based instead of position-based.
`AgentFeedView.tsx`'s own rendering (separate `.tool`/`.toolResult` rows)
was **not** touched, since it's a different, still-desired presentation for
that surface.

**Backend, additive only.** `provider.Message` and `contracts.
TranscriptMessage` both gained `IsError bool `json:"is_error,omitempty"``.
`providers/claudecode/transcript.go` now sets it from the wire's
`tool_result` block. `runner/transcript.go` now copies `Tool.ID`,
`Timestamp`, and `IsError` (previously dropped ID/Timestamp — a real bug,
independent of this task, that specifically affected *live* sessions).
`normalizeTurns` (shared by the Router's `/transcript` and the pipeline's
`/turns` endpoints — confirmed by reading both call sites) now also copies
`IsError`. I did **not** move any pairing/folding logic into Go: `
normalizeTurns` is shared by `ChatRenderer`'s transcript endpoint and the
pipeline drill-in's turns endpoint, so folding there would have silently
changed `AgentFeedView`'s data too.

**New regression fixture.** `providers/claudecode/testdata/
transcript_denial.jsonl` + golden: a denied image read (`is_error: true`
tool_result) followed by a successful bash call, asserting both
`Tool.ID` and `IsError` propagate correctly end-to-end through
`normalizeTranscript`. New test `TestTranscriptNormalizationDenialGolden`.

## Addendum: fixed a downstream `make test` flake

A prior CI run of `make build && make test` failed on
`TestSwitchTransportPreservesRef` (`services/session/internal/runner`) with
`runner: cannot switch transport mid-turn`. This test file has zero diff
between this branch and its parent commit, and `runner/transcript.go`'s
change here is a pure additive field copy — so the failure isn't caused by
this task's diff. Root cause: in PTY mode, `providers/fake`'s `finish()`
records the turn's reply into transcript history and *then* emits
`EventTurnEnd` on the session's event channel; a separate goroutine in
`pumpPTY` consumes that event, calls `onTurnEnd()` (which does a blocking
`data.Append` before clearing `busy`, deliberately — see its doc, needed for
digest-buffer correctness). The test's `waitForTranscriptLen` polls for the
new transcript entry as a proxy for "turn finished," but that entry becomes
visible strictly before `busy` clears, and the gap between the two is only
reliably small under low system load — under full-suite parallel load (`go
test -race ./...` across every package) it's wide enough that the test's
immediate follow-up `SwitchTransport` call sometimes lands while `busy` is
still true. Confirmed by running the full suite repeatedly: this branch and
its parent both flake, non-deterministically, at a similar rate.

Fixed by making the test tolerate the transient `ErrSwitchBusy` instead of
reordering any production code: `switchTransportWhenIdle` (new helper in
`runner_test.go`) retries `SwitchTransport` with a short backoff until it
either succeeds or a 5s deadline elapses, and only the PTY→ACP switch (the
one following the racy path) uses it — the earlier ACP→PTY switch has no
such race, since `pump` (the non-PTY path) calls `onTurnEnd()` synchronously
before broadcasting `EventTurnEnd` to the hub, so `drainText` returning
already implies `busy` is clear. Ran `go test -race -count=1 ./...` five
times after the fix with no failures (previously ~1-in-5).

**Reviewer should scrutinize:** this is a test-only fix for a pre-existing,
unrelated race; production `onTurnEnd`/`pumpPTY` ordering was intentionally
left untouched. If this flake resurfaces with a different error or in a
different test, treat it as a separate, real bug rather than re-applying
this pattern reflexively.

## What wasn't done

- Did not add a Router-level (`services/router/internal/api`) HTTP test
  exercising the new `IsError` field over the wire — the existing
  `transcript_test.go` suite doesn't construct tool/tool_result fixtures at
  all, and `normalizeTurns`'s unit-level correctness is already covered by
  the claude-code golden fixture plus the field being a straight pass-
  through. Flagging in case the reviewer wants an explicit contract-level
  assertion.
- Did not touch `services/session/internal/synth/transcript.go` (the
  synthesis/retro consumer) — it uses its own simplified shape, not
  `contracts.TranscriptMessage`, so it's genuinely unaffected, not just
  untested.
- Did not add a duration hint for a still-pending tool call in `ToolCard`
  (no result yet ⇒ no status chip at all) — the task's acceptance criteria
  only asked for a hint once resolved; a live "pending" treatment already
  exists separately via `WorkingIndicator`.

## What the reviewer should scrutinize

- **`foldToolResults`'s reference-identity contract.** It intentionally
  returns the *same* object for an unresolved `tool` message and only
  allocates a new object for the index once a matching `tool_result`
  arrives, specifically so `AgentFeedView`'s `turn === pending` check
  (compared against the *unfolded* `turns` prop) keeps working. If this
  invariant is ever broken by a future edit to `toolPairing.ts`, the
  drill-in's liveness indicator will silently stop lighting up — it's not
  covered by a doubled test.
- **Orphan-drop policy.** A `tool_result` with no matching `tool_use` (by
  ID) or with no ID at all is dropped silently. This was a judgment call
  reading the acceptance criteria ("zero 'no input' cards... under any
  circumstance") — if there's ever a wire scenario producing legitimately
  orphaned results that should be visible, this will hide them.
- **`is_error` naming on the TS side.** Went with `is_error` (not
  `isError`) to match the repo's existing convention in `router/types.ts`
  of mirroring Go JSON keys verbatim (see `Attribution.link_id`,
  `Usage.input_tokens`). Worth a second look since it's the one spot in
  this diff introducing a new field name, not just wiring an existing one.
- **`services/pipeline/engine`'s `TestDefaultConcurrencyBatchLandsSerialized`**
  failed once during `go test ./...` but passed 5/5 in isolation both with
  and without this change — pre-existing flake, unrelated to this task
  (that package isn't touched by this diff).

## Verification

- `go build ./...`, `go vet ./...`, `go test ./...` all pass (the one
  flake above aside).
- `npx vitest run`: 200 test files / 1055 tests pass, including new
  `toolPairing.test.ts`, extended `ChatRenderer.test.tsx` and
  `ToolCard.test.tsx`.
- `npx tsc --noEmit`: clean.
