# hermes 0.22.2 captures

Evidence for the hermescli knob values, the session wrapper's corrections
and the TranscriptReader's rules. Line citations in the code
("runc1 line 4") are 1-based lines of these files; "row N" is a
`messages.id` in the matching `.db.jsonl`.

## Provenance

Captured 2026-10-20 against Hermes Agent 0.22.2 (`hermes-acp --version`,
git install at `~/.hermes/hermes-agent`, d923a5eb487a) with a ~150-line
Python ACP client that logs every stdio line with a millisecond timestamp
(`"dir": "out"` = client → agent, `"in"` = agent → client, `"note"` = the
client's own step markers) and polls `$HERMES_HOME/state.db` read-only
every 25 ms (`.db.jsonl`: one entry per row insert/update/delete).

Every run used a scratch `HERMES_HOME` — never `~/.hermes`:

- `run1`, `run2`, `run3`, `run9`: Ollama `qwen3.8-coder-nothink` through
  hermes's `custom` provider (scratch `config.yaml` naming only the model
  and base URL).
- `runc1`: GitHub Copilot `gpt-4.1` (scratch `config.yaml` naming only
  `provider: copilot`; hermes resolved the credential itself from
  `gh auth token` and recorded a fingerprint, no secret).
- `probe-*`: Copilot `gpt-4.1`/`gpt-4o`, written for this adapter;
  `probe-mcp` used a 20-line stdio MCP server (the same one
  `smoke_test.go` embeds).

## Files

| File | What it pins |
|---|---|
| `runc1.wire.jsonl` | initialize capabilities (line 2); session/new models selector + modes, no configOptions (line 4); unsolicited `available_commands_update` / `session_info_update` (lines 8, 10); kind-based tool call titled `terminal: ls` under a live `tc-…` id (lines 21-22); cumulative prompt-result usage (lines 18, 40, 87, 323 — the cancelled turn repeats the previous totals); cancel resolving `cancelled` (lines 321-323) |
| `runc1.db.jsonl` | the stored rows: whitespace-stripped user rows, `call_…` tool ids (row 6), the interrupted partial (row 12), the post-cancel rewrite `<cancelled>\n\nUser correction/guidance after interrupt: <new>` (row 13) |
| `run1.wire.jsonl` | the `"\n\n"` hermes prepends to the first text after a tool (live 58 chars vs stored 56) |
| `run2.wire.jsonl`, `run2.db.jsonl` | session/load replay, then a cancel before any output: the synthetic `failed_turn` row (row 20) and the rewritten next prompt (row 21); usage restarting at 0 in a new process |
| `run3.db.jsonl` | a load deleting and reinserting the active rows (new ids, same `message_uid`); a failed-turn notice stored without its `display_kind` when the process died first (row 40) |
| `run9.wire.jsonl` | the edit ask under the synthetic id `edit-approval-1`, options `allow_once`/`deny`, and the stray `tool_call_update` for that id (lines 12-16); `rm -rf` auto-approved by hermes's own reviewer with no ask (lines 23-24) |
| `probe-setmodel.wire.jsonl` | `session/set_model` `copilot:gpt-4o` succeeding; `config.yaml` sha256 and mtime identical before and after (the probe's stdout, recorded in the design doc); the session row stores the model |
| `probe-load.wire.jsonl` | reloading that session restores `currentModelId` `copilot:gpt-4o` — the choice lives on the session, not in config |
| `probe-loadunknown.wire.jsonl` | session/load of an unknown id answered `{"result": {}}`, and a prompt on it `stopReason: "refusal"` |
| `probe-mcp.wire.jsonl` | a stdio MCP server passed in session/new's `mcpServers`: the model finds it (`tool_search`), calls `mcp__madsprobe__mads_probe_ping`, and replies with the marker only the tool returns |
| `probe-emptynew.wire.jsonl` | session/new with no prompt leaves no `sessions` row (model discovery deposits nothing) |

## Sanitization

Nothing needed removing: the runs carried no account identifiers, keys or
tokens (checked for home paths, emails, `gh*_`/`sk-` prefixes and
`api_key`). Paths are scratch `/tmp` directories.
