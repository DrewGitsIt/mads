# HANDOFF: wire StartOptions.MCPServers into real launches

Fast-follow from `docs/LINKED-SESSIONS-DESIGN.md` decomposition item 5
(L2c). Today `StartOptions.MCPServers` is populated by
`services/session/internal/runner/capability.go`'s `setupOrchestrate` (one
entry: `Name: "orchestrate-<sessionID>"`, `Command: "mads internal
orchestrate-mcp"`, `Env: [MADS_ROUTER_URL=..., MADS_SESSION_ID=...]`) and
handed to `provider.Provider.Start`, but only `providers/fake` actually
records/exposes it — no real provider threads it into an actual launch, so
a live agent can never reach the orchestrate MCP tools
(`list_links`/`get_summary`/`send_message`). This handoff wires
`providers/claudecode` for real and makes `providers/kirocli` take an
honest documented stance. It does **not** touch
`services/session/internal/runner` — that package's own test
(`orchestrate_test.go`, `TestSetupOrchestrateWithGrant` et al., run against
`providers/fake`) already proves the grant-gating logic and stays the
authoritative behavioral proof for *that* half of the story, per the task
brief ("the fake-provider e2e stays the behavioral proof"). This handoff
proves the other half: that the exact descriptor shape `setupOrchestrate`
produces survives translation into a real `claude` launch.

## Researched mechanism (verified against a real `claude --help` in this
environment, not guessed)

- `claude` has a real, documented `--mcp-config <configs...>` flag: "Load
  MCP servers from JSON files or strings (space-separated)". This is the
  mechanism to use — **not** writing a `.mcp.json` into the session's cwd.
  `claude mcp list`/`get` explicitly say project-scoped `.mcp.json` servers
  sit "⏸ Pending approval" until a human confirms them interactively; a
  headless `stream-json` session has nobody to do that, so a `.mcp.json`
  server would hang forever waiting on an approval that can never come.
  `--mcp-config`-supplied servers are the CLI's own documented mechanism
  for exactly this (headless/CI) use case and are not subject to that
  approval gate.
- `--strict-mcp-config`: "Only use MCP servers from `--mcp-config`,
  ignoring all other MCP configurations." Pass this whenever
  `opts.MCPServers` is non-empty, alongside `--mcp-config`. This is safe
  today with zero behavior change for anyone else: grepping the repo
  confirms nothing else currently wires agent-declared MCP capabilities
  (`agent.Capabilities.MCPServers` / `setupMCP` in `capability.go`) into
  any provider's actual launch config at all — they're started as
  health-checked subprocesses only, never connected to the agent's tool
  palette by any existing mechanism. So `--strict-mcp-config` cannot
  exclude anything that currently works; it only prevents an ambient
  project `.mcp.json` from silently attaching and hanging the session on
  approval. (That gap — declared MCP-type capabilities never actually
  reaching an agent's tool palette — is real but out of scope here; leave
  a one-line pointer to it in the claudecode README if useful, don't fix
  it.)
- `--mcp-config` accepts either an inline JSON string or a path to a JSON
  file. **Use a real temp file**, not an inline string: matches the task
  brief's own phrasing ("config file exists, args present"), gives a real
  on-disk artifact a human can `cat` while debugging a launch, and keeps
  argv short. Write it with `os.CreateTemp("", "mads-mcp-*.json")` —
  deliberately in the OS temp dir, not the session's cwd/worktree, so its
  own path is never a worktree-relative concern at all.
- Config shape (standard MCP stdio server config, same shape `claude mcp
  add-json` documents):
  ```json
  {"mcpServers": {"<name>": {"command": "...", "args": ["..."], "env": {"K": "V"}}}}
  ```

## The worktree-cwd risk (explicitly named in the task brief)

`MCPServerRef.Command` is a shell-splittable string, `strings.Fields`-split
exactly like `contracts.CapabilityConfig.Command` is split by its own
consumer (`pkg/capability/mcp.go`'s `EnsureStarted`, line ~119:
`fields := strings.Fields(cap.Config.Command)`). Mirror that splitting
convention exactly.

