# HANDOFF: Capability requirements on tasks and workspaces

Task: tsk_796229e65f77d4b9

## What this task builds

Required capabilities exist at both the workspace and task level:

- **Workspace** (`Workspace.Capabilities []string`) — ambient environment
  facts true of every task in that workspace (e.g. "repos on GitLab").
- **Task** (`Task.RequiredCapabilities []string`) — work-specific additive
  deltas on top of the workspace baseline (e.g. "this one needs playwright").

There is no subtraction: the effective requirement for a task is always the
union of its workspace's baseline and its own deltas. Neither set is ever
required to be populated — empty means "no constraint."

Requirements reference capability registry names and are validated against
the registry at every observable store-lifecycle write point.

## What was done

**Backend** (prior sessions in this pipeline run, confirmed still passing):
- `Task.RequiredCapabilities` / `Workspace.Capabilities` fields, migration
  `0022_task_required_capabilities`, write-time registry validation in the
  store layer.
- `pkg/agentselect.EffectiveCapabilities(task, workspace)` — union of the two
  sets, plus `ExpandCapabilities` to resolve names into
  `{Tools, MCPServers, Skills}` via registry lookup on `Capability.Type`.
- Go tests: schema round-trip, write-time rejection of unknown names,
  resolver union + expansion including empty-set cases.

**UI** (this session):
- `CapabilityPickerField`'s `type` prop is now optional — omit it to offer
  candidates of every capability type (used by task/workspace pickers that
  aren't scoped to one type).
- `TaskDetailSheet` takes new optional `workspace`/`capabilities` props
  (plain props, not fetched internally, matching its existing testable-prop
  convention) and renders:
  - workspace-inherited capabilities as non-interactive `Chip`s
  - an editable `CapabilityPickerField` for `required_capabilities`, with
    candidates already covered by the workspace filtered out of the add-list
- `WorkspaceEditor` gained its own `CapabilityPickerField` wired to
  `Workspace.Capabilities`, using the `useCapabilities()` singleton hook
  (same pattern as `AgentEditor`).
- New `useSessionWorkspace(routerClient, sessionId)` hook resolves a
  session's `Workspace` by chaining `getSession` → `Session.workspace` (a
  name, not an id) → `listWorkspaces()` find-by-name, since
  `WorkspaceTabContext` only carries a `sessionId`. Takes `routerClient` as a
  prop (not the singleton), consistent with the rest of `tasks/`'s
  prop-based testability convention.
- `TasksTab` wires both `workspace` (via the new hook) and `capabilities`
  (via `useCapabilities()`) into `TaskDetailSheet`.
- New fixtures: `fixtureCapabilityRegistry: Capability[]` (full shape,
  derived from the existing `fixtureCapabilities: CapabilitySummary[]` —
  the two types are structurally incompatible, not interchangeable) and
  `fixtureWorkspaces`. `FakeRouterClient.listWorkspaces()` /
  `listCapabilities()` were previously stubs that threw `"not fixtured"`;
  both are now implemented.

## What was NOT done

- Credential scoping (`Capability.CredentialEnvVars`) is intentionally left
  unwired, per the brief — parked as future work.
- No backend changes this session; all backend work for this task landed in
  prior sessions of this same pipeline run and was only re-verified here.

## Verification

- `npx vitest run` (ui/): 222 files / 1339 tests passing.
- `npx tsc --noEmit` / `npx eslint .` (ui/): clean.
- `make check` (repo root — golangci-lint, `go test -race ./...`, ui lint,
  ui test): exits 0. Fixed one incidental `gofmt` misalignment in
  `pkg/contracts/task.go`'s `Task` struct surfaced by golangci-lint's gofmt
  linter (struct field alignment shifted after adding
  `RequiredCapabilities`) — not a functional change.

## Reviewer should scrutinize

- The `Capability` vs `CapabilitySummary` fixture split
  (`fixtureCapabilityRegistry` vs the pre-existing `fixtureCapabilities`) —
  confirm this doesn't mask a real type-unification opportunity elsewhere in
  the router types.
- Whether `TaskDetailSheet` receiving `workspace`/`capabilities` as plain
  props (rather than fetching internally) is the right call long-term, or
  whether it should own that fetch itself as more consumers appear.
