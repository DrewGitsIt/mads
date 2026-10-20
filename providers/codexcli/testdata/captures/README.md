# codex 0.154.0 app-server captures

Real `codex app-server` stdout, trimmed and scrubbed, pinning every wire
claim in this package. Model gpt-5.6-luna at low reasoning effort.

| File | What | Source |
|---|---|---|
| `run1-stdout.jsonl` | Five turns on one thread: plain reply, a commentary + `ls` command + final answer, a markdown answer, a web search, and a story interrupted mid-stream (`turn/interrupt` → `turn/completed` status `interrupted`; the partial agentMessage never completes). | Provider survey run1, 2026-10-20 (`/tmp/mads-provider-survey/codex/run1/stdout.ts.jsonl`), driven by the survey's `drive.py`: `thread/start` with `sandbox: workspace-write`, `approvalPolicy: never`. |
| `run1-items-list.json` | `thread/items/list` for that thread after the five turns: the same item ids the live stream carried; no injected context; no partial text for the interrupted turn. | Survey run1 `thread_items_list.json` (the `result` member). |
| `run3-resume-items-list.json` | `thread/items/list` for a thread whose app-server was killed (`kill -9`) mid-command, then resumed: the in-flight command item is gone. | Survey run3-resume. |
| `limited-decline.jsonl` | Limited trust (`workspace-write` + `untrusted`): `touch a.txt` asks (`item/started` commandExecution, then `item/commandExecution/requestApproval` with that item id); the client declines; the item completes `declined`; the model retries once, declined again. | Probe 2026-10-20 (P2d), ephemeral thread, client answered `{"decision":"decline"}`. |
| `limited-filechange.jsonl` | Limited trust: an apply_patch add asks (`item/fileChange/requestApproval`); accepted; the item completes `completed`. | Probe 2026-10-20 (P2d), client answered `{"decision":"accept"}`. |
| `steer-compact.jsonl` | `turn/steer` mid-turn (one more userMessage item, same turn id, one `turn/completed`), then `thread/compact/start` (its own turn: a `contextCompaction` item, `tokenUsage.last.totalTokens` 4900). | Probe 2026-10-20 (P2d), read-only sandbox, ephemeral thread. |

## Trimming and scrubbing

- Only the server's lines (stdout). `remoteControl/status/changed`,
  `turn/moderationMetadata`, `account/rateLimits/updated`,
  `mcpServer/startupStatus/updated` and `deprecationNotice` dropped; so are
  the `thread/read` / `thread/items/list` responses inside the streams
  (the items list has its own file).
- Home directory → `/Users/user`, hostname → `host.local`, installation id
  zeroed, workspace paths → `/tmp/work`. Thread, turn and item ids are
  codex's random ids, kept so live/stored correlation stays checkable.
- The P2d probe logs printed lines through a truncating viewer: any line
  that no longer parsed (one `thread/start` response) was dropped, and the
  agentMessage deltas were filtered out of those three files (their
  completed items carry the text).

## Probes behind the adapter's other decisions (no file kept)

- **Trust persistence**: `thread/start` with `sandbox` `workspace-write` or
  `danger-full-access` appends `[projects."<realpath or git root>"]
  trust_level = "trusted"` to `~/.codex/config.toml`; `read-only` and no
  sandbox do not; a thread `config` override
  `projects.<realpath cwd>.trust_level` suppresses the write every time
  (config backed up and restored around each probe).
- **Process groups**: a `sleep 37` run by a turn had pgid == its own pid,
  app-server another; `killpg` on app-server's group left it running,
  reparented to launchd (reap.go).
- **MCP servers**: `config.mcp_servers.<name> = {command, args, env}` on
  `thread/start` starts that server (`mcpServer/startupStatus/updated`
  names it); nothing written to the user's config.
- **thread/items/list without a loaded thread**: a fresh app-server answers
  it right after `initialize` (~0.8 s spawn-to-answer); an ephemeral thread
  answers `-32601 not supported yet`.
- **model/list**: answers right after `initialize`, no thread.
- **thread/fork**: answers with a new thread id and `forkedFromId`.