The risk: `claude` spawns the MCP server subprocess *itself*, from
whatever cwd `claude` was started in — a session's worktree dir
(`services/session/internal/runner/runner.go`'s `Start`, the `cwd` that
becomes `opts.Cwd` and `cmd.Dir`), not this adapter process's own cwd. A
bare command name resolved only against *this* process's `PATH` doesn't
reliably re-resolve inside whatever subprocess tree `claude` spawns from a
different directory. Fix: **resolve `fields[0]` to an absolute path via
`exec.LookPath` before writing it into the config file**, in this process,
at `Start()` time — bake in an absolute path so it's immune to whatever
cwd it's later spawned from. If `exec.LookPath` fails (command not
resolvable right now), fall back to the raw token rather than erroring —
let `claude`'s own MCP server startup surface the failure; don't block the
whole session launch over one capability's misconfiguration (matches
`setupCapabilities`'s "the session must always start" philosophy, even
though this fallback lives in the provider layer, not the runner layer).

## Files to touch

### `providers/provider/provider.go`
Update the `StartOptions.MCPServers` doc comment — it currently says
"claudecode and kirocli do not yet consume this field"; that's no longer
true for claudecode. Something like:
> claudecode threads this into a real `--mcp-config` launch (both
> transports); kirocli declares `BehaviorMCPServers` unsupported
> (providers/kirocli/README.md) — the descriptor is simply never
> materialized for its sessions.

### `providers/provider/matrix.go`
Add a new behavior row — this is the "matrix discipline covers this both
directions" ask:
```go
// BehaviorMCPServers — thread StartOptions.MCPServers into the provider's
// real launch, so the agent process can actually call the declared
// servers as tools (the L2c orchestrate capability,
// docs/LINKED-SESSIONS-DESIGN.md item 5, is the first and only producer
// today). Optional: a provider with no wiring mechanism simply never
// materializes the declared server for its sessions — no error, no
// partial/degraded state, the capability just silently doesn't reach the
// agent.
BehaviorMCPServers Behavior = "mcp_servers"
```
Add to `requirements`: `BehaviorMCPServers: LevelOptional,` (same level as
`BehaviorBranching`/`BehaviorCwdReroot`).

**Every existing `Matrix()` implementation must add this key** or
`Validate()` fails with `"mcp_servers" not declared` — that's the point,
it forces every adapter (including the conformance stub, which builds via
`provider.Behaviors()` and needs no change) to take an explicit stance.
Concretely:
- `providers/fake/fake.go` `Matrix()`: add `provider.BehaviorMCPServers:
  provider.Native,` (the fake already fully records/exposes it via
  `MCPServers(ref)`).
- `providers/claudecode/claudecode.go` `Matrix()`: add
  `provider.BehaviorMCPServers: provider.Native,`.
- `providers/kirocli/kirocli.go` `Matrix()`: add
  `provider.BehaviorMCPServers: provider.Unsupported,` (see kirocli
  section below for the citation).
- `providers/conformance/stub_test.go`'s `newStub` and
  `providers/provider/registry_test.go`'s `fullMatrix` both already
  iterate `provider.Behaviors()` to build an all-Native baseline — **no
  change needed**, they pick the new row up automatically.
- `providers/provider/matrix_test.go`'s `TestRequirementLevels` hardcodes
  `len(Behaviors()) != 13` — bump to `14`.

### `providers/conformance/conformance.go`
Add a generic, adapter-agnostic probe — this is the other half of "matrix
discipline covers this both directions". It does **not** need a new
`Script` field; it needs no dialect cooperation from the agent at all,
since it only proves `Start()` degrades gracefully with the field set,
never that any particular provider's launch args are correct (that's
adapter-specific and lives in each adapter's own test file, per the task
brief — see below).

