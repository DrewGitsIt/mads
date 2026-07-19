# Drill-in activity feed, ground up — executor notes

## What changed

- **Provider (claude-code)**: `oneShotStream` now announces `provider.EventSessionRef`
  as soon as `session_id` is known on the stream-json wire (every wire line carries
  it), and `parseOneShot` sets `OneShotResult.Ref`. `normalizeTranscript` now handles
  `tool_result` blocks and attaches per-line RFC3339Nano timestamps to each turn.
- **Engine**: records the provider-native session ref per stage attempt via a new
  `recordRef` path wired into `runAgent`'s `OnEvent` switch, posting a
  `contracts.StageAttemptRef` event (`{stage, attempt, ref}`) — exact attribution,
  no path guessing. Covered by `services/pipeline/engine/ref_test.go`
  (`TestStageAttemptRefRecordedNamesStageAndAttempt`,
  `TestNoStageAttemptRefWithoutAnnouncement`).
- **Contracts**: `TranscriptMessage`/`ToolUse` extended with `ID`/`Timestamp`.
- **Router**: new `GET /sessions/{id}/pipeline/tasks/{taskId}/turns` SSE endpoint
  (`pipeline_turns.go`) resolves the recorded ref for task+stage+attempt, reads the
  provider's own transcript via `TranscriptReader`, and serves **normalized** turns
  (contracts schema) — provider wire format never reaches the UI. Tailable while a
  stage is running. `normalizeTurns` is shared with the existing `/output`-adjacent
  transcript path. `pipeline_turns_test.go` covers a real claude-code transcript
  fixture (tool_use, tool_result, timestamps) plus the no-ref/no-transcript cases.
- **UI**: new `DrillInFeed` wrapper picks between the rich feed and the pre-existing
  raw `AgentOutputView`, based purely on whether any normalized turns have arrived
  (`turns.length > 0`). `AgentFeedView` renders the enhanced feed: header metadata
  (task, stage, attempt, started), prose rows visually distinct from tool rows,
  Bash commands / file paths as the tool row's salient argument, visible per-row
  timestamps, a liveness row (ticking elapsed time) for a trailing tool_use with no
  matching tool_result yet, middle-truncated long values with click-to-expand, and
  auto-follow (scroll-lock-on-scrollback). New `feedFormat.ts` holds the pure
  formatting/derivation helpers. Wired through `TaskDrillIn.tsx` (new required
  `turnsSource` prop) and `PipelineTab.tsx` (new `turnsSource` useMemo/cleanup,
  mirroring the existing `outputSource` lifecycle).

## What I did not do

- Did not modify `providers/fake` to announce a session ref from `OneShot`. The
  `fake` provider is the model of "a provider with no announcement mechanism"
  (see `TestNoStageAttemptRefWithoutAnnouncement`), and the Playwright e2e suite
  exclusively drives `fake` via real `mads run` subprocesses — so e2e structurally
  exercises only the fallback-to-raw-output path, never the enhanced turns feed.
  Coverage for the enhanced feed's UX (prose/tool distinction, timestamps,
  liveness ticking, truncation) is Vitest component tests
  (`AgentFeedView.test.tsx`, `DrillInFeed.test.tsx`) instead.
- Did not add "model" to the feed's header metadata block, even though the design
  lists stage/attempt/task/model/started. No client-side data path currently
  exposes a model for a given task/stage/attempt (`StageAttempt` has no model
  field); plumbing one through would be a larger, out-of-scope backend change.
  Header shows stage/attempt/task/started only.
- Did not touch `TaskDrillIn`'s resize/height-clamp system (`ResizablePanel`,
  `MIN_HEIGHT`, `BOARD_SLIVER`, `maxHeight` formula, `loadDrillInHeight`/
  `saveDrillInHeight`) — verified unchanged behavior via the existing e2e resize
  specs, which still pass.

## What to scrutinize

- **e2e coverage gap for the enhanced feed**: as above, no Playwright test can
  currently reach `AgentFeedView` (only its Vitest tests can), since the `fake`
  provider never announces a ref. If real end-to-end coverage of the turns feed
  matters, it needs either a new provider capable of announcing refs in test
  scenarios, or a scripted transcript fixture path — out of scope for this task.
- **Header metadata omits "model"** — flagged above; confirm this is acceptable
  or decide how "model" should be sourced before expanding scope.
- **Build/env gotcha unrelated to this task's code**: `ui-smoke`/Playwright
  requires `VITE_ROUTER_URL=""` at build time (see Makefile) so the built UI talks
  same-origin to the Router's ephemeral port instead of a hardcoded
  `localhost:8080` baked in by a plain `npm run build`. Not a regression from this
  change — just a discovery made while verifying it — but worth knowing if e2e
  ever mysteriously 404s/502s again after a UI rebuild.
- **Fallback selection heuristic**: `DrillInFeed` switches to the rich feed the
  moment `turns.length > 0`, with no explicit "stream closed empty" signal (the
  `PipelineTurnsSource` interface has no such callback). This is the smallest
  correct signal for today's requirements, but note it if `PipelineTurnsSource`
  ever grows an explicit end-of-stream event.

## Verification

- `go build ./...` and `go test ./...` — all packages pass.
- `npx tsc --noEmit -p .` — clean.
- `npx vitest run` — 199 files / 1031 tests pass.
- `npx playwright test` (full suite, 26 tests, fresh `VITE_ROUTER_URL="" npm run
  build`) — all pass, including both `pipeline-drillin.spec.ts` cases and the
  pre-existing resize/reload-persistence specs.

Task: tsk_f67f34a78c199aaf
