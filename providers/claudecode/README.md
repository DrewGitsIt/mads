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
| ModelDiscovery | **Emulated** | No discovery call exists; `DefaultModels` is a pinned list this adapter maintains. |
| CostReporting | **Native** | Both `-p --output-format json` and the stream-json `result` line report `total_cost_usd` and a nested `usage` object with token counts. |
| ACP | **Native** | The stream-json structured transport (this package's name for its ACP-equivalent event stream). |
| PTY | **Native** | claude-code's own interactive terminal UI runs directly on a pseudo-terminal. |
| Branching | **Unsupported** | No flag or API forks a session into alternatives. |
| CwdReroot | **Unsupported** | No flag or API moves a live session to a different working directory. |
| TrustLimited | **Native** | `--disallowedTools Bash` removes command execution from the model's tool palette entirely — confirmed against `claude --help`: `--disallowedTools, --disallowed-tools <tools...>` takes "a comma or space-separated list of tool names to deny". `Write`/`Edit` stay available. |
| TrustReadOnly | **Native** | `--disallowedTools Bash,Write,Edit,NotebookEdit` denies both command execution and every file-mutating tool. |

## Trust enforcement, transport by transport

`--disallowedTools` is a single CLI-wide flag, not headless-only — both
`startStream()` (structured transport) and `startPTY()` (terminal transport)
append the identical mapping via the shared `disallowedToolsArgs()` helper.
Unlike kirocli, there is no PTY-vs-ACP asymmetry here: whichever transport a
session uses, the disallowed tools are genuinely absent from what the model
can call, not merely intercepted at approval time.

`TrustFull` (and the zero value) appends nothing — today's unrestricted
behavior is unchanged.

**Caveat:** this row is asserted from `claude --help`'s documented flag
description in this environment, not from a scripted-vs-live divergence test
— there is no automated check that a disallowed tool is truly unreachable
rather than merely unlisted. If a future `MADS_CLAUDE_SMOKE=1` run surfaces
daylight between "flag passed" and "tool actually unavailable," downgrade
both trust rows to `Emulated` and cite the finding here.

## Pipeline vs. interactive registries

`cmd/mads/run.go` (pipeline one-shots) and `services/session/main.go`
(interactive sessions) construct two separate `claudecode.Provider`
instances with independent `Config.ExtraArgs`. The pipeline's
`-claude-args` flag supplies `--dangerously-skip-permissions` for autonomous
agents and is untouched by trust enforcement — `OneShot` has no `Trust`
field and pipeline one-shots never call `Start`. Trust only ever reaches
`startStream`/`startPTY`, which only the interactive registry drives.

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