In `Check()`, after the cost-reporting block (or near
`BehaviorBranching`/`BehaviorCwdReroot`), add:
```go
c.add(provider.BehaviorMCPServers, c.mcpServers(), "")
```
New method:
```go
// mcpServers proves the field can never fail a session launch, regardless
// of declared support — a provider with no real wiring mechanism must
// silently drop the descriptor, never error (docs/LINKED-SESSIONS-DESIGN.md
// item 5; kirocli is the concrete case this protects). Verifying a
// "working" declaration actually threads the descriptor into real launch
// args/config is adapter-specific (argv/config shape differs per CLI) and
// lives in each adapter's own scripted-stand-in test, not here.
func (c *checker) mcpServers() error {
	mode := provider.ModeACP
	if !c.m[provider.BehaviorACP].Works() {
		mode = provider.ModePTY
	}
	sess, err := c.p.Start(c.ctx, provider.StartOptions{
		Mode:       mode,
		MCPServers: []provider.MCPServerRef{{Name: "conformance-probe", Command: "true", Env: []string{"FOO=bar"}}},
	})
	if err != nil {
		return fmt.Errorf("Start with MCPServers set: %w", err)
	}
	_ = sess.Close()
	return nil
}
```
`"true"` is the POSIX coreutil, present on every Linux/macOS CI image this
repo targets — used only to exercise the command-splitting/env-conversion
code path, never actually executed by the probe itself.

