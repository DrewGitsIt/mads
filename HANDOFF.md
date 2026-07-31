# HANDOFF: Unified capability inference — one engine, two call sites

Task: tsk_c3832608a8bce860

## What this task builds

Owner decision (2026-07-30): mads infers "which capabilities does this need"
in two places — the task editor and interactive session-start's `agent:auto`
— and these must share one inference mechanism, not two divergent
heuristics. Users never type capability names; inference suggests, the user
confirms/edits/clears.

## What was done

**`pkg/capinfer`** — the single inference engine. `Inferrer.Infer(ctx, text,
repos)` unions two signal sources, always grounded to the live registry
(hallucinated names dropped, same rule `pkg/agentgen` already enforces):
- `RepoSignals` — cheap, deterministic, no LLM: each repo's git remote host
  (`github.com`/`gitlab.com`) implies a same-named capability, when that
  capability actually exists in the registry. Runs even with no Fast Model
  configured.
- `inferFromModel` — a grounded Fast Model call; any name outside the
  registry catalog is silently dropped, never surfaced. No FastModel
  configured, or a configured one producing no usable output, degrades to
  "no model-derived suggestions" — never a hard failure (build requirement
  4). Only live infra errors (Data Service unreachable) propagate.

**`pkg/agentselect.BuildRequest`** — the single population path from free
text + a resolved capability-name list to an `agentselect.Request`
(`ExpandCapabilities` resolves names into tools/MCP servers/skills). Both
call sites go through this rather than constructing `Request` by hand.

**`pkg/autostaff.SelectOrGenerate`** — the single select-then-generate-
fallback staffing function (`agentselect.Select`, falling back to
`agentgen.GenerateFromTask` + registration when no candidate survives). Lives
in its own package because `agentgen` already imports `agentselect`, so a
function needing both can't live in either without a cycle.

**Call site A — task editor** (`POST /tasks/suggest-capabilities`,
`handleSuggestTaskCapabilities` in `tasks.go`): synchronous, bypasses
`job.Runner` (same shape as the existing agent-suggestion handler), calls
`capinfer.Infer` over title+description+workspace repos, returns suggested
names only — never writes `Task.RequiredCapabilities` itself.
`TaskDetailSheet` renders the result as `Chip tone="draft"` chips below the
existing `CapabilityPickerField`: clicking a chip **confirms** it into
`draft.required_capabilities` (via the same local-state path every other
field uses — persisted only by the pre-existing full-replace Save), a
confirmed suggestion becomes an ordinary chip **editable/removable** through
the pre-existing picker (no new UI needed), and **Clear** discards the
remaining suggestion list without touching `required_capabilities`.
Suggestions already covered by workspace inheritance or an already-confirmed
task capability are filtered out before rendering.

**Call site B — session-start `agent="auto"`** (`resolveAutoAgent` in
`sessionautoagent.go`, called from `handleCreateSession` in `sessions.go`
when `sess.Agent == "auto"`): infers from the composer's opening prompt (sent
as a new `opening_prompt` field on `POST /sessions`, never persisted on the
session record itself), then routes through the identical
`agentselect.BuildRequest` + `autostaff.SelectOrGenerate` machinery the
pipeline uses. `sess.Agent` is mutated from `"auto"` to the concrete,
now-registered agent name before session creation completes — `"auto"` is
never persisted. `services/pipeline/engine/engine.go`'s `resolveStageAgent`
was refactored to call the same `BuildRequest`/`SelectOrGenerate` pair
(previously had its own inline select-then-generate logic) — this is the Go
proof that both call sites share machinery, not copy-pasted logic.

**Launchpad UI** (`AgentPill.tsx`, `Launchpad.tsx`): the existing editable
agent pill now surfaces the concrete agent that "Auto" resolved to, once
session creation returns, so the user can see and correct what was inferred
— reusing the pre-existing pill/editor rather than building new UI for call
site B.

## What was NOT done / scope decisions

- `resolveStageAgent`'s `Request` is built from `task.RequiredCapabilities`
  only, not a workspace-union `EffectiveCapabilities` — this task's brief
  scoped to unifying the *inference* mechanism, not changing what capability
  set pipeline stages already resolve against; `EffectiveCapabilities` union
  behavior (from the prior capability-requirements task) is untouched.
- No per-chip dismiss control for capability suggestions — bulk "Clear" only.
  A per-chip dismiss would need a nested-button (`Chip`'s `onClick` variant
  already renders as a `<button>`); "confirm what you want, then Clear the
  rest" satisfies the confirm/edit/clear acceptance criterion without that
  complexity.
- The Launchpad's `TrustPill` shows default/empty permissions while "Auto" is
  still selected, since the real agent isn't known until after session
  creation resolves it — this is pre-existing behavior for any agent whose
  concrete identity isn't yet known, not new to this task.
- No new UI for call site B's "surface what was inferred" beyond making the
  existing editable agent pill show the resolved name — no separate
  "here's why we picked this" explanation surface was built.

## Verification

- `go test ./...` (via `make check`): all packages pass, including
  `pkg/capinfer`, `pkg/agentselect`, `pkg/autostaff`,
  `services/router/internal/api`, `services/pipeline/engine`.
  - `TestSuggestTaskCapabilitiesDropsHallucinatedNames` (`tasks_test.go`):
    proves call site A's output ⊆ registry with a hallucinated name dropped.
  - `TestCreateSessionAgentAutoResolvesToRegisteredAgent` /
    `TestCreateSessionAgentAutoGeneratesWhenNoCandidate` (`sessions_test.go`):
    prove call site B routes through select-then-generate-fallback.
  - `pkg/capinfer/capinfer_test.go`: registry-grounding (hallucination
    dropped) and `RepoSignals` deterministic-augmentation coverage.
- `npx vitest run` (ui/): 222 files / 1348 tests passing, including 5 new
  `TaskDetailSheet` tests covering draft-chip render (excluding
  already-applied names), confirm-without-persisting, edit-after-confirm-
  then-Save-persists, Clear-dismisses-without-mutating, and the
  no-suggestions message.
- `npx tsc --noEmit -p .` (ui/): clean.
- `make check` (repo root — golangci-lint, `go test ./...`, ui lint, ui
  test): exits 0.

## Reviewer should scrutinize

- `resolveAutoAgent`'s new `opening_prompt` request field on
  `POST /sessions` — confirm the "never persisted on the session record"
  claim holds (it's read once, used to build the inference `Request`, then
  discarded) and that this doesn't leak free-text session content anywhere
  it shouldn't.
- The `engine.go` refactor of `resolveStageAgent` — it's a behavior-
  preserving refactor onto shared machinery, but worth confirming the error
  messages/event-logging shape (`agent.selected` events) are unchanged for
  existing pipeline consumers depending on their exact wording.
- Whether reusing `Chip tone="draft"` directly (rather than the existing
  `Draft`/`DraftChip` wrapper component used by `NoteGroup`) is the right
  call — the wrapper is a container/label pattern; this needed individually-
  actionable chips, so `Chip tone="draft"` was used directly instead.
