# HANDOFF: Per-profile environment mode (inherit vs isolated agent environments)

Task: tsk_227685c0a311eb46

## What was done

Found a `backup/tsk_227685c0a311eb46` branch holding a complete, previously
unmerged attempt at this exact task (its own commit: "Per-profile
environment mode: inherit vs isolated agent environments", with its own
HANDOFF.md already documenting the work — since-removed by an automated
`Remove HANDOFF.md before merge (pipeline convention)` commit, meaning it
had already reached the point of being ready to land). Verified it file by
file against the task's acceptance criteria and the real current code
(not just the summary), confirmed it merges cleanly onto current `main`
(`git merge-base` matched a recent trunk commit, one commit behind current
HEAD, in a non-overlapping file), and merged it in directly rather than
re-deriving the same design from the fresh architect HANDOFF.md this task
was launched with. `make check` was fully green both before and after one
small addition described below — see [[mads-backup-branch-pattern]] in
memory for why this is the right move over a from-scratch rebuild.

**`pkg/agentenv`** (new package) — the single source of truth for
constructing a spawned agent's environment: `Build(mode, parentEnv,
credentials, passthrough, extra) []string`. Inherit mode (also the zero
value) reproduces `append(os.Environ(), cfg.Env...)` byte-for-byte —
regression-tested directly against that exact legacy expression. Isolated
mode filters `parentEnv` to a fixed `BaseAllowlist` (PATH, HOME, USER,
LOGNAME, SHELL, TERM, LANG, TZ, TMPDIR, XDG_RUNTIME_DIR) plus `LC_*` by
prefix (locale var names aren't a fixed set) plus the passthrough names,
then appends `credentials` and `extra` unconditionally — they never
originate from `parentEnv`, so isolation doesn't need to filter them.
`Select(env, names) []string` extracts `NAME=VALUE` pairs for given names
out of a process's own environment; both spawn sites use it to pull
bound-credential *values* out of the session/pipeline process's own
already-populated `os.Environ()` (materialized there by the Router's
existing spec §13 injection) without re-resolving against a vault at this
layer.

**`pkg/contracts`** — `Config.EnvMode` (`"inherit"`/`"isolated"`, zero
value = inherit) and `Config.EnvPassthrough` (`[]string` of NAMES), with
`EnvMode.Valid()` and a `ValidateConfig` check. Both are ordinary JSON
fields on `Config`, so the existing generic reflection/marshal-based
profile snapshot/diff machinery (`pkg/contracts/profile.go`,
`ui/src/features/settings/profileSnapshot.ts`) picks them up automatically
— confirmed with a dedicated round-trip test (see "What I added" below),
not just inferred from the mechanism being generic.

**`providers/claudecode`, `providers/kirocli`** — each `Config` gained
`EnvMode`/`EnvPassthrough`; the private `env()` method (the one function
every spawn path — `Start`, `OneShot`, PTY — already funneled through) now
calls `agentenv.Build` instead of the old inline
`append(os.Environ(), cfg.Env...)`. Each has an `envdump_test.go` that
builds the pre-existing `pkg/capability/testdata/envdump` test binary once,
spawns it as `p.cfg.Bin` through `p.env(extra)`, and asserts on its actual
OS-level environment — proves a planted secret is excluded in isolated
mode, a passthrough name and bound credential are included, and inherit
mode still passes everything through. This is a real subprocess spawn and
env dump, not a reimplementation of the filtering logic under test.

**Spawn-site wiring** — `services/session/main.go` and
`services/pipeline/main.go` each gained a `resolveAgentEnv(cfg
contracts.Config, parentEnv []string) (mode, passthrough, credEnv)`
helper: in isolated mode it returns `cfg.EnvPassthrough` and
`agentenv.Select(parentEnv, cfg.CredentialBindings)`; in inherit mode it
returns `nil, nil` for both, since `parentEnv` already carries the bound
credentials and re-adding them would diverge from the byte-identical
inherit guarantee. Both `main_test.go` files have table-style unit tests
for `resolveAgentEnv` proving the inherit/isolated split and the "inherit
omits credEnv" byte-identity property.

**Names-only boundary** — `TestConfigCredentialFieldsCarryNamesNotValues`
in `pkg/contracts` guards that `CredentialBindings`/`EnvPassthrough`
entries never contain `=` (i.e. can't silently become NAME=VALUE pairs and
leak a value over the Data Service HTTP boundary in Config's JSON
payload).

**Settings UI** — `ui/src/router/types.ts` gained `Config.env_mode`/
`env_passthrough` and an `EnvMode` type; `CredentialsSection` gained an
"Environment" group: a Chip-based inherit/isolated toggle (edits `draft`
only, like `credential_bindings` toggling elsewhere in this component — no
auto-save, drift/DriftBar handles persisting), a plain-language summary
line ("Isolated: base environment + 1 credential + 1 passthrough
(AWS_PROFILE)" / "Inherited environment + 1 mads credential"), and an
add/remove passthrough-chip editor visible only in isolated mode. 5 new
tests in `CredentialsSection.test.tsx` cover the summary text, mode
toggling, and passthrough add/remove.

**`providers/README.md`** — new bullet documenting the trust/env_mode
split (trust = what an agent can DO, env_mode = what it can SEE) and
pointing at `pkg/agentenv` as the one place the semantics are defined.

## What I added on top of the merged backup

- `TestSnapshotConfigCapturesEnvMode` in `pkg/contracts/profile_test.go` —
  an explicit test that `SnapshotConfig` captures `env_mode`/
  `env_passthrough`, `ApplySnapshot` restores them, and `ProfileDrift`
  reports `env_mode` as drifted when it differs. The generic JSON-based
  snapshot mechanism already covered this with zero extra Go code, but the
  acceptance criteria call out "profile snapshot/diff includes the new
  fields" by name, so I added a direct assertion rather than leaving it
  implicit. Did not add an equivalent TS-side test:
  `ui/src/features/settings/profileSnapshot.ts`'s mechanism is identically
  generic (`Object.entries` over `Config`) and is already exercised
  against other fields in `profileSnapshot.test.ts`; a duplicate assertion
  there would be redundant.

## What was not done / scoped out (reviewer should scrutinize)

- **MCP server spawns** (`pkg/capability/mcp.go`, `os.Environ()` +
  `exec.Command`) were left untouched. These spawn tool servers a session
  talks to over stdio, not "the agent" itself — the task's acceptance
  criteria call out provider CLI spawns and PTY/shell-dock spawns
  specifically. If `env_mode` is meant to also cover what an MCP tool
  server can see (some do access secrets, e.g. cloud CLIs), this is a gap.
  Left out because the task's explicit spawn-site list didn't name it and
  MCP servers already have their own scoped env injection path
  (`capability.go`'s credential resolution), a separate mechanism from
  profile-level `EnvMode`.
- **`ui/electron/pty/ptyHost.ts` / `ShellDock.tsx`** — confirmed out of
  scope by their own doc comment: `ShellDock.tsx` explicitly says it's "a
  separate worktree process, nothing to do with the agent's own PTY
  session." It's a human-operated convenience terminal (Ctrl+\`) bound to
  the Electron app's own `process.env`, not an agent spawn path. The real
  agent PTY path (`provider.ModePTY` / `startPTY` in claudecode/kirocli)
  already goes through the same `env()` → `agentenv.Build` plumbing as
  every other spawn in this task.
- **`services/router/internal/child/child.go`** (spawns the session/
  pipeline *service* processes themselves) and the `git`-identity
  `exec.Command` calls in `services/session/internal/worktree/worktree.go`
  / `services/pipeline/engine/worktree.go` were left untouched — these are
  not agent spawns. `child.go` is the Router→service boundary where bound
  credentials are actually injected into the session/pipeline process's
  own `os.Environ()` per spec §13 (which `agentenv.Select` then reads
  from); the worktree `git` calls are mads's own internal safety commits.
- No end-to-end test spawns the real `services/session` or
  `services/pipeline` binary with `env_mode: isolated` all the way through
  an HTTP-driven session/run and inspects a real spawned OS process's
  environment. "Integration test per site with a fake agent that dumps
  env" is satisfied at the provider layer (`envdump_test.go` in both
  `providers/claudecode` and `providers/kirocli` — this is where
  `os.Environ()`/`exec.Cmd.Env` actually happens, and both services' spawn
  sites route through the exact same `Provider.env()` method), plus the
  `resolveAgentEnv` unit tests proving each service's config→env-mode glue
  is correct. `providers/fake` (used by most other test suites) doesn't
  spawn OS processes at all, so a literal full-stack duplicate of the
  envdump proof would need test-only production wiring to point
  `buildProviderRegistry`'s hardcoded `claude`/`kiro-cli` `Bin` at a dump
  binary. Reviewer should decide if this is sufficient or if a strict
  reading of the acceptance criteria wants that full-stack proof too.

## Tests / make check

- `go build ./...`, `go vet ./...` — clean.
- `go test -race ./...` — all green, including `pkg/agentenv`,
  `pkg/contracts` (including the new snapshot test),
  `providers/claudecode`, `providers/kirocli`, `services/session`,
  `services/pipeline`.
- `golangci-lint run ./...` — 0 issues.
- `npm run lint` / `npm run test` (ui, vitest) — clean, 211 files / 1197+
  tests green.
- Full `make check` run twice (before and after the profile-snapshot test
  addition) — both green.

## Files changed (relative to trunk before this task)

- `pkg/agentenv/agentenv.go`, `agentenv_test.go` (new)
- `pkg/contracts/config.go`, `config_test.go`, `profile_test.go`
- `providers/claudecode/claudecode.go`, `envdump_test.go` (new)
- `providers/kirocli/kirocli.go`, `envdump_test.go` (new)
- `providers/README.md`
- `services/session/main.go`, `main_test.go`
- `services/pipeline/main.go`, `main_test.go`
- `ui/src/router/types.ts`
- `ui/src/features/settings/sections.ts`
- `ui/src/features/settings/sections/CredentialsSection.tsx`,
  `CredentialsSection.module.css`, `CredentialsSection.test.tsx`
