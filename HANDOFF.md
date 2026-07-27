# HANDOFF: Ship playwright-ui-testing as the first system skill capability

Task: tsk_1074a4ea4479c84a
Commits: a887a94, b94704a, d069730, 2acaf5e

## Recovery note

This branch's ref (`mads/tsk_1074a4ea4479c84a`) pointed at the fork point
from `main` with none of the task's actual work — a prior session's
commits existed only as `backup/tsk_1074a4ea4479c84a` plus one dangling
commit (`b26301e`, the migration-collision renumber) never reachable from
any branch. Recovered by cherry-picking the four task-scoped commits
(`c46aa6f`, `7f7f500`, `b26301e`, in that order — the intermediate
`bd74f85` was just a pipeline auto-commit of the prior session's
now-superseded HANDOFF.md, skipped) onto the current branch tip. They
applied cleanly with no conflicts: none of the four touch any of the files
that diverged between the old and new fork points (the agent-editor
redesign, lifecycle-controls work). This recovery is `a887a94`/`b94704a`/
`d069730`; everything below `## What was done` from here down is new work
in `2acaf5e`.

## What was done (inherited, a887a94 / b94704a / d069730)

**Part 1 — System flag + seeding path**
- `Capability.System bool` (`pkg/contracts/registry.go`), mirroring
  `Agent.System`/`Template.System`; wired into `NewCapabilities`'s
  `system` extractor (`services/data/internal/store/registries.go`) so
  the generic editable-not-deletable guard applies to capabilities too.
- `pkg/capability/shipped.go`: `ShippedCapabilities`, computing an
  absolute repo-root path via `runtime.Caller(0)`.
  `services/data/internal/store/shipped.go`'s `SeedShippedCapabilities`
  reconciles that list against the registry (skip-if-present, idempotent),
  computing real health via `capability.Health`.

**Part 2 — the skill**
- `assets/skills/playwright-ui-testing/SKILL.md`: procedural. Covers
  `make ui-smoke` / targeted `npx --prefix ui playwright test`, the
  shared-topology-vs-serial-mode split (verified against the actual
  `ui/e2e` suite, not assumed — see the b94704a review-fix commit),
  a throwaway-spec template, reading traces/screenshots on failure, and
  cleanup.

**Part 3/4 — registration + attachment**
- Registered as `type=skill`, `System: true`, `config.path` = the real
  `assets/skills/playwright-ui-testing` directory.
- Migration `0020_playwright_skill.{up,down}.sql` (renumbered from 0019
  in `d069730` — trunk's `agentgen` task landed
  `0019_system_agent_capability_cleanup` first) appends
  `"playwright-ui-testing"` to `agt_system_executor` and
  `agt_system_verifier`'s `capabilities.skills`.

## What I did (new work, 2acaf5e)

