# HANDOFF: tsk_10cef567304fe88e — Context assembly scoped to the effective capability set

## What was done

- Added `contracts.StageCapabilitiesMounted` (`stage.capabilities_mounted`) event
  type (`pkg/contracts/event.go`), documented with its payload shape.
- `services/pipeline/engine`:
  - `engine` gained `mcpRegistry *capability.Registry` / `credStore *credstore.Store`
    fields, lazily constructed in `Run()` when `cfg.MadsHome != ""` (mirrors
    `services/session/main.go`'s construction exactly). Both stay `nil` when
    `MadsHome` is unset — mounting then degrades to "decision computed and
    logged, nothing actually started/installed" rather than failing.
  - `scopedCapabilities(ctx, task, agent)` computes, per stage, the
    intersection of the stage's resolved agent's declared MCP
    servers/skills with `agentselect.EffectiveCapabilities(task, workspace)`.
    Empty effective set → full agent declaration, `scoped=false`, nothing
    skipped (today's unannotated-task behavior, unchanged).
  - `mountCapabilities(...)` installs mounted skills
    (`capability.InstallSkill`) and starts mounted MCP servers
    (`capability.Registry.EnsureStarted`, with credstore-resolved env),
    then emits `stage.capabilities_mounted` with mounted/skipped names per
    type plus `scoped`. Best-effort throughout — a resolve/install/start
    failure is logged, never fails the stage.
  - Wired into `runAgent`, right after `resolveStageAgent` succeeds, for
    every stage (auto-selected or explicit agent).
- Two new Go tests in `engine_test.go`:
  `TestScopedCapabilitiesMountsIntersection` (annotated task → exactly
  effective ∩ declared) and `TestUnscopedCapabilitiesMountsFullDeclaration`
  (unannotated task → full declaration, `scoped:false`). Both assert on the
  `stage.capabilities_mounted` event payload.
- `make check` passes (golangci-lint, `go test -race ./...`, UI test suite).

## What was not done (and why)

- **No changes to `provider.OneShotRequest`, `providers/claudecode`, or
  `providers/fake`.** "Mounting" an MCP capability for a one-shot stage
  session means `capability.Registry.EnsureStarted` only (system-wide
  process lifecycle) — not wiring a `--mcp-config`/`MCPServerRef` into the
  one-shot call. Confirmed this matches existing precedent: even interactive
  sessions never wire registry-declared MCP capabilities into a provider
  launch except the synthetic per-session orchestrate server, and spec
  §12.4's availability/injection split is skills-only — MCP servers are just
  "started automatically... system-wide" with no injection concept. Adding
  that plumbing would be new work beyond both spec text and precedent.
- **No new `engine.Config` fields, no changes to `pkg/pipelinerun`,
  `cmd/mads/run.go`, or `services/pipeline/main.go`.** `MadsHome` already
  threads end-to-end into `engine.Config`; the registry/credstore are
  constructed lazily inside `engine.Run()` itself, mirroring
  `services/session/main.go`'s exact pattern.
- **No baseline/system-capability exemption mechanism added.**
  `contracts.Capability.System` is a registry lifecycle marker ("ships with
  mads; editable but not deletable", spec §12), unrelated to session-time
  mounting scope — no such concept exists today. Acceptance criterion #3 is
  satisfied as a documented no-op in `scopedCapabilities`'s doc comment,
  noting where the exemption would go if one is introduced.
- `effectiveCapabilities` (the existing auto-select capability filter) was
  left untouched; `scopedCapabilities` is a new, separate method reusing
  `agentselect.EffectiveCapabilities`. Keeping them decoupled avoided any
  risk to the already-tested auto-select hard-filter/ranking logic.

## What a reviewer should scrutinize

- `scopedCapabilities` fetches `Session`/`Workspace` itself rather than
  reusing whatever `resolveStageAgent`/`effectiveCapabilities` already
  fetched for an auto-select stage — two Data Service round-trips per
  auto-selected stage instead of one. This was the smallest-change option
  (no signature changes to existing, tested functions); worth a second look
  if this path turns out to be hot.
- `mountCapabilities`'s skill/MCP resolution failures are logged via the
  stdlib `log` package, not a `stage.*` event — only the final mount/skip
  decision is logged as an event. If per-capability failure diagnosis needs
  to be queryable (not just grep-the-process-log), that's follow-up work.
- **Disclosure, not related to the diff itself:** during research for this
  task, a subagent dispatch's relayed tool output was flagged by the harness
  as instruction-shaped (matched a `dangerously-skip-permissions` pattern).
  I did not act on or trust that content — I discarded it and re-verified
  every fact it claimed via direct, first-party `Read`/`grep` calls before
  writing any code. No code or decision in this diff is based on that
  output. Flagging it here since a reviewer may want to know this occurred
  during the session.

Task: tsk_10cef567304fe88e
