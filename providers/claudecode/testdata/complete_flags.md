# Complete flag verification (claude CLI v2.1.215)

Every flag `Complete` (complete.go) passes was confirmed against a real
installed `claude` binary before use — two prior tasks in this repo died on
fabricated flags (`--permission-prompt-tool` never existed). Method: read
`claude --help` in full, then run live invocations in a scratch `mktemp -d`
directory with a planted `CLAUDE.md`, a `.claude/settings.json` SessionStart
hook, and a `.mcp.json`, asking the model to reveal any injected codeword.

## Verified flags in use

- `--safe-mode` — help text: disables CLAUDE.md, hooks, MCP, skills,
  plugins, custom agents; leaves auth, model selection, built-in tools, and
  permissions working normally.
- `--setting-sources <sources>` — comma-separated settings sources to load;
  `""` loads none.
- `--settings <file-or-json>` — loads an additional settings file/JSON blob.
- `--strict-mcp-config` — use only the MCP servers given via `--mcp-config`,
  ignoring any ambient project `.mcp.json`.
- `--mcp-config <configs...>` — "Load MCP servers from JSON files or
  strings" (help text explicitly allows an inline string, not just a path).
- `--tools <tools...>` — `""` disables all tools.
- `--system-prompt <prompt>` — full replacement of claude's default system
  prompt (as opposed to `--append-system-prompt`, which adds to it).
- `--model <model>`, `--output-format json` — already used by OneShot.

## Ruled out

- `--bare` — help text says it forces `ANTHROPIC_API_KEY`/`apiKeyHelper`-only
  auth. Rejected: would silently break Fast Model for OAuth/keychain-authed
  claude-code users, contradicting "today claude-code... zero-config."
- `--json-schema <schema>` — worked, but added an internal `tool_use`
  round-trip (`stop_reason: "tool_use"`, `num_turns: 2`) and much higher,
  less predictable `input_tokens`. Rejected: JSON validation belongs in the
  fastmodel layer only, and a deterministic token-overhead budget is needed
  for the hermeticity test.
- Overriding `$HOME` for the subprocess — breaks OAuth: credentials live at
  `~/.claude/.credentials.json` under the *real* `$HOME`. A fake `$HOME`
  produced `"Not logged in · Please run /login"`. Hermeticity here comes
  from a fresh scratch **cwd** + flags only, never from `$HOME`.
- `--max-budget-usd <amount>` — a dollar cap, not a token cap; no flag
  enforces `CompleteRequest.MaxOutputTokens` directly, so it's left
  unenforced (best-effort) by this provider.

## Live experiments (control + calibration)

All runs: scratch cwd containing a planted `CLAUDE.md` (marker
`PURPLE_ELEPHANT`), `.claude/settings.json` SessionStart hook injecting
`ORANGE_GIRAFFE`, and a decoy `.mcp.json`. Prompt asked the model to reveal
any injected codeword, or reply `pong` for the calibration runs.

| Flags                                                                                                                                          | Result                                                                 |
|--------------------------------------------------------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------|
| (none — control)                                                                                                                                 | `PURPLE_ELEPHANT` leaked into the reply; `cache_creation_input_tokens: 9287` |
| `--safe-mode`                                                                                                                                     | No codeword — CLAUDE.md/hook excluded                                   |
| `--safe-mode --tools ""`                                                                                                                          | `usage.input_tokens: 3298`, no cache_creation                          |
| `--safe-mode --tools "" --system-prompt "You are a helpful assistant."`                                                                            | `usage.input_tokens: 181`                                              |
| `--safe-mode --tools "" --setting-sources "" --strict-mcp-config --mcp-config '{"mcpServers":{}}' --system-prompt "You are a test harness." -p "reply with exactly: pong"` (run twice) | `usage.input_tokens: 165` both times, identical                        |
| same, plus `--json-schema`                                                                                                                        | `usage.input_tokens: 914`, `num_turns: 2`, `stop_reason: "tool_use"`    |

## Hermeticity budget

The full flag set Complete uses produces a **fixed overhead of ~154-165
tokens** above the constructed prompt+system text length, stable across
repeated identical calls, even with planted ambient files present in the
surrounding cwd (they're excluded, not merely small). The hermeticity
integration test (complete_test.go, `MADS_CLAUDE_SMOKE=1`) asserts
`input_tokens` falls within a tolerance window around
`len(prompt)+len(system)` plus this documented overhead, and separately
asserts none of the planted markers appear in the reply.
