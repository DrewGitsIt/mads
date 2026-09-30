# providers/claudecode

The reference provider adapter (spec §11): Claude Code driven through its
`stream-json` interface for interactive sessions, `claude -p` for one-shots,
`--resume` for reattach, and the on-disk project transcripts for normalized
history.

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | `claude -p --input-format stream-json --output-format stream-json` is a real, documented persistent structured session. |
| Interrupt | **Emulated** | Rides a stream-json `control_request` (`subtype: interrupt`) rather than a first-class harness API — the turn ends, the process survives, but it is a protocol convention layered on top of the stream, not a dedicated call. |
| OneShot | **Native** | `claude -p "<prompt>" --output-format json` is the documented non-interactive invocation. |
| Resume | **Native** | `--resume <session-id>` is a real, documented flag on both transports. |
| Transcript | **Native** | Claude Code writes project transcripts to `~/.claude/projects/<project>/<session-id>.jsonl`; this adapter reads them directly. |
| ModelDiscovery | **Native** | `list_models` over stream-json returns the harness's own catalog (`discover.go`): one control request, no turn, no LLM call, ~1s. Aliases (`opus`, `default`) are kept as the selectable id so the picker tracks the latest model without mads re-pinning. `Config.Models` pins the list instead when set; otherwise a failed probe wraps `ErrDiscoveryUnavailable` rather than falling back to a stale list. |
| CostReporting | **Native** | Both `-p --output-format json` and the stream-json `result` line report `total_cost_usd` and a nested `usage` object with token counts. |
| ACP | **Native** | The stream-json structured transport (this package's name for its ACP-equivalent event stream). |
| PTY | **Native** | claude-code's own interactive terminal UI runs directly on a pseudo-terminal. |
| Branching | **Unsupported** | No flag or API forks a session into alternatives. |
| CwdReroot | **Unsupported** | No flag or API moves a live session to a different working directory. |
| TrustLimited | **Native** | `--disallowedTools Bash` removes command execution from the model's tool palette entirely — confirmed against `claude --help`: `--disallowedTools, --disallowed-tools <tools...>` takes "a comma or space-separated list of tool names to deny". `Write`/`Edit` stay available, and asking before those runs through the permission-prompt hook below on the chat transport. |
| TrustReadOnly | **Native** | `--disallowedTools Bash,Write,Edit,NotebookEdit` denies both command execution and every file-mutating tool; whatever narrow set remains still asks via the hook below on chat. |
| MCPServers | **Native** | `--mcp-config <file> --strict-mcp-config` threads `StartOptions.MCPServers` into a real launch on both transports — see "MCP servers" below. |
| RefAnnounce | **Emulated** | The structured transport's `system`/`init` line carries `session_id` at connect, before any turn (Native-strength). The terminal transport prints nothing, and a structured session's own announcement can itself be dropped or delayed past its window; either way the Runner falls back to `LocateSession` (`providers/provider.SessionLocator`), a weaker best-effort mechanism — see "Ref announcement, transport by transport" below. Emulated is the honest one-row summary across both. |

## Ref announcement, transport by transport

`BehaviorRefAnnounce` lets the runner persist a session's provider-native
ref the moment it's known, instead of only at turn end — the defense
against a crash losing an unresumable session. Two independent mechanisms
feed it:

**Wire announcement (primary, `providers/provider.RefAnnouncer`):**
`startStream()`'s `system`/`init` line already carries `session_id` before
any user turn is sent (confirmed against the scripted stand-in's
`fcStream`, which emits it immediately on connect, mirroring real
claude-code 2.1.207 behavior). `handleLine` fires `streamSession.announced`
the first time `session_id` is seen, which for a fresh session is on that
very first line. The terminal transport has no equivalent — `ptySession`
does not implement `RefAnnouncer` at all.

**Content-correlation backstop (`providers/provider.SessionLocator`,
`locate.go`):** the Runner calls `LocateSession(ctx, cwd, spawnedAfter,
firstMessage)` with the exact first message it sent whenever the wire
announcement never arrives — always for a PTY session, and also for a
structured session whose announcement is dropped or delayed past its
window. `LocateSession` retries two rungs on a short interval until `ctx`
is done:

1. **Rung 1 — `locateFromHistory`:** scan claude-code's own append-only
   prompt index, `Config.HistoryFile` (default `~/.claude/history.jsonl`),
   for the one entry whose `project` matches `cwd`, whose `timestamp` is no
   earlier than `spawnedAfter` (minus a small clock-skew margin), and whose
   `display` corresponds to `firstMessage` (exact, or a prefix match in
   either direction to tolerate claude's pasted-content placeholder
   mangling). A history scan is far cheaper than walking every transcript
   file, so it runs first whenever the index has a usable entry. Its result
   is never trusted on its own — `verifyRef` confirms it names exactly one
   real transcript under `TranscriptDir` with a matching `cwd` before
   `LocateSession` returns it.
2. **Rung 2 — `discoverRefByContent`:** the fallback when the index has no
   usable entry (or is unavailable). `candidateTranscripts` filters every
   transcript under `TranscriptDir` to those modified after `spawnedAfter`
   (same clock-skew margin) whose stamped `cwd` matches; `discoverRefByContent`
   then narrows those survivors to the one whose first user-role message is
   exactly `firstMessage`.

Both rungs decline (rather than guess) on zero or multiple matches — a
wrong ref would silently resume someone else's conversation, which is worse
than no ref. If neither rung ever resolves before the caller's `ctx` is
done, the ref is simply never discovered — logged, never surfaced to the
user as an error, and the session works exactly as it did before this
mechanism existed (resume-less until reattached with an explicit ref).

## Trust enforcement, transport by transport

Both `startStream()` (structured transport) and `startPTY()` (terminal
transport) append the identical mapping for a given `StartOptions.Trust` via
the shared `trustArgs()` helper. Unlike kirocli, there is no PTY-vs-ACP
asymmetry here: whichever transport a session uses, the same flags reach
claude.

| Trust | Flag(s) | Effect |
|---|---|---|
| `TrustLimited` | `--disallowedTools Bash` (+ `--settings` hook on chat, see below) | Command execution is genuinely absent from the model's tool palette, not merely intercepted at approval time. `Write`/`Edit` stay available and still ask — answerable via the chat transport's permission card. |
| `TrustReadOnly` | `--disallowedTools Bash,Write,Edit,NotebookEdit` (+ `--settings` hook on chat, see below) | Command execution and every file-mutating tool are absent. Whatever narrow set of tools remains still asks, answerable the same way. |
| `TrustFull` (and the zero value) | `--permission-mode bypassPermissions` | claude never enters its default ask-first flow at all — no hook is wired, since there is never a prompt to answer. |

`--disallowedTools`'s effect is confirmed against `claude --help` in this
environment: `--disallowedTools, --disallowed-tools <tools...>` takes "a
comma or space-separated list of tool names to deny".

**Why `TrustFull` needed a flag at all:** claude's *default* permission mode
asks before every tool call, on both transports. That's fine when a human is
sitting at the terminal to answer, but chat mode (the structured transport)
has no surface to answer a permission prompt at all — the request just sits
there. Before this fix, `TrustFull` (meant to be "no restrictions, no
prompts") passed no permission-related flag, so a full-trust chat session
silently stalled on the CLI's own interactive prompt instead of running the
tool. `--permission-mode bypassPermissions` makes claude skip that flow
entirely, which is what "Auto" trust is supposed to mean on both transports.

**Why `--permission-mode bypassPermissions` and not
`--dangerously-skip-permissions`:** both flags skip the same prompt, but
`--dangerously-skip-permissions` is reserved for the separate pipeline
engine's own autonomous-agent invocation (`cmd/mads/run.go`'s
`-claude-args`, see "Pipeline vs. interactive registries" below) and is left
untouched by this change. `--permission-mode bypassPermissions` is the
CLI's documented interactive-session equivalent (claude 2.1.x); using a
distinct flag for the interactive path keeps the two invocation sites
independently greppable and avoids conflating "an autonomous pipeline stage
configured its own agent" with "a human set the trust pill to Auto."

**Caveat:** the exact-flag claims above are asserted from `claude --help`'s
documented flag descriptions in this environment, not from a
scripted-vs-live divergence test — there is no automated check that a
disallowed tool is truly unreachable rather than merely unlisted, or that
`bypassPermissions` truly suppresses every prompt shape claude can produce.
If a future `MADS_CLAUDE_SMOKE=1` run surfaces daylight between "flag
passed" and actual behavior, downgrade the affected row to `Emulated` and
cite the finding here.

### Defense in depth: a blocked tool call must never be silent

Even with the flag above, a permission-shaped stall is still possible (a
custom permission hook, an unrecognized prompt shape, a future CLI change)
— so the live event stream never silently drops a tool's outcome.
`wireLine.events()` (`wire.go`) now handles the `"user"`-type wire line the
same way `normalizeTranscript` (`transcript.go`) already handled it for the
on-disk/polled path: each `tool_result` block becomes a
`provider.EventToolResult`, carrying the correlating `Tool.ID`, the result
text, and `IsError`. Previously this line type fell into `events()`'s
`default:` case and was dropped entirely on the live stream — a permission
denial (or any other tool failure) was invisible until the next transcript
poll caught up, or never, if the process was genuinely hung awaiting an
unanswerable prompt. The fixture at `testdata/transcript_denial.jsonl` is a
real captured session with exactly this shape (a denied `Read`, `is_error:
true`, followed by a successful `Bash` call); `TestWireEventsSurfacesToolResultDenial`
pins `events()`'s live parsing of it, alongside
`TestTranscriptNormalizationDenialGolden`'s existing coverage of the
on-disk path.

The frontend renders an `EventToolResult` with `is_error: true` as the
error state on its own tool card (claude reports *every* ordinary tool
failure this way — a non-zero exit, a missing file — so it must not be
treated as a permission signal). The "blocked awaiting permission" banner is
driven exclusively by `EventPermissionRequest` (the hook below), which
carries the parked tool's name and clears itself when the request resolves —
see `ui/src/conversation/ConversationTab.tsx` and `ChatRenderer/`.

### Answering the prompt itself: the permission hook (chat transport only)

`TrustLimited`/`TrustReadOnly` leave whichever tools survive `--disallowedTools`
subject to claude's default ask-before-every-call behavior. On the terminal
transport (`startPTY`) that prompt is claude's own native TUI — a human is
sitting there, so nothing further is needed. On the chat/structured transport
(`startStream`) there is no such surface at all, which is exactly what the
"Defense in depth" section above exists to make visible rather than silent.

This adapter closes that gap with a **PreToolUse command hook**, not an MCP
tool — `claude` has no MCP-based permission-delegation flag (`--help` has
never listed one; see `services/session/HANDOFF.md` for the verification
trail of two earlier attempts that assumed one existed). `--settings
<file>` does exist and is documented to load hook configuration at session
start, in the "direct" (non-plugin) shape:

```json
{"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "...", "timeout": 660}]}]}
```

`permission.go`'s `permissionArgs` builds this file — mirroring `mcp.go`'s
`writeMCPConfig`/`--mcp-config` temp-file pattern — only when
`needsPermissionHook(opts.Trust)` is true (`TrustLimited`/`TrustReadOnly`)
**and** `opts.PermissionPromptURL` is set. `startStream` is the only call
site; `startPTY` never sees this option, so terminal-mode behavior is
unchanged. The Runner (`services/session/internal/runner`) is what sets
`opts.PermissionPromptURL`, and only for the chat transport — see its own
docs for the request/response contract, the in-memory pending-request
registry, and the ~10-minute deny-on-timeout bound. The hook command itself
is a single `curl -d @-` invocation (no Go subcommand, no script file —
`pkg/githook`'s bare-shell-script precedent) that pipes its stdin straight
through as the POST body and prints the response straight back to stdout;
the session service's handler pre-formats the exact
`{"hookSpecificOutput": ...}` JSON claude expects, so the shell command
never itself parses or builds JSON. A `curl` failure (connection refused,
its own `--max-time` expiry) falls back to an inline deny rather than
leaving claude to interpret empty/malformed stdout — three layered
timeouts (the hook's own `"timeout"`, curl's `--max-time`, and the session
service's own bound) each shorter than the one before it, so the
server-side bound always wins the race and the agent process never hangs
indefinitely on an unanswered prompt.

## MCP servers

`StartOptions.MCPServers` (the L2c leader capability's orchestrate server,
`docs/LINKED-SESSIONS-DESIGN.md` item 5) threads into claude's own
documented `--mcp-config <file>` flag — a real temp JSON file
(`mcp.go`'s `writeMCPConfig`), not an inline string, and not a `.mcp.json`
dropped into the session's cwd. `claude mcp list`/`get` show project-scoped
`.mcp.json` servers sitting "⏸ Pending approval" until a human confirms
them interactively; a headless `stream-json` session has nobody to do
that, so a `.mcp.json` server would hang forever. `--mcp-config`-supplied
servers are the CLI's own mechanism for exactly this headless case and
skip that gate. `--strict-mcp-config` is passed alongside it whenever
`opts.MCPServers` is non-empty, so an ambient project `.mcp.json` can never
silently attach and hang the session on approval.

`claude` spawns the MCP server subprocess itself, from the session's own
cwd (a worktree) — not this adapter process's cwd. `writeMCPConfig`
resolves each server's command to an absolute path via `exec.LookPath`
before writing the config, so it survives being spawned from a different
directory; a `LookPath` failure falls back to the raw token rather than
erroring, since one capability's misconfiguration must never block the
whole session launch. Both transports (`startStream`/`startPTY`) build the
same args before `p.cfg.ExtraArgs` and remove the temp config file on
`Close()` and on process-start failure.

`fakeclaude_test.go`'s `"mcp-echo"` dialect reads back the `--mcp-config`
file's exact contents and the `--strict-mcp-config` flag, proving the
adapter's launch args/config carry a server descriptor unmodified
(`TestStartStreamMCPServersMatchesOrchestrateShape`,
`TestStartPTYMCPServersMatchesOrchestrateShape`). `TestRealClaudeMCPSmoke`
is the opt-in real-binary counterpart:

```sh
MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run MCPSmoke
```

A live stream session can also change its servers without a restart:
`streamSession.SetMCPServers` (`provider.MCPHotSetter`) sends the
`mcp_set_servers` control request, which replaces claude's *dynamic*
server set. Tools reach the model from the next turn on the same
conversation. It can add servers but not remove ones passed at launch;
that case returns `provider.ErrMCPHotSetUnsupported`, and the Runner
restarts the process instead. The PTY transport doesn't implement it.
Findings and probes are in `docs/research/claude-mcp-hot-mount.md`, and
the real-binary check is `TestRealClaudeMCPHotSetSmoke` (same
`MADS_CLAUDE_SMOKE=1` gate, `-run MCPHotSetSmoke`).

Note: declaring an MCP server in an agent's own capabilities
(`agent.Capabilities.MCPServers` / `setupMCP` in `capability.go`) is a
separate, still-unwired path — those servers run as health-checked
subprocesses only, never connected to a provider's own launch by any
existing mechanism. This section covers `StartOptions.MCPServers` only.

## Pipeline vs. interactive registries

`cmd/mads/run.go` (pipeline one-shots) and `services/session/main.go`
(interactive sessions) construct two separate `claudecode.Provider`
instances with independent `Config.ExtraArgs`. The pipeline's
`-claude-args` flag supplies `--dangerously-skip-permissions` for autonomous
agents and is untouched by trust enforcement — `OneShot` has no `Trust`
field and pipeline one-shots never call `Start`. Trust only ever reaches
`startStream`/`startPTY`, which only the interactive registry drives.

## Complete: hermetic one-shot completion (Fast Model)

`Complete` (`complete.go`) implements `provider.Completer`, the optional
capability backing Fast Model's zero-config "use my provider" resolution
(`providers/fastmodel`) — stateless, no session left behind, no tools, no
conversation. Unlike `OneShot`, which is session/tool-shaped and runs in
`req.Cwd`, `Complete` runs in a fresh scratch directory with every flag
below verified against the installed `claude` CLI first (see
`testdata/complete_flags.md` — two prior tasks in this repo died on
fabricated flags):

- a throwaway `os.MkdirTemp` cwd, never the workspace — kills CLAUDE.md/
  AGENTS.md discovery and project settings, and any transcript this leaves
  behind lands under a throwaway project slug;
- `--safe-mode`, disabling CLAUDE.md, hooks, MCP, skills, plugins, and
  custom agents while leaving auth, model selection, built-in tools, and
  permissions working normally (`--bare` was rejected — it forces
  `ANTHROPIC_API_KEY`-only auth, silently breaking OAuth/keychain users);
- `--setting-sources ""` plus a per-call `--settings` file with no hooks/
  preferences, so `~/.claude/settings.json` never loads;
- `--strict-mcp-config --mcp-config '{"mcpServers":{}}'`, so no MCP server
  (including an ambient project `.mcp.json`) attaches;
- `--tools ""` (every tool denied);
- an explicit `--model`, defaulting to `DefaultCompleteModel`
  (`claude-haiku-4-5-20251001`) but overridable per call.

JSON validation and the single bounded repair-retry are deliberately absent
here — they live in the `fastmodel` layer only, never per-provider, so
every backend (this one, the OpenAI-compatible endpoint backend) is held to
the same retry contract.

Hermeticity is asserted, not assumed: `TestRealClaudeCompleteHermeticity`
(`complete_test.go`, opt-in, `MADS_CLAUDE_SMOKE=1`) plants a `CLAUDE.md`, a
`SessionStart` hook, and a decoy `.mcp.json` in the surrounding directory,
then asserts none of it is observable in the reply and that
`usage.input_tokens` falls within the documented overhead budget
(`testdata/complete_flags.md`: ~154-165 tokens above the constructed
prompt+system length) — if a future CLI update starts injecting ambient
context, this test fails loudly rather than silently degrading.

**Latency:** a `Complete` call pays claude-code's own process-spawn cost
(roughly 1-2s), on top of whatever the model itself takes. That's
acceptable because every Fast Model job (`providers/fastmodel`) is
debounced or one-off background work — task synthesis, summaries, retro
steps, NL agent generation — never a request blocking on a human's typed
input. Interactive consumers that need lower latency should configure the
OpenAI-compatible endpoint backend (`fastmodel.DynamicConfig.BaseURL`)
instead of relying on the zero-config provider default.

## Testing without claude installed

`fakeclaude_test.go` re-execs the test binary as a scripted stream-json
stand-in (`MADS_FAKE_CLAUDE=1`) — real subprocess spawn, real stream-json
parsing. `providers/conformance`'s full suite
(`TestConformanceAgainstScriptedBinary`) runs against that stand-in in CI —
no claude install required, and no live LLM is ever called.

`TestRealClaudeSmoke` is opt-in and exercises the actual binary:

```sh
MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run Smoke
```

It skips cleanly if `claude` isn't on `PATH`.
