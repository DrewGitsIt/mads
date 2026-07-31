# HANDOFF: tsk_7729d10351a7bc19 — Auto-select consumes effective capabilities and ranks by least-extraneous

## What was done

**Build item 1 — Request population (`services/pipeline/engine/engine.go`)**
- `resolveStageAgent` now calls a new `effectiveCapabilities(ctx, task)` helper before building the `agentselect.Request`: fetches the session, its workspace (if bound), and the live capability registry, then runs `agentselect.EffectiveCapabilities` (task+workspace union) → `agentselect.ExpandCapabilities` (registry expansion) to populate `RequiredTools`/`RequiredMCPServers`/`RequiredSkills`.
- Confirmed (via grep) there is no other live caller of `agentselect.Select` with a task in hand — `services/router/internal/api/agents.go`'s `handleGenerateAgent` already threads `Required*` fields from its own request body for generation (not selection), so it needed no change.

**Build item 2 — extraneous-capability ranking tier (`pkg/agentselect`)**
- Added `extraneousCount` (`rank.go`) and wired it into `Select()`'s per-candidate `ScoreBreakdown.Extraneous`.
- Placed the new tier **second** in `rankCandidates`, right after label affinity and *before* track record/recency — deliberately, so ties on label affinity (common — integer token overlap) are decided by leanness before an established generalist's history/recency can pre-empt a leaner, equally-relevant candidate. This was the owner's explicit condition for accepting additive-only capability composition ("must actually decide outcomes, not just serve as an unreachable tiebreak"). Rationale is documented as a doc comment directly above `rankCandidates`.

**Build item 3 — agentgen forced declaration (`pkg/agentgen/agentgen.go`)**
- Added `mergeRequired`/`unionStrings`, called at the end of `GenerateFromTask` (after `generate()`, before `withSimilar()`) to force-union `req.RequiredTools/MCPServers/Skills` into the draft's capabilities, regardless of what the Fast Model's own reply declared.
- These merged names bypass the registry-grounding `knownOnly` drop entirely — they arrive already validated-by-construction via `EffectiveCapabilities`/`ExpandCapabilities`, so per the task brief they are not "inventions."

**Build item 4 — selection transparency**
- `filter.go`: `eliminationReason`/`missingCapabilities` now render `"capability (missing <names>)"` instead of the bare string `"capability"`, so both the logged `agent.selected` event and any downstream reason string name every missing tool/MCP server/skill.
- `engine.go`: added `describeElimination(sel)`, which renders every `"capability"`-eliminated candidate's detail; wired into both `ErrNoCandidates` failure branches (no Fast Model configured; generation itself failed) so the blocked-task note names the missing capability (e.g. `"...(no-playwright: capability (missing playwright))"`).

## Tests added
- `pkg/agentselect/agentselect_test.go`: updated `TestSelectCapabilityFilter`'s assertion for the new elimination string; added `TestSelectExtraneousCapabilities` (lean agent beats a kitchen-sink generalist with a perfect track record; asserts `ScoreBreakdown.Extraneous` values).
- `pkg/agentselect/agentselect_test.go`: fixed `TestSelectRecencyTiebreak`'s isolation mechanism — it previously used an extra MCP-server capability to isolate the "recent" candidate for seeding recency, which the new extraneous-count tier now decides *before* reaching recency, breaking the test's premise. Switched isolation to a tag/label-affinity signal instead, keeping both candidates' capability sets identical.
- `services/pipeline/engine/engine_test.go`: fixed `newHarness()` to actually create the `"test"` Workspace record it references (previously latent — `resolveStageAgent` didn't fetch workspaces before this change, so the gap was invisible). Added `TestAutoSelectPopulatesEffectiveCapabilities` (task+workspace union feeds the filter; logged event names the missing capability) and `TestAutoSelectNoCandidateNamesMissingCapability` (blocked-task note names the missing capability).
- `pkg/agentgen/agentgen_test.go`: added `TestGenerateFromTaskDeclaresRequiredCapabilities` (a requirement-bearing task's draft declares the required capability even when the scripted Fast Model reply omits it).

## Verification
- `go build ./...`, `go test -race ./...` (full repo) — all green.
- `golangci-lint run ./...` — 0 issues.
- `make check` (Go lint+test, `npm run lint`, `npm run test`, 222 UI test files / 1339 tests) — all green.

## What was NOT done / reviewer should scrutinize
- `resolveStageAgent`'s new `effectiveCapabilities` call makes 2–3 extra Data Service round trips (session, optional workspace, capability registry) per "auto" stage resolution. This is not a hot loop (only fires for `agent: auto` stages), so no caching/freezing was added — matches the project's "don't add for scenarios that can't happen / aren't needed" guidance, but flagging in case call volume is higher than assumed in some template.
- Workspace/session lookups in `effectiveCapabilities` propagate real errors rather than degrading gracefully. In production, `services/pipeline/main.go`'s `run()` already validates the session has a resolvable workspace before `engine.Run()` is invoked, so a broken workspace reference "can't happen" there — but this means any *test* harness building an engine `Config` directly (bypassing `main.go`'s validation) must create a real workspace matching `Session.Workspace`, same as the `newHarness()` fix here. If other test files construct sessions/engines outside `engine_test.go`'s harness, they may need the same fix (grepped and found none currently).
- The ranking-tier position (extraneous count second, ahead of track record/recency) is a judgment call within the brief's constraints, not dictated verbatim — reasoning is captured in code comments on `rankCandidates` and the package doc comment.
