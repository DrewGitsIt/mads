# Drill-in activity feed, ground up — fix-stage notes

Follow-up to `f537e8e`, addressing the reviewer's single fail point:
acceptance criterion 3 ("Feed shows ... e2e journey asserts it") had no
Playwright test reaching `AgentFeedView`, because the `fake` provider never
announced a session ref.

## What changed

- **`providers/fake`**: `Scenario` gets a new opt-in field,
  `AnnounceOneShotRef` (`announce_one_shot_ref` in scenario YAML). When set,
  `Fake.OneShot` mints a ref, announces `provider.EventSessionRef` via
  `OnEvent` right after recording the "user" turn (before any tools run),
  records a "tool" turn per scripted tool *before* the chunk-delay sleep
  (so a caller polling `Transcript(ref)` mid-invocation sees a pending
  tool_use with no matching tool_result — the liveness signal), then
  records "tool_result"/"agent" once all chunks finish. `OneShotResult.Ref`
  carries the ref. The zero value stays exactly the prior behavior: no ref,
  no transcript, `TestNoStageAttemptRefWithoutAnnouncement` (engine) and
  the new `TestOneShotNoRefWithoutAnnounceOptIn` (fake) both still pass.
- **`providers/fake` — the real gap**: giving `OneShot` a ref wasn't enough.
  `Fake.histories` is in-memory per-instance, but in the real (and e2e)
  topology, `mads run` (which calls `OneShot`) and the Router (which serves
  the drill-in feed by calling `Transcript(ref)`, `pipeline_turns.go`) are
  **separate processes**, each with their own `fake.New()`/
  `fake.NewWithScenario()` call — see `services/router/main.go`'s own
  registration vs. `cmd/mads/run.go`'s. A ref announced by one instance was
  structurally invisible to the other; the first e2e attempts failed with
  the pipeline completing normally but the drill-in staying on the raw-log
  fallback the whole time (turns always empty). `providers/claudecode`
  doesn't have this problem because its transcript is a real file on disk
  (`providers/claudecode/transcript.go`), inherently readable by any
  process on the same host under `$MADS_HOME`. Fixed the same way: `Fake`
  gets a new functional option, `fake.WithTranscriptDir(dir)` — when set,
  every `record()` write-through-persists the ref's full history to
  `<dir>/<ref>.json` (atomic write-to-temp-then-rename, so a concurrent
  cross-process reader never sees a torn write), and `Transcript()` falls
  back to reading that file when the ref isn't in this instance's own
  memory. `New()`/`NewWithScenario()` took variadic `...Option` so every
  existing zero-arg caller (~30 call sites, mostly tests) is unaffected.
  `cmd/mads/run.go` and `services/router/main.go` both now construct their
  fake provider with `fake.WithTranscriptDir(filepath.Join(madsHome,
  "fake-transcripts"))`, resolving `$MADS_HOME` the same way
  `pipeline_output.go`'s tee'd-log fallback already documents doing ("true
  for local dev and the e2e harness, not guaranteed across [more exotic
  deployments]"). New test
  `TestOneShotTranscriptDirBridgesSeparateInstances` constructs two
  independent `*fake.Fake` instances sharing only a `t.TempDir()` and
  proves the second can read the first's announced ref — the direct
  regression test for the bug the first e2e attempts hit.
- **e2e**: new `ui/e2e/lib/seed.ts` helper `pollStageStarted(routerURL,
  sessionId, stage, timeoutMs)` polls for a `stage.started` event naming a
  specific stage (existing `pollEventOfType` only filters by event type,
  not payload). New describe block in `pipeline-drillin.spec.ts`,
  "pipeline drill-in: normalized turns feed": launches a real `mads run`
  subprocess with a scenario that sets `announce_one_shot_ref: true` and
  scripts a Bash tool + a multi-second chunked delay matched only against
  the executor stage's prompt (so architect/code_review fall through to
  the fake's instant built-in reply and don't eat the observation window),
  synchronizes on the executor stage starting via `pollStageStarted`, then
  asserts: `agent-feed` (not the raw-log fallback) becomes visible, a
  `feed-tool-row` shows "Bash", "make test", and a timestamp, `feed-
  liveness` is visible and its reading changes over time (ticking with no
  new transcript entries — the pending-tool_use signal), and a `feed-
  prose-row` eventually appears once the stage's reply lands.

## What I did not do

- Did not change `providers/claudecode` or the engine/router code from
  `f537e8e` — that layer was already correct and already covered by its
  own tests; this stage is scoped to the fake provider's cross-process gap
  and the missing e2e assertion.
- Did not add a `WithTranscriptDir`-equivalent for `Start`/interactive
  sessions. Interactive fake sessions have a different, already-working
  cross-process story (the Router proxies live traffic directly to the
  spawned session child rather than reading through its own provider
  registry — see `transcript.go`'s `transcriptFor` doc comment). Only
  `OneShot`'s ref-backed history needed the file-backed bridge; `Start`'s
  `f.histories` entries stay in-memory-only, unchanged.
- Did not persist/clean up `$MADS_HOME/fake-transcripts` — files there are
  small (JSON per ref) and accumulate for the lifetime of `$MADS_HOME`,
  same as `$MADS_HOME/runs`'s tee'd logs already do; no existing cleanup
  path for those either, so this doesn't regress anything that was cleaned
  up before.

## What to scrutinize

- **`fake-transcripts` directory naming/location**: chosen to sit next to
  `$MADS_HOME/runs` and `credentials.json` at the top level of
  `$MADS_HOME`, matching the existing convention for state scoped to "this
  install," not "this session." If a future consumer wants transcripts
  scoped per-session or garbage-collected, this is the place to revisit.
- **Best-effort disk writes**: `writeTranscriptFile` swallows `MkdirAll`/
  `WriteFile`/`Rename` errors rather than surfacing them through `OneShot`
  — deliberate (a transcript-persistence hiccup shouldn't fail a pipeline
  stage that otherwise succeeded), but means a broken `$MADS_HOME` (e.g.
  read-only disk) degrades silently back to the raw-log fallback rather
  than erroring loudly. Matches how `openOutputLog`'s failures are already
  handled (best-effort, `MadsHome == ""` degrades quietly) — same
  philosophy, not a new pattern.
- **Scenario match scoping in the new e2e test**: the scripted rule matches
  `"Pipeline stage: executor"` specifically (not the task title generically)
  so only one stage in the six-stage default template incurs the multi-
  second scripted delay. If the default template's stage names or
  `taskPrompt`'s format (`services/pipeline/engine/engine.go`) ever change,
  this match string needs to move with it — there's no compile-time link
  between the two.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` — all packages pass, including new
  `TestOneShotTranscriptDirBridgesSeparateInstances`,
  `TestOneShotAnnouncesRefAndBacksTranscript`, and
  `TestOneShotNoRefWithoutAnnounceOptIn` in `providers/fake`.
- `npx tsc --noEmit -p .` — clean.
- `npx vitest run` — 199 files / 1031 tests pass.
- `npx playwright test` (full suite, 27 tests, fresh `VITE_ROUTER_URL=""
  npm run build`) — all pass, including the new "normalized turns feed"
  case. Re-ran `pipeline-drillin.spec.ts` alone three times back-to-back
  (2.2–2.4s for the new test) to check for flakiness in the stage-start
  synchronization; stable each time.

Task: tsk_f67f34a78c199aaf
