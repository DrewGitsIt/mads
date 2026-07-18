# Executor handoff: Drill-in output: structured log, taller readable pane

Task: `tsk_b474cb07834c070a`. Both bugs from the architect's brief (below,
now superseded) are fixed. All acceptance criteria are met except one
pre-existing, out-of-scope e2e failure — see "Not done" below.

## What was done

**Part A/B — structured log (Go)**

- `providers/provider/provider.go`: `OneShotRequest.OnChunk func(string)`
  renamed to `OnEvent func(Event)`, matching the interactive `Session`
  path's event model.
- `providers/claudecode/claudecode.go`'s `oneShotStream` now calls
  `wireLine.events()` (already used by the interactive path) instead of
  hand-rolling a text-only subset of the stream-json switch — this is what
  makes tool-use blocks visible to `OnEvent` instead of being silently
  dropped.
- Updated all `OneShotRequest` callers/fakes across `providers/*` and
  `providers/conformance` to the new `OnEvent` shape.
- `services/pipeline/engine/outputlog.go` rewritten: `openOutputLog` writes
  a `=== stage=... attempt=... task=... model=... started=... ===` header
  immediately on open; `emit`/`write`/`tool` insert a newline boundary
  between events instead of concatenating raw chunks; `tool()` writes
  `⏺ Name path` marker lines; `close()` writes a
  `=== stage=... outcome=... duration=... ===` footer.
- `services/pipeline/engine/engine.go` wired to call `log.tool(...)` on
  `EventToolUse` and `log.write(...)` on `EventText`, preserving existing
  close-ordering (footer always written, even on a zero-event attempt via
  the `fallback` text).
- New test `TestOneShotStreamsToolUse` (provider/engine level) plus updated
  `outputlog_test.go` covering header/footer/marker/boundary formatting.
- Router SSE follow endpoint (`services/router/internal/api/pipeline_output.go`)
  needed no changes — it already tails the log file byte-for-byte; the new
  structure is transparent to it.

**Part D — taller, reactive drill-in pane (UI)**

- `ResizablePanel.module.css` / `TaskDrillIn.module.css` /
  `PipelineTab.module.css`: fixed a `flex: none`/min-height chain that
  prevented the panel from ever growing past ~250px regardless of its
  `maxSize` prop.
- `PipelineTab.tsx`: added a `ResizeObserver` on the tab's own root
  container, threading a live `tabHeight` down to `TaskDrillIn`.
- `TaskDrillIn.tsx`: `maxHeight` is now `max(MIN_HEIGHT, min(viewportCap *
  0.85, tabHeight - BOARD_SLIVER))`, recomputed every render (not frozen at
  mount) so it tracks real layout changes.
- Terminal width capped at ~120ch (`AgentOutputView`/`TerminalView`
  styling) so a very wide pane doesn't stretch lines unreadably.

## Bug found and fixed during verification (not in the original brief)

`TaskDrillIn.tsx`'s `maxHeight` used `window.innerHeight * 0.85` for the
percentage-cap term instead of the measured `tabHeight * 0.85`. Since
`window.innerHeight` includes app chrome above the tabpanel (rail, tab
bar), it's always >= the tab's own height, so the 85% term was too loose
whenever it — not the `tabHeight - BOARD_SLIVER` term — should have been
the binding constraint. This passed the two pre-existing unit tests only
by coincidence: in both, the `BOARD_SLIVER` term happened to be the
tighter bound regardless of which height fed the 85% term, so neither
test could distinguish the two formulas.

Caught by the architect's own new e2e test ("drill-in pane drags to at
least 85% of the tab height"), which measures against
`#workspace-panel-pipeline`'s real bounding box — the first assertion in
this whole task that exercises a viewport where the two formulas diverge.

Fix: `viewportCap = Number.isFinite(tabHeight) ? tabHeight : window.innerHeight`,
then `maxHeight = max(MIN_HEIGHT, min(viewportCap * 0.85, tabHeight - BOARD_SLIVER))`.
Falls back to `window.innerHeight` only when `tabHeight` isn't measured
(the `Infinity` default), preserving both existing unit tests' fallback
path. Added a third `TaskDrillIn.test.tsx` case
("clamps drag height to 85% of tabHeight, not 85% of the full window...")
that specifically discriminates the two formulas (tabHeight=700,
window.innerHeight=2000 — old code would wrongly return 604, new code
correctly returns 595).

Verified: unit test passes, and the e2e "85% of the tab height" test
passes when run in isolation (`--grep`, see "Not done" for why isolation
was necessary).

## Not done: pre-existing SSE connection-starvation failure (out of scope)

`pipeline-drillin.spec.ts`'s first test ("drill-in shows live streaming
agent output for a running task") fails in `make ui-smoke` with a blank
terminal — `toContainText("stage=executor")` times out. This cascades
(via `test.describe.configure({ mode: "serial" })`) to skip the next two
tests in the same block, including the new "85% of the tab height" test.

Root cause: browser per-origin SSE connection budget exhaustion, not a
backend or app bug. Viewing a session already opens ~5-6 long-lived
EventSource connections (global `session.*`/`task.*`/`link.*`
subscriptions, a broad session-scoped subscription, and the Conversation
tab's own output stream). `usePipelineRun.ts` deliberately opens a 6th,
non-pooled connection when the Pipeline tab mounts mid-run (an
intentional, explicitly-commented trade-off from task `tsk_c6d8d6c147a7cddc`
— "don't fix this into always omitting `since`"). That leaves no headroom
for this task's own new drill-in output stream (a 7th), which gets stuck
in an open→error retry loop and never delivers data.

Verified pre-existing and unrelated to this task's changes by swapping in
the exact unmodified `HEAD` version of `pipeline-drillin.spec.ts` and
observing an identical failure. Also verified test 2 ("resize persists
across reload") fails identically on unmodified `HEAD` when run in
isolation (`page.reload()` times out with `net::ERR_ABORTED` — same
connection-budget symptom, different assertion). This exact failure mode
on this exact test was already documented and accepted as
"pre-existing, environment-specific, out of scope" by the immediately
prior pipeline stage (commit `fadeb92a5389`, task `tsk_c6d8d6c147a7cddc`),
using the same git-stash isolation technique, and merged anyway.

**Reviewer should scrutinize:** whether this precedent is still the right
call, or whether it's time to budget/pool `usePipelineRun.ts`'s dedicated
connection (or the drill-in's own stream) rather than deferring it a
second time. Not attempted here because `usePipelineRun.ts` carries an
explicit do-not-fix comment protecting the exact behavior that causes
this, and reworking it is out of scope for a "structured log, taller
pane" task.

## Verification

- `make check`: full pass (195 test files, 972 tests).
- `make ui-smoke`: 17/20 pass. 1 fails (pre-existing, see above), 2 skip
  as a consequence of serial-mode cascade — both independently verified
  passing/pre-existing-failing in isolation via `--grep`.
- New/updated tests: `TestOneShotStreamsToolUse`, `outputlog_test.go`,
  `TaskDrillIn.test.tsx` (new discriminating case), `pipeline-drillin.spec.ts`
  (new "85% of tab height" test, plus tool-use assertions on the existing
  live-output test).
