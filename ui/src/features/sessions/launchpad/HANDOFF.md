# Executor handoff: Launchpad preview-before-Start

Implemented per the architect's design doc (this file's prior content, now
superseded). Task: `tsk_bcca8ce43629742b`.

## What was done

**Go (`services/router`)**
- `pkg/contracts/sessiondefaults.go`: new `SessionDefaultSource`,
  `ResolvedSessionField`, `SessionDefaults` wire types.
- `internal/api/sessiondefaults.go`: `resolveSessionDefaults` refactored into
  a thin wrapper around a new `resolveSessionDefaultsSourced`, which performs
  the identical resolution but also records provenance per field. New
  `handleSessionDefaults` handler backs `GET /sessions/defaults?workspace=`.
- `internal/api/api.go`: registered the new route.
- `internal/api/sessiondefaults_test.go`: 5 new tests — no-workspace,
  workspace-set, config-set, unknown-workspace-degrades, no-provider-configured.
- All pre-existing `TestCreateSession*` tests pass unmodified, confirming the
  refactor is behavior-preserving for the two existing callers.

**UI**
- `router/types.ts`, `client.ts`, `fixtures.ts`: mirrored contracts,
  `getSessionDefaults()` on both `FetchRouterClient` and `FakeRouterClient`.
- `Launchpad.tsx`: fetches `getSessionDefaults(staged.workspace)` once in a
  `useEffect` (cancelled-guard on unmount/dep-change), passes the resolved
  value down to all 5 pills as props, and renders "Inherits from `<workspace>`
  defaults" beneath the pill row when at least one of agent/model/isolation
  was sourced from the workspace. Also removed the `WorkspacePill` onChange's
  prior auto-fill of `staged.agent/model/isolation` from `ws.defaults` — that
  logic duplicated server-side resolution and had a latent bug (set `model`
  without `provider`). Selecting a workspace now only sets `staged.workspace`.
- `AgentPill`, `WorkspacePill`, `IsolationPill`, `ModelPill`: each takes a
  `resolved` (+ `ModelPill` also takes `resolvedProvider`) prop and shows
  `"<value> (default)"` in the closed-pill label when unset.
- `TrustPill`: deleted its own `getConfig()` fetch + hardcoded `"limited"`
  fallback entirely (this was the exact duplication bug the task exists to
  fix) in favor of the `resolved` prop passed down from `Launchpad`.
- Component tests added/updated for all 5 pills plus 3 new `Launchpad.test.tsx`
  cases (inherit line shown, hidden on fetch failure, hidden when a workspace
  resolved but contributed no field).

## Not done / explicitly out of scope
- No e2e spec (`first-run.spec`, `sessions.spec`) edits — grepped for the old
  placeholder strings ("Select model/agent/workspace/isolation/trust"); no
  spec asserts on them, so nothing needed updating. Confirmed by running the
  full `make ui-smoke` suite (11/11 passing).
- Did not touch `useSuggestions.ts`, which has a pre-existing unrelated bug:
  its `load()` calls `Promise.all([listSessions(), listAllTasks("ready")])`
  with no `.catch()`. Surfaced while writing the "fetch fails" test (had to
  scope the mock rejection to only the `/sessions/defaults` URL to avoid
  tripping an unrelated unhandled-rejection warning). Left untouched as out
  of scope for this task.

## What a reviewer should scrutinize
- `TrustPill` now has a "Select trust" placeholder state that didn't exist
  before (the old hardcoded `"limited"` fallback meant it never rendered a
  bare placeholder). This is intentional — it now degrades the same way the
  other 4 pills do — but is a visible behavior change worth a second look.
- The "Inherits from `<workspace>`" line's visibility condition: it only
  shows when `agent`, `model`, or `isolation` was sourced as `"workspace"` —
  deliberately excludes `provider`/`trust` since those aren't part of
  `WorkspaceDefaults` in the current schema. Worth confirming this matches
  intent if `WorkspaceDefaults` grows more fields later.
- `resolveSessionDefaultsSourced`'s provenance recording is additive
  (assign-if-branch-fires); double check the "unknown workspace" and
  "no provider configured" branches leave `ResolvedSessionField{}` zero-valued
  as expected (covered by tests, but worth a read of the branch logic itself).

## Verification
- `make check`: pass (lint + `go test -race` + UI lint + UI unit tests, 177
  files / 744 tests).
- `make ui-smoke`: pass (11/11 Playwright e2e tests, including `sessions.spec`
  and `first-run.spec`).
