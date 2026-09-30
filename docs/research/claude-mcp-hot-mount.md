# Can claude hot-mount MCP servers mid-session? (spike, 2026-09-30)

**Verdict: yes.** claude-code 2.1.283's stream-json control channel has an
`mcp_set_servers` request. It replaces the process's *dynamic* MCP server
set: the new servers connect right away, and their tools reach the model
from the next turn, on the same conversation, with no restart. mads now
uses it to activate an orchestrate grant (`provider.MCPHotSetter`,
implemented by claude-code's structured transport). The provider-process
cycle from `RefreshOrchestrate` remains as the fallback.

## What exists

Found in the installed binary (`~/.local/share/claude/versions/2.1.283`,
via `strings`). These are the MCP-related `control_request` subtypes and
the schema text the CLI ships with them:

| subtype | schema description |
|---|---|
| `mcp_status` | lists servers: name, status, config, scope/source, and tools |
| `mcp_set_servers` | "Replaces the set of dynamically managed MCP servers." Result: `{added, removed, errors}` |
| `mcp_toggle` | "Enables or disables an MCP server." (`serverName`, `enabled`) |
| `mcp_reconnect` | "Reconnects a disconnected or failed MCP server." |
| `mcp_message` | carries JSON-RPC for an SDK-hosted (in-process) server |

These sit beside the two our adapter already used: `interrupt`
(session.go) and `list_models` (discover.go). Related strings show the
same machinery serves the Agent SDK's `setMcpServers` ("added later with
mcp_set_servers"). The error texts are specific, e.g. "mcp_set_servers:
servers must be an object of config objects".

## What I tried

Every run used the adapter's hermetic launch (`-p --input-format
stream-json --output-format stream-json --verbose --strict-mcp-config
--mcp-config '{"mcpServers":{}}'`) with `CLAUDE*` env vars stripped. The
server was the real `bin/mads internal orchestrate-mcp`.

1. **Control only, no turn (free).** `mcp_status` returned
   `{"mcpServers": []}`. Then `mcp_set_servers` with
   `{"servers": {"orchestrate-probe": {"type": "stdio", "command":
   ".../bin/mads", "args": ["internal", "orchestrate-mcp"], "env": {...}}}}`
   returned `{"added": ["orchestrate-probe"], "removed": [], "errors": {}}`.
   A second `mcp_status` showed it `"status": "connected"`, `"scope":
   "dynamic"`, with all six tools (`list_links`, `get_summary`,
   `get_turns`, `send_message`, `check_mailbox`, `set_attention`). Setting
   `{}` removed it (`"removed": ["orchestrate-probe"]`).
2. **Before the first turn (haiku, $0.023).** Mounted, then asked for
   every `mcp__` tool. The turn's init line listed
   `mcp__orchestrate-probe__*` (all six), and the model named them all.
3. **Mid-session, the case that matters (haiku, $0.014 for turn 2).** Turn
   1 had no `mcp__` tools, and the model answered NONE. Then
   `mcp_set_servers`. Turn 2, same process and same `session_id`
   (`d13e2c93-…`), had all six tools, and the model named them.
4. **Through our adapter.** `TestRealClaudeMCPHotSetSmoke`
   (`MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run
   MCPHotSetSmoke`) runs the same mid-session sequence through
   `streamSession.SetMCPServers`. It passes, and the session ref is
   unchanged.

## Limits, and how the implementation handles them

- **Only the dynamic set.** Servers passed at launch (`--mcp-config`)
  aren't in it, so `mcp_set_servers` can add servers but not remove those.
  `SetMCPServers` sends only the servers beyond the launch set. It returns
  `provider.ErrMCPHotSetUnsupported` when a change would drop a launch-time
  server, and the runner cycles the process for that case.
- **Stream transport only.** The terminal (PTY) transport has no control
  channel, so its session doesn't implement `MCPHotSetter` and cycles as
  before.
- **Idle only, by choice.** I verified a set *between* turns. Whether a set
  landing mid-turn is safe wasn't tested, so the runner applies it only
  when idle (turn over, nothing queued). Otherwise it defers exactly like
  the cycle does.
- **Process lifetime.** The dynamic set dies with the process. The runner
  records the new set in `baseOpts.MCPServers`, so any later relaunch (a
  model or transport switch) passes it via `--mcp-config`.
- **Version.** Verified on 2.1.283 only. An older binary that doesn't know
  the subtype won't answer successfully (or at all). That errors or times
  out after 20s, and the runner falls back to the cycle.