This one probe runs automatically for **every** adapter's
`TestConformanceAgainstScriptedBinary` (claudecode, kirocli, fake, and the
conformance package's own stub) once each `Matrix()` declares the new row
— no per-adapter test changes needed for this half.

### `providers/claudecode/mcp.go` (new file)
```go
package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

type mcpServerJSON struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type mcpConfigFile struct {
	MCPServers map[string]mcpServerJSON `json:"mcpServers"`
}

// writeMCPConfig materializes servers as a real claude --mcp-config file
// and returns its path. See HANDOFF.md's "worktree-cwd risk" section for
// why each command is resolved to an absolute path before being written.
func writeMCPConfig(servers []provider.MCPServerRef) (path string, err error) {
	cfg := mcpConfigFile{MCPServers: map[string]mcpServerJSON{}}
	for _, s := range servers {
		fields := strings.Fields(s.Command)
		if len(fields) == 0 {
			return "", fmt.Errorf("mcp server %q: empty command", s.Name)
		}
		bin := fields[0]
		if resolved, lookErr := exec.LookPath(bin); lookErr == nil {
			bin = resolved
		}
		entry := mcpServerJSON{Command: bin, Args: fields[1:]}
		for _, kv := range s.Env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if entry.Env == nil {
				entry.Env = map[string]string{}
			}
			entry.Env[k] = v
		}
		cfg.MCPServers[s.Name] = entry
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "mads-mcp-*.json")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(raw); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// mcpArgs returns the CLI args wiring servers into claude's real launch,
// and the config file path (for the caller to remove on session Close) —
// both zero/nil when there are none, so sessions without a declared MCP
// server see zero behavior change.
func mcpArgs(servers []provider.MCPServerRef) (args []string, configPath string, err error) {
	if len(servers) == 0 {
		return nil, "", nil
	}
	configPath, err = writeMCPConfig(servers)
	if err != nil {
		return nil, "", err
	}
	return []string{"--mcp-config", configPath, "--strict-mcp-config"}, configPath, nil
}
```

### `providers/claudecode/session.go` (`startStream`)
Insert `mcpArgs` after `disallowedToolsArgs`, before `p.cfg.ExtraArgs`:
```go
args = append(args, disallowedToolsArgs(opts.Trust)...)
mcpArgVals, mcpConfigPath, err := mcpArgs(opts.MCPServers)
if err != nil {
	return nil, fmt.Errorf("mcp config: %w", err)
}
args = append(args, mcpArgVals...)
args = append(args, p.cfg.ExtraArgs...)
```
On `cmd.Start()` failure, remove the config file before returning (it's
otherwise orphaned): `if mcpConfigPath != "" { _ = os.Remove(mcpConfigPath) }`.
Add `mcpConfigPath string` to `streamSession` and set it in the `s :=
&streamSession{...}` literal. In `Close()`, after `closeWait`, `if
s.mcpConfigPath != "" { _ = os.Remove(s.mcpConfigPath) }`.

### `providers/claudecode/pty.go` (`startPTY`)
Same shape: build `mcpArgVals`/`mcpConfigPath` before `pty.Start(cmd)`,
insert before `p.cfg.ExtraArgs`, clean up the file on `pty.Start` failure,
add `mcpConfigPath string` to `ptySession`, remove it in `Close()` after
`closeWait`.

### `providers/claudecode/claudecode.go` `OneShot`
No change — `OneShotRequest` has no `MCPServers` field; this is
interactive-only, matching how `Trust` is also `Start`-only.

### `providers/claudecode/claudecode.go` `Matrix()`
Add `provider.BehaviorMCPServers: provider.Native,`.

### `providers/claudecode/fakeclaude_test.go` — extend the stand-in
`parseFCArgs` currently has a catch-all that treats any non-flag arg as
the prompt (`if a.print && a.prompt == "" && !strings.HasPrefix(argv[i],
"-")`). **Without consuming `--mcp-config <path>` explicitly, the config
file path would be misparsed as the prompt** — this needs fixing whether
or not you add the new dialect below, since `TestConformanceAgainstScriptedBinary`
will now sometimes pass `--mcp-config`/`--strict-mcp-config` (via the new
conformance probe) and must not corrupt prompt parsing when it does.

Add to `fcArgs`: `mcpConfigPath string`, `strictMCPConfig bool`. Add cases
to `parseFCArgs`:
```go
case "--mcp-config":
	i++
	a.mcpConfigPath = argv[i]
case "--strict-mcp-config":
	a.strictMCPConfig = true
```

Add an `"mcp-echo"` dialect entry, special-cased inline in `fcStream`
(same pattern as the existing `attempt-write`/`attempt-command` inline
branches — `fcReply` itself is a package-level function with no `a`
in scope, so this has to live in `fcStream`/`fcTerminal`, not `fcReply`):
```go
if prompt == "mcp-echo" {
	raw, err := os.ReadFile(a.mcpConfigPath)
	reply := fmt.Sprintf("mcp_config_err=%v strict=%v mcp_config=%s", err, a.strictMCPConfig, raw)
	fcEmit(fcAssistantText(id, reply))
	fcAppendTranscript(transcript, "assistant", reply, "")
	result(reply, false)
	continue
}
```
This is the "config file exists" proof: if the file doesn't exist,
`os.ReadFile` fails and `err` shows up non-nil in the echoed reply, which
the test below asserts against. `fcTerminal` needs the same treatment for
PTY coverage — change its signature to `fcTerminal(a fcArgs)` (update the
`fakeClaudeMain` call site: `default: fcTerminal(a)`), and give it the
same `mcp-echo` special case before falling through to `fcReply`.

### `providers/claudecode/claudecode_test.go` — the L2c proof
This is "extend the existing L2c test" for this task's scope
(`providers/claudecode`, not `services/session/internal/runner` — see the
top of this document for why the runner-layer fake-provider e2e stays
as-is and isn't touched). Construct `StartOptions.MCPServers` with **the
exact descriptor shape `setupOrchestrate` produces**, so this test proves
that specific real-world shape survives translation, not an arbitrary one:

```go
// TestStartStreamMCPServersMatchesOrchestrateShape proves the L2c leader
// capability's exact descriptor shape (services/session/internal/runner/
// capability.go's setupOrchestrate) survives translation into a real
// claude launch: --mcp-config points at a file that really exists and
// contains the server verbatim. The grant-gating logic that produces this
// descriptor is proven separately, against providers/fake, in
// services/session/internal/runner/orchestrate_test.go.
func TestStartStreamMCPServersMatchesOrchestrateShake(t *testing.T) {
	p := testProvider(t)
	ref := provider.MCPServerRef{
		Name:    "orchestrate-sess-123",
		Command: "mads internal orchestrate-mcp",
		Env:     []string{"MADS_ROUTER_URL=http://localhost:9999", "MADS_SESSION_ID=sess-123"},
	}
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, MCPServers: []provider.MCPServerRef{ref},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "mcp-echo"); err != nil {
		t.Fatal(err)
	}
	// collect events through EventTurnEnd (mirror TestTrustDisallowsTools'
	// loop), assert the reply:
	//  - contains `mcp_config_err=<nil>` (file existed and was readable)
	//  - contains `strict=true`
	//  - unmarshals (after stripping the "mcp_config=" prefix) into a
	//    mcpConfigFile whose MCPServers["orchestrate-sess-123"].Command is
	//    an ABSOLUTE path ending in ".../mads" is NOT guaranteed here
	//    (there's no real "mads" on PATH in this package's own test env,
	//    LookPath will fail and the raw "mads" token passes through
	//    unresolved — assert Command == "mads", Args == ["internal",
	//    "orchestrate-mcp"] instead) and whose Env == {"MADS_ROUTER_URL":
	//    "http://localhost:9999", "MADS_SESSION_ID": "sess-123"}.
}
```
Add a PTY-transport sibling (`TestStartPTYMCPServers...`) using
`sess.(provider.PTYSession).Terminal()` + `readUntil`-style scanning (see
`providers/conformance/conformance.go`'s `readUntil` for the pattern, or
just `bufio.Scanner` over the terminal — claude-code's own PTY tests in
this package likely already have a helper; check before writing a new
one) for a marker in the echoed reply, same assertions.

**Note on `exec.LookPath` in these two tests**: since there's no real
`mads` binary on `PATH` inside `providers/claudecode`'s own test process,
`LookPath("mads")` will fail and the code correctly falls back to the raw
token — assert `Command == "mads"` (unresolved), not an absolute path.
If you want to additionally prove the *resolution* half of `writeMCPConfig`
works, add a second small case using `os.Executable()` (the test binary
itself, already absolute) as the `Command`, and assert the JSON's
`command` field equals that exact absolute path.

### `providers/claudecode/claudecode_test.go` — opt-in real smoke
`TestRealClaudeSmoke` already exists (opt-in via `MADS_CLAUDE_SMOKE=1`,
skips cleanly with no `claude` on `PATH`). Add a sibling,
`TestRealClaudeMCPSmoke`, that spawns a **real** `mads internal
orchestrate-mcp` process as the MCP server (it doesn't need a live Router
to answer `initialize`/`tools/list` — `pkg/orchestratemcp.New` only
constructs a client lazily; the Router URL is only dereferenced on an
actual tool *call*, not on tool *listing*) and asks a real `claude` to
list its available MCP tools:

```go
func TestRealClaudeMCPSmoke(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 (spends tokens) to run against the real claude binary")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}
	// go build -o <tmp>/mads github.com/drewkidwell/mads/cmd/mads
	// (mirror services/session/internal/runner/capability_test.go's
	// TestMain build step) so "mads internal orchestrate-mcp" is a real,
	// resolvable command — put <tmp> on PATH for this test only via
	// t.Setenv.
	p := New(Config{ExtraArgs: []string{"--dangerously-skip-permissions"}})
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP,
		Cwd:  t.TempDir(),
		MCPServers: []provider.MCPServerRef{{
			Name:    "orchestrate-smoke",
			Command: "mads internal orchestrate-mcp",
			Env:     []string{"MADS_ROUTER_URL=http://localhost:1", "MADS_SESSION_ID=smoke"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "What MCP tools do you have available? List their exact names."); err != nil {
		t.Fatal(err)
	}
	// collect through EventTurnEnd, assert the reply mentions "list_links"
	// (pkg/orchestratemcp/tools.go's tool name) — proves claude actually
	// saw the server via --mcp-config, not that a tool CALL succeeds
	// (MADS_ROUTER_URL is a dummy; a real call would fail, which is fine —
	// out of scope for this smoke).
}
```

## `providers/kirocli` — declare unsupported

kiro-cli's own README (`providers/kirocli/README.md`) already states it's
not installed in this environment and every claim is sourced from public
docs, not a live binary — the same honesty bar applies here: don't wire an
unverified mechanism. Nothing in kiro-cli's public docs/GitHub issues
surveyed for the rest of that README's matrix mentions an `--mcp-config`
-equivalent flag or file. Declare it unsupported rather than guess:

`providers/kirocli/kirocli.go` `Matrix()`: add
`provider.BehaviorMCPServers: provider.Unsupported,`.

No code change needed for graceful degradation — `kirocli.go`/`session.go`/
`pty.go` never reference `opts.MCPServers` today, so a session simply
starts without it, exactly the desired behavior (confirmed by grep: no
`MCPServers` references anywhere in `providers/kirocli`). The new
conformance probe (`c.mcpServers()` above) mechanically proves this stays
true — `TestConformanceAgainstScriptedBinary` in
`providers/kirocli/kirocli_test.go` will now also assert `Start` never
errors when `MCPServers` is set, with no test changes needed there.

Add a row to `providers/kirocli/README.md`'s matrix table:
```
| MCPServers | **Unsupported** | No documented flag or config mechanism thread an MCP server into kiro-cli's own launch was found in its public docs/GitHub issues (same citation bar as the rest of this table — kiro-cli is not installed in this environment). A session simply starts without the descriptor materialized; the L2c leader capability (docs/LINKED-SESSIONS-DESIGN.md item 5) never reaches a kiro-cli-backed agent. Revisit with `MADS_KIRO_SMOKE=1` against a real binary. |
```
Add one sentence to the "Net shape vs. claude-code" paragraph noting this
new asymmetry.

## `docs/LINKED-SESSIONS-DESIGN.md`
Update decomposition item 5's "**Not shipped**" parenthetical — it
currently says wiring `StartOptions.MCPServers` into a real provider is
not shipped. Once this lands, reword to something like: "claudecode now
threads `StartOptions.MCPServers` into a real `--mcp-config` launch
(providers/claudecode); kirocli declares the row unsupported
(providers/kirocli/README.md) pending a confirmed live mechanism."

## `providers/claudecode/README.md`
Add a matrix row and a short "MCP servers" section mirroring the existing
"Trust enforcement, transport by transport" section's style — mechanism
(`--mcp-config` real temp file + `--strict-mcp-config`), the
`exec.LookPath` absolute-path rationale, and a pointer to
`fakeclaude_test.go`'s `mcp-echo` dialect + `TestRealClaudeMCPSmoke`.

## Verification checklist for the executor

- `go build ./...` and `go vet ./...` clean.
- `go test ./providers/...` green — this exercises
  `TestConformanceAgainstScriptedBinary` for claudecode/kirocli/fake (new
  `mcp_servers` row probed automatically) plus the two new claudecode L2c
  tests.
- `go test ./providers/provider/...` green — `TestRequirementLevels`'
  updated count.
- `make check` green (whatever that wraps — confirm it includes the above;
  it's the task's own stated bar).
- No live LLM ever called by a required test — `MADS_CLAUDE_SMOKE`/
  `MADS_KIRO_SMOKE` stay opt-in, unset in CI.
- `git status` clean of stray temp `mads-mcp-*.json` files after a test
  run (proves the `Close()`-time cleanup actually fires) — the new tests'
  `defer sess.Close()` calls should already guarantee this; worth an
  explicit look during review since a leaked-temp-file bug is easy to
  write and easy to miss.

## Executor notes (this pass)

Implemented essentially as specced, with a few deviations worth flagging.

**Followed exactly**: `provider.go`/`matrix.go` doc + behavior row,
`fake.go`/`claudecode.go`/`kirocli.go` `Matrix()` rows, the `13 → 14`
count bump in `matrix_test.go`, `conformance.go`'s `mcpServers()` probe,
`mcp.go` (copied near-verbatim from the handoff's own snippet), the
`session.go`/`pty.go` wiring shape (build args after
`disallowedToolsArgs`, clean up the temp file on start failure and in
`Close()`), and `fakeclaude_test.go`'s `--mcp-config`/`--strict-mcp-config`
parsing plus the `"mcp-echo"` dialect in both `fcStream` and `fcTerminal`.

**Deviations from the handoff's sketch**:
- The two L2c proof tests are named `TestStartStreamMCPServersMatchesOrchestrateShape`
  / `TestStartPTYMCPServersMatchesOrchestrateShape` (the handoff's own
  suggested name had a typo, "...Shake"). Both use `orchestrateMCPRef()`,
  a shared helper returning the exact `setupOrchestrate` descriptor shape,
  rather than duplicating the literal in each test.
- **The handoff's assumption that `exec.LookPath("mads")` fails in this
  test process turned out to be false in this environment** — this repo's
  own `mads` binary is installed on this machine's `PATH`
  (`/home/drew/.local/bin/mads`), so the first version of
  `assertOrchestrateMCPEcho` (hardcoding `Command == "mads"`, unresolved)
  failed locally. Fixed by computing the expected command via the same
  `exec.LookPath("mads")` call the test asserts against, so the test is
  correct regardless of whether a `mads` binary happens to be on the
  machine running it — worth a reviewer glance since it means the
  resolved-absolute-path branch of `writeMCPConfig` is exercised on this
  machine and the unresolved-fallback branch would only be exercised on a
  clean CI box without `mads` on `PATH`. Neither branch's correctness is
  actually in doubt (both paths are simple), but the test's *coverage*
  of the fallback branch specifically is environment-dependent — not
  added a forced-failure case for it, since doing so cleanly needs either
  stubbing `exec.LookPath` (a small refactor, felt like scope creep for a
  test-only concern) or `PATH` manipulation via `t.Setenv` (viable but
  adds complexity to two already-long tests); flagging instead of fixing
  since the handoff didn't ask for that level of environment isolation.
- The PTY test's config-file JSON is followed immediately by the fake's
  next `"> "` prompt on the same write, so `json.Unmarshal` rejected it
  as trailing garbage; switched to `json.NewDecoder(...).Decode(&cfg)`,
  which stops after the first complete JSON value and ignores what
  follows. Reviewer: this is the one place this task's plumbing differs
  between the two transports' test assertions, worth a glance.
- Skipped the handoff's optional "additionally prove the *resolution*
  half of `writeMCPConfig`" suggestion (a second case using
  `os.Executable()` as `Command`) — the fallback-vs-resolved distinction
  above already exercises the resolved-path branch on this machine, and
  the task brief asks for the smallest change satisfying the acceptance
  criteria, not maximal coverage.
- `TestRealClaudeMCPSmoke` builds a real `mads` binary into a temp dir and
  prepends it to `PATH` via `t.Setenv` (mirroring
  `services/session/internal/runner/capability_test.go`'s `TestMain` build
  step, but scoped to just this one opt-in test rather than every test in
  the package, since only this test needs a real `mads`). Never run in
  this pass — opt-in, requires a real `claude` binary and spends tokens;
  reviewer should run it with `MADS_CLAUDE_SMOKE=1` at least once before
  relying on it.

**Verification performed**: `go build ./...`, `go vet ./...`, `go test
-race ./providers/...` (all green), and `make check` (lint + `go test
-race ./...` + UI lint/test, all green — 184 test files / 835 tests
passed on the UI side, untouched by this change). Confirmed no stray
`mads-mcp-*.json` temp files survive a test run.

**Not done / out of scope**: `MADS_CLAUDE_SMOKE=1` was not actually run
(no tokens spent, per the "never a live LLM in required tests"
convention — this is the opt-in smoke, intentionally left for a human or
a separate gated CI job). `services/session/internal/runner` was not
touched, per the handoff's own scoping note — its existing
`orchestrate_test.go` against `providers/fake` remains the sole proof of
the grant-gating logic.
