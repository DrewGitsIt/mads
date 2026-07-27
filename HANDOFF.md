# HANDOFF: Wire pkg/agentselect into pipeline agent:auto with automatic generation fallback

## What was done

`agent: auto` stages now route through the real selection machinery instead of the hardcoded `"executor"` stub.

- `services/pipeline/engine/engine.go`
  - `Config` gained a `FastModel *fastmodel.Runner` field. `nil` (the default) means a no-candidate auto stage blocks instead of generating — used by tests and any caller that doesn't want generation.
  - `resolveAgents` (launch-time template validation) now only pre-checks explicitly-named (non-`"auto"`) agents; `"auto"` is resolved per-task at stage-run time instead.
  - New `resolveStageAgent(ctx, stage, task)`: for an explicit agent, returns it exactly as before. For `"auto"`, calls `agentselect.New(e.cfg.Data).Select(ctx, req)` with `Title`/`Description`/`Labels`/`Stage`/`TaskID`/`SessionID` populated and `Required*` left empty (per the task's instruction — no capability scheme invented here). On success, fetches and returns the selected agent. On `ErrNoCandidates`, if `FastModel` is configured, generates a draft via `agentgen.New(e.cfg.Data, e.cfg.FastModel).GenerateFromTask(ctx, req)`, registers it via `CreateAgent`, and uses it. Any other selection error, a nil `FastModel`, or a generation failure returns an error that fails the stage with a clear reason (never falls back to a hardcoded agent).
  - `runAgent` now calls `resolveStageAgent` first thing; a resolution error becomes the stage's failure output (`return err.Error(), err`), which flows through the existing block-reason path into a `contracts.NoteBlocker` note on the task.
  - `agentselect.Select` already logs `agent.selected` on every call (including the no-candidate case), so selection visibility (required change #3, event-level part) came for free — no new event-logging code needed.

- `pkg/pipelinerun/pipelinerun.go`
  - `Options` gained `FastModel *fastmodel.Runner`, threaded straight into `engine.Config`. This is the shared surface between `mads run` (CLI) and the Router-spawned pipeline child.

- `services/pipeline/engine/engine_test.go`
  - Four new tests, using the `contracts.QuadrantInvestigator` quadrant (the seeded system agents all carry either no quadrant or `autonomous_executor`, so a task classified as Investigator has zero candidates among them for free):
    - `TestAutoSelectsRegisteredAgent` — registers an `investigator-bot` agent, confirms the auto stage selects it (not `"executor"`) and logs exactly one `agent.selected` event naming it.
    - `TestAutoGeneratesAgentWhenNoCandidate` — no extra agent registered; scripts a Fast Model reply via a fake provider/backend; confirms generation registers `generated-investigator` and the stage completes.
    - `TestAutoGenerationFailureBlocksStage` — scripts a Fast Model reply missing required `name`/`prompt` fields (fails validation on every retry, since `fastmodel.Runner`'s retry-feedback prompt is appended to the same matched prompt); confirms the stage blocks with a `NoteBlocker` note mentioning generation.
    - `TestExplicitAgentBypassesAutoSelect` — an explicitly-named `agent: executor` stage; confirms zero `agent.selected` events fire.
  - All pre-existing tests in this file are unmodified and still pass (regression coverage that explicit agent names are untouched).

## What was NOT done (deliberately out of scope)

- **`services/pipeline/main.go`** (the Router-spawned pipeline child binary): no `FastModel` is wired into its `pipelinerun.Options` construction. This means, in production today, a live pipeline run still has `FastModel: nil` and any `auto` stage with no surviving candidate will block rather than generate. Wiring a real Fast Model backend into the Router→pipeline-child launch path (config, API keys, etc.) is a separate concern from this task's scope (which was the selector/generator plumbing itself) and wasn't in the acceptance criteria. Flagging this explicitly since it means the generation fallback is currently inert outside of tests/any caller that sets `FastModel` itself.
- **Drill-in UI showing the selected agent name on the stage** (required change #3, second half — "Minimal UI: show the selected agent name on the stage in the drill-in"). The acceptance criteria list only Go tests + `make check`; no UI work was done. The event-level data (`agent.selected`, including elimination reasons) is persisted and queryable, so this is purely a follow-up UI task, not a data-availability gap.
- No capability-based filtering: `RequiredTools`/`RequiredMCPServers`/`RequiredSkills` are left empty on every `agentselect.Request` built by the engine, per the task's explicit instruction not to invent a task-side capability scheme. Only the quadrant filter is operative for auto-selection today.

## What the reviewer should scrutinize

- `resolveStageAgent`'s error-wrapping: a nil `FastModel` and a real generation failure both currently produce fairly similar-looking wrapped errors ("no candidate agent for task %q, and ..."). Worth confirming the phrasing is clear enough for a user reading the blocked task's note in production.
- The choice to fetch the agent via `e.cfg.Data.Agent(ctx, sel.Agent)` after a successful `Select` (rather than having `Select` return the full `contracts.Agent`) — this is an extra round trip per auto stage per task. `agentselect.Selection` only carries the agent's name, not the full record, so this seemed like the natural boundary rather than changing `agentselect`'s return shape.
- Test design leans on the seeded system agents' quadrant assignments (`synthesizer`/`agent-builder` have no quadrant at all; `architect`/`executor`/`code-reviewer`/`verifier` are all `autonomous_executor`) to get a clean zero-candidate case using `contracts.QuadrantInvestigator`. If the seed migration (`0005_registries.up.sql`) or `Classify`'s keyword heuristics ever change, these tests could start failing for a reason unrelated to the code under test — worth a second look if either changes.
- `services/pipeline/main.go` is unchanged (see above) — confirm this deferral is acceptable rather than an oversight.

## Verification

- `go test ./services/pipeline/... ./pkg/agentselect/... ./pkg/agentgen/... ./pkg/pipelinerun/...` — all pass, including the 4 new tests.
- `make check` — passes clean (golangci-lint: 0 issues; `go test -race ./...`: pass; UI: 219 test files / 1287 tests pass).
