# HANDOFF: Live chat turns — render structure as it streams, survive stream drops

Task: tsk_a29002f6e155f216

## What I did

Root cause (per the task brief) was twofold in `ui/src/conversation/ConversationTab.tsx`:
`messages` (the persisted transcript) only ever got refreshed at `turn_end`, and the
live-turn state machine concatenated every text delta into one string bubble with no
block boundaries. Fix, on the same transcript-sourced pattern the pipeline drill-in's
`DrillInFeed`/`streamTaskTurns` already uses (provider transcript is written
continuously during a turn — confirmed server-side in
`services/router/internal/api/transcript.go`, which proxies live to the running
session child):

1. **Mid-turn polling** — new effect polls `routerClient.getTranscript(sessionId)`
   every `TURN_POLL_INTERVAL_MS` (1.5s) whenever `turn.phase !== "idle"`. Each refresh
   replaces `messages` (so completed paragraphs/tool cards render via the existing
   `ChatRenderer` — no changes needed there, it already maps one array entry to one
   block) and resets the streamed bubble's text to `""` (task's "shrinks to just the
   currently-open text block"). Effect cleans up its interval on turn-end/unmount; it
   never starts while idle.
2. **EventSource reconnect** — new effect listens for the native `"open"` event on the
   session's data-plane stream (fired when the browser auto-reconnects after a
   mid-stream drop) and does one immediate transcript+session refetch, gated on the
   turn currently being non-idle (via a `turnPhaseRef` mirror, read from the event
   callback).
3. **visibilitychange** — new effect + a sticky `turnWasLiveRef` (set true whenever
   `turn.phase` leaves idle; cleared only once reconciled while visible) triggers the
   same immediate refetch on `visibilitychange` → `"visible"`, covering the reported
   bug (turn resolves while backgrounded, nothing updates until refocus) even when the
   turn has already gone idle by the time the tab is looked at again.
4. Added `FakeSessionEventSource.emitOpen()` in `ui/src/router/fixtures.ts` (test-only)
   to simulate the native reconnect event — `StreamEvent`'s `kind` union has no "open"
   case, so this is a separate raw-DOM-event helper alongside the existing `emit()`.
5. Four new tests under a `describe("live-turn transcript polling", ...)` block in
   `ConversationTab.test.tsx`: structure-before-turn_end (tool card + multiple prose
   blocks), stream-killed-mid-turn convergence + reconnect fast path, no-polling-while-
   idle, and visibilitychange refetch.

No changes to `ChatRenderer`, terminal mode, or `SwitchTransport` — all untouched per
the brief.

## What I did not do

- Did not change the `refetchAfterTurn` bounded-retry path at `turn_end` — it's
  untouched and still runs; the new poll effect is purely additive.
- Did not try to precisely reconcile the streamed bubble's text against the polled
  transcript's exact trailing block (e.g. diffing to preserve any in-flight suffix not
  yet persisted at fetch time). I reset it to `""` on every poll instead — simpler, and
  any tiny loss at a fetch boundary self-heals on the next poll or at `turn_end`. Flagged
  below for scrutiny.
- Did not gate the `"open"`-listener refetch by anything other than turn phase — it
  fires on every native reconnect while a turn is in flight, not just ones following a
  detected drop. Cheap and harmless, but worth a look if reconnect turns out to be
  noisier in practice than assumed.
- Did not add a fixture/manual QA pass in a real browser (no live provider session
  available in this environment) — verified via the unit suite only (1071 tests pass,
  `tsc --noEmit` and `eslint` clean).

## Scrutinize

- The `turnWasLiveRef` state machine (ConversationTab.tsx, just above the
  `visibilitychange` effect) is the fiddliest part — sticky-true on any non-idle turn,
  cleared only when the turn reaches idle *while visible*. Worth tracing through the
  "hidden the whole time, turn starts and finishes while hidden" case once more against
  real browser timer-throttling behavior, since jsdom doesn't actually throttle
  anything and the test for it can't exercise that path faithfully.
- `TURN_POLL_INTERVAL_MS = 1500` is a guess at "modest cadence" — not tuned against any
  real provider transcript-write latency.

## Post-review fixup

First review failed with: "diff deletes an unrelated, already-merged feature (GET
/transport mount-time mode read...), reintroducing the exact 'resets to Chat on mount'
bug it fixed." Root cause wasn't a deletion in this branch's own commit — `git diff
main...HEAD` on `f71029f` alone was purely additive (246 insertions, 0 deletions). The
branch was cut from `743d5c4`, and trunk moved on with
`c35c830`/`6e9b8cd`/`8afe1f5` ("Display mode: reflect the session's real transport,
don't reset to Chat on every mount") *after* that point. Comparing this branch against
current trunk therefore showed that feature as absent — which a straight merge would
have manifested as a deletion.

Fix: merged trunk into this branch (`152b05d`, "Merge trunk into
tsk_a29002f6e155f216 (pipeline rebase-before-merge)"), matching the same
rebase-before-merge convention visible elsewhere in this repo's history (e.g. `01f5009`,
`aecdb13`). The merge was clean — no conflicts in `ConversationTab.tsx` despite both
branches touching it, since the mount-time transport-mode read and the mid-turn
polling/reconnect/visibility effects sit in disjoint parts of the file. Verified after
merging: full suite (1074 tests, 200 files) passes, `tsc --noEmit` clean, `eslint`
clean, `go build ./...` clean. No source changes beyond the merge itself.
