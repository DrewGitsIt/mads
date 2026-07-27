# tsk_8d7c9187526848ec — agentgen registry grounding + phantom capability cleanup

## What was done

1. **pkg/agentgen**: `Generator.generate` now fetches the live capability
   registry (`dataclient.Client.Capabilities`) before calling the Fast Model,
   lists the actual available skill/MCP names in the prompt (or "none
   available"), and — after generation — drops any skill/MCP name the model
   emitted that isn't in that list before building the persisted
   `contracts.Agent`. Drops are never silent: logged via `log.Printf` and
   also surfaced on a new `Result.Dropped []string` field. `capabilities.tools`
   is left untouched — those are provider tool names, not registry entries.
   `services/router/internal/job/agentgen.go` serializes the whole
   `agentgen.Result` as the `AgentDraftGenerated` event payload already, so
   `Dropped` reaches the event log/UI with no additional plumbing.

2. **Write-side guard**: `Registry[T]` (services/data/internal/store/registries.go)
   gained a `refCheck` hook run inside the write transaction for both
   `Create` and `Update`. `NewAgents` wires it to `checkAgentCapabilityRefs`,
   which rejects any `capabilities.skills`/`capabilities.mcp_servers` entry
   with no matching row in the `capabilities` table, naming every offending
   entry in one `contracts.ValidationError`. This surfaces as **422**
   (the codebase's existing uniform validation-error status, per
   `writeStoreError` — not the 400 mentioned casually in the task text;
   every other registry validation error in this codebase, e.g. the
   workspace primary-repo rule, uses 422, so I matched that convention
   rather than introducing a one-off 400).

3. **Cleanup migration** `0019_system_agent_capability_cleanup.{up,down}.sql`:
   new migration (no amendment of any applied migration) that strips the
   phantom `capabilities.skills` blob from the 6 system agent rows down to
   `"capabilities":{}`, using the same `REPLACE(data, ...)` pattern as 0017.
   `down.sql` restores the original phantom lists.

## Explicitly out of scope (per task text)

- Deleting a capability that agents still reference — not implemented.
  Nothing currently prevents deleting a `capabilities` row that a
  `refCheck`-validated agent still points at; the agent would then reference
  a now-missing capability again. This is a real gap but was explicitly
  named out of scope by the task.

## Tests added

- `services/data/internal/store/registries_test.go`:
  `TestAgentCapabilityRefsValidated` (write-side guard: Create/Update,
  mixed skill+mcp, tools-only never rejected) and
  `TestSystemAgentsNoPhantomCapabilities` (post-cleanup: every system agent's
  skill/mcp refs resolve in the registry — the direct acceptance-criterion
  test for the migration).
- `services/data/internal/api/registries_test.go`:
  `TestAgentCapabilityRefsRejectedOverHTTP` (POST/PUT through the HTTP API,
  422 naming unknown refs, succeeds once registered).
- `pkg/agentgen/agentgen_test.go`:
  `TestGenerateKeepsOnlyRegisteredCapabilities` and
  `TestGenerateWithEmptyRegistryDropsAllSkillsAndMCP`.
- `services/data/internal/store/migrate_test.go`: `TestMaxVersion` literal
  bumped 18→19.

## Blast radius fixed (pre-existing tests, not new behavior)

The new write-side guard rejects any agent test fixture that names a
skill/MCP not first registered as a capability. Fixed by registering the
matching capability before agent creation, not by weakening the guard:
- `pkg/agentselect/agentselect_test.go` (`setup` now pre-registers
  "acme-tool", "shared-tool", "unique-tool" as MCP capabilities).
- `pkg/agentgen/agentgen_test.go` golden tests (registered "testing"
  skill / "postgres" MCP before generation — goldens unchanged, confirming
  registry-grounding is a no-op when the registry already has what the
  model names).
- `services/session/internal/runner/capability_test.go` already registered
  matching capabilities before every agent fixture — no changes needed.

## Verification

`make check` equivalent run in full: `go build ./...`, `go test -race ./...`
(whole repo), `golangci-lint run ./...`, `npm run lint` and `npm run test`
in `ui/` — all pass, zero lint issues, zero test failures.

## For the reviewer to scrutinize

- The 422-vs-400 status code choice (see write-side guard section above).
- `checkAgentCapabilityRefs` does one `SELECT COUNT(*)` per referenced name
  inside the transaction — fine at current scale, but not batched.
- Migration 0019 hardcodes the exact phantom-skill substrings from the
  original seed data (mirroring 0017's approach) rather than parsing/
  rewriting JSON; if a system agent's seed skill list ever changes before
  this migration lands elsewhere, the `REPLACE` becomes a silent no-op
  (would need `TestSystemAgentsNoPhantomCapabilities` to catch that, which
  it does).