Trunk had moved since the prior session: the `agentgen` task
(`0019_system_agent_capability_cleanup`) added write-side validation —
`checkAgentCapabilityRefs` in `registries.go` — that rejects any
agent Create/Update whose `capabilities.skills`/`mcp_servers` names don't
resolve in the live `capabilities` table. That collided with this task's
own migration: 0020 attaches a `playwright-ui-testing` ref to
executor/verifier, but `SeedShippedCapabilities` only ran from
`main.go`/`datatest.go`, as a step *after* `Migrate`, called by production
and by `datatest.Start` — never by the store package's own
`openMigratedDB` test helper. Any test using that helper (which is most
of `services/data/internal/store`'s suite) held those two system agents
referencing a capability absent from the registry, so `TestSystemAgentsEditableNotDeletable`
(edits `executor`, triggers `checkAgentCapabilityRefs` on Update) and
trunk's own `TestSystemAgentsNoPhantomCapabilities` (built for the *previous*
cleanup, now regressed by *this* task's own migration) both failed.

Fix: added `store.Bootstrap(ctx, db)` (`shipped.go`) — `Migrate` +
`SeedShippedCapabilities` as one step — and pointed every real store
consumer at it instead of `Migrate` alone: `services/data/main.go`,
`services/data/datatest/datatest.go`. I deliberately did **not** fold
seeding into the shared `openMigratedDB` test helper (log_test.go) itself:
`SeedShippedCapabilities` writes through `caps.Create`, which appends a
`capability.created` event to the same outbox `TestOutboxAtomicity` and
`TestQueryFilters` assert exact counts against — folding it in broke both.
Instead, the two tests that actually exercise the capability-ref invariant
(`TestSystemAgentsEditableNotDeletable`, `TestSystemAgentsNoPhantomCapabilities`)
now call `SeedShippedCapabilities` explicitly after `openMigratedDB`,
matching the pattern `shipped_test.go` already used for its own two tests.
`shipped.go`'s `Bootstrap` doc comment explains the split.

Also hit and fixed, both fallout from trunk having moved:
- `services/session/internal/runner/capability_test.go`'s
  `TestSetupCapabilitiesForShippedSystemAgentsSucceeds` called
  `r.setupCapabilities(ctx, sess, cwd)` — a signature that predates a
  trunk change (`setupCapabilities(ctx, cwd string, agent contracts.Agent,
  agentOK bool)`). Fixed to look up the `contracts.Agent` by name first
  and call with the current signature, matching every other test in the
  file.
- `TestMaxVersion`'s literal bumped 19→20 (mechanical, per the renumber).

**Tests (all passing, including all three acceptance-criteria tests)**
- `services/data/internal/store/shipped_test.go`:
  `TestShippedCapabilitiesSeeded`, `TestShippedCapabilitiesAttachedToExecutorAndVerifier`.
- `pkg/capability/skill_test.go`: `TestInstallSkillShippedPlaywrightSkill`.
- `services/session/internal/runner/capability_test.go`:
  `TestSetupCapabilitiesForShippedSystemAgentsSucceeds`.
- Full `go build ./...`, `go vet ./...`, `go test -race ./...`,
  `golangci-lint run ./...` (0 issues), and `make check` (Go + UI, 219 UI
  test files / 1281 tests) all clean.

## What was not done

- No UI/frontend changes — Go-only task, no `ui-smoke` run needed.
- Did not touch write-side capability-ref validation itself (that's the
  `agentgen` task, already landed on trunk before this session started) —
  only adapted to it.
- Did not fold shipped-capability seeding into every conceivable
  `Migrate(db, Migrations())` call site (e.g. `compat_test.go`,
  `services/data/internal/api/events_test.go`) — those don't touch
  agent/capability records or go through the write-side validator, so the
  invariant this fix protects was never at risk there, and adding seeding
  would be unrequested scope with the same event-log side effect risk
  seen in `log_test.go`.

## Reviewer should scrutinize

- **The `Bootstrap` vs. bare-`Migrate` split** (`services/data/internal/store/shipped.go`):
  worth confirming the boundary I drew — "API-consumer-shaped store
  bring-up gets seeding, low-level store-mechanics unit tests don't, tests
  that need the invariant seed explicitly" — is the right one rather than,
  say, seeding via direct SQL insert (bypassing the outbox event) so
  `Bootstrap` could safely replace `Migrate` everywhere with no event-log
  side effect. I judged that a larger, unrequested change to
  `SeedShippedCapabilities`'s existing Create-path design (which the prior
  session chose deliberately per its own HANDOFF, to get real health
  computation and the normal registry code path) versus the smaller
  targeted fix I made.
- **`services/router/internal/api/registries_test.go`** and its
  `len(list) != 1` → contains-created-capability change: inherited from
  the prior session (a887a94), not re-verified independently by me beyond
  confirming the full suite (including this file) passes.
- The branch-recovery cherry-pick itself: I verified the four recovered
  commits' diff is byte-identical in intent to what `backup/tsk_1074a4ea4479c84a`
  held (checked via `git diff` between the ranges) before picking, but
  worth a second look that nothing was silently dropped in the process.
