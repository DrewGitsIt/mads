# HANDOFF: Per-profile environment mode (inherit vs isolated agent environments)

Task: tsk_227685c0a311eb46

## What was done

Restored and finished a prior interrupted attempt at this task
(`backup/tsk_227685c0a311eb46`, a WIP commit that had the low-level helper
and provider wiring but never reached the real spawn-site integration or
the Settings UI). Reused that WIP's `pkg/agentenv` package and the
`pkg/contracts`/`providers/claudecode`/`providers/kirocli` changes near
verbatim (all applied cleanly via `git apply` against current `main`,
re-verified with the full test suite), then built the rest from scratch.

**`pkg/agentenv`** (new package, restored from the backup) — the single
source of truth for constructing a spawned agent's environment:
`Build(mode, parentEnv, credentials, passthrough, extra) []string`. Inherit
mode (also the zero value) reproduces `append(os.Environ(), cfg.Env...)`
byte-for-byte. Isolated mode filters `parentEnv` to a fixed `BaseAllowlist`
(PATH, HOME, USER, LOGNAME, SHELL, TERM, LANG, TZ, TMPDIR,
XDG_RUNTIME_DIR) plus `LC_*` by prefix plus the passthrough names, then
appends `credentials` and `extra` unconditionally (they never originate
from `parentEnv`, so isolation doesn't need to filter them). I added
`Select(env, names) []string` on top of the backup's `Build`+tests: it
extracts `NAME=VALUE` pairs for given names out of a process's own
environment, used by both spawn sites below to pull bound-credential
*values* out of the session/pipeline process's already-populated
`os.Environ()` (materialized there by the Router's existing spec §13
injection) without re-resolving against a vault at this layer.

**`pkg/contracts`** — `Config.EnvMode` (`"inherit"`/`"isolated"`, zero value
= inherit) and `Config.EnvPassthrough` (`[]string` of NAMES), with
`EnvMode.Valid()` and a `ValidateConfig` check. Both fields are ordinary
JSON fields on `Config`, so the existing generic reflection-based profile
snapshot/diff machinery (`pkg/contracts/profile.go`) picks them up with no
extra Go code.

**`providers/claudecode`, `providers/kirocli`** — each `Config` gained
`EnvMode`/`EnvPassthrough`; the private `env()` method (the one function
every spawn path — `Start`, `OneShot`, PTY — already funneled through)
now calls `agentenv.Build` instead of the old inline
`append(os.Environ(), cfg.Env...)`. Restored from the backup along with its
`envdump_test.go` per provider: builds the pre-existing
`pkg/capability/testdata/envdump` test binary once, spawns it as `p.cfg.Bin`
through `p.env(extra)`, and asserts on its actual OS-level environment —
proves a planted secret is excluded in isolated mode, a passthrough name
and bound credential are included, and inherit mode still passes everything
through.

**Spawn-site wiring** (the part the backup never reached) —
`services/session/main.go` and `services/pipeline/main.go` each gained a
`resolveAgentEnv(cfg contracts.Config, parentEnv []string) (mode, passthrough,
credEnv)` helper: in isolated mode it returns `cfg.EnvPassthrough` and
`agentenv.Select(parentEnv, cfg.CredentialBindings)`; in inherit mode it
returns `nil, nil` for both, since `parentEnv` already carries the bound
credentials and re-adding them would diverge from the byte-identical
inherit guarantee. `services/session/main.go` was reordered so its
`dataclient.Config` fetch happens before the provider registry is built
(previously the registry was built first); `services/pipeline/main.go`
already fetched `Config` before `buildProviderRegistry`, so that one just
gained the two new parameters threaded through. Both `main_test.go` files
got table-style unit tests for `resolveAgentEnv` proving the inherit/
isolated split and the "inherit omits credEnv" byte-identity property.

**Names-only boundary** — added
`TestConfigCredentialFieldsCarryNamesNotValues` in `pkg/contracts` guarding
that `CredentialBindings`/`EnvPassthrough` entries never contain `=`
(i.e. can't silently become NAME=VALUE pairs and leak a value over the
Data Service HTTP boundary in Config's JSON payload).

**Settings UI** — `ui/src/router/types.ts` gained `Config.env_mode`/
`env_passthrough` and an `EnvMode` type; `sections.ts` maps both to the
existing "credentials" section (generic diff/snapshot machinery handles
them with zero extra code, same as every other field). `CredentialsSection`
gained an "Environment" group: a Chip-based inherit/isolated toggle (edits
`draft` only, like `credential_bindings` toggling elsewhere in this
component — no auto-save, drift/DriftBar handles persisting), a
plain-language summary line ("Isolated: base environment + 1 credential +
1 passthrough (AWS_PROFILE)" / "Inherited environment + 1 mads credential"),
and an add/remove passthrough-chip editor visible only in isolated mode.
6 new tests in `CredentialsSection.test.tsx`.

**`providers/README.md`** — new "Contract notes" bullet documenting the
trust/env_mode split (trust = what an agent can DO, env_mode = what it can
SEE) and pointing at `pkg/agentenv` as the one place the semantics are
defined.

## What was not done / scoped out

- **MCP server spawns** (`pkg/capability/mcp.go`, `os.Environ()` +
  `exec.Command`) were left untouched. These spawn tool servers a session
  talks to over stdio, not "the agent" itself — the task's acceptance
  criteria call out provider CLI spawns and PTY/shell-dock spawns
  specifically. Flagging this as a **judgment call the reviewer should
  scrutinize**: if env_mode is meant to also cover what an MCP tool server
  can see (some MCP servers do access secrets, e.g. cloud CLIs), this is a
  gap. I left it out because the task's explicit spawn-site list didn't
  name it and MCP servers already have their own scoped env injection
  path (`capability.go`'s credential resolution) that's a separate
  mechanism from profile-level `EnvMode`.
- **`ui/electron/pty/ptyHost.ts`, `ShellDock.tsx`** confirmed out of scope
  by their own doc comments (explicitly "not the agent's environment") —
  no changes made, verified via `SessionTerminalBridge` that the real
  agent PTY terminal path goes through the Go provider layer already
  covered by the `env()` rewiring.
- No dedicated end-to-end test spawns the real `services/session` or
  `services/pipeline` binary with `env_mode: isolated` all the way through
  an HTTP-driven session/run and inspects a real spawned OS process's
  environment. The acceptance criteria's "integration test per site with a
  fake agent that dumps env" is satisfied at the provider layer
  (`envdump_test.go` in both `providers/claudecode` and `providers/kirocli`
  — this is where `os.Environ()`/`exec.Cmd.Env` actually happens, and both
  services' spawn sites route through the same `Provider.env()` method),
  plus `resolveAgentEnv` unit tests proving each service's config→env-mode
  glue is correct. I did not build a same-binary-different-layer
  duplicate of the envdump proof through the full session/pipeline HTTP
  stack (`providers/fake` doesn't spawn OS processes at all, and pointing
  `buildProviderRegistry`'s hardcoded `claude`/`kiro-cli` `Bin` at envdump
  for a test would require test-only production wiring). **Reviewer should
  decide whether this is sufficient** or whether a full end-to-end proof at
  each service's `main_test.go` is required by a strict reading of the
  acceptance criteria.

## Tests / make check

- `go test -race ./...` — all green, including new tests in
  `pkg/agentenv`, `pkg/contracts`, `providers/claudecode`,
  `providers/kirocli`, `services/session`, `services/pipeline`.
- `golangci-lint run ./...` — 0 issues.
- `npm run lint` (ui) — clean.
- `npm run test` (ui, vitest) — 211 files / 1178 tests, all green
  (12 in `CredentialsSection.test.tsx`, 6 new).
- `npm run build` (ui) — `tsc --noEmit` + `vite build` +
  `tsc -p electron/tsconfig.json` all succeed.

## Files changed

- `pkg/agentenv/agentenv.go`, `agentenv_test.go` (new)
- `pkg/contracts/config.go`, `config_test.go`
- `providers/claudecode/claudecode.go`, `envdump_test.go` (new)
- `providers/kirocli/kirocli.go`, `envdump_test.go` (new)
- `providers/README.md`
- `services/session/main.go`, `main_test.go`
- `services/pipeline/main.go`, `main_test.go`
- `ui/src/router/types.ts`
- `ui/src/features/settings/sections.ts`
- `ui/src/features/settings/sections/CredentialsSection.tsx`,
  `CredentialsSection.module.css`, `CredentialsSection.test.tsx`
