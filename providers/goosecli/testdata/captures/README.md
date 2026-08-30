# goose wire captures — provenance

Real-binary evidence for the goosecli adapter (`docs/GOOSE-ADAPTER-DESIGN.md`).
Every fact the adapter's parsers rely on is pinned by a file in this
directory; the design doc cites these files by name.

- **Binary**: goose **1.48.0** (`goose --version` prints ` 1.48.0`, leading
  space; the ACP `initialize` response in `acp-transcript.jsonl` line 2
  self-reports `agentInfo: {name:"goose", version:"1.48.0"}`).
- **Captured**: 2026-08-29, Linux x86_64.
- **Model**: local ollama, `qwen3.8` (free — no paid tokens involved).
  Model *output* is nondeterministic; the wire *shapes* are the evidence.
- **Scratch cwds**: every run used a fresh `/tmp/mads-goose-capture-*`
  directory, so no home paths, hostnames, or user-identifying strings ended
  up on the wire.

## Files

### Set 1 — one-shot stream (`oneshot-stream.jsonl`, `oneshot-stream.stderr.txt`)

```sh
cd /tmp/mads-goose-capture-oneshot
goose run -q --output-format stream-json \
  -t "Use the shell tool to create a file /tmp/mads-goose-capture-oneshot/x.txt containing the word hello, then tell me you did." \
  > oneshot-stream.jsonl 2> oneshot-stream.stderr.txt
```

Evidences (303 lines, exit 0, tool really ran — `x.txt` contained `hello`):

- **No session-id line anywhere in the stream.** `grep -ci session` = 0.
  Every line is `{"type":"message",…}` except the final
  `{"type":"complete",…}`. One-shot ref attribution therefore *needs* the
  `--name` fallback (set below).
- **Tool request block** (line 254, inside an assistant message's
  `content[]`): `{"type":"toolRequest","id":"call_…","toolCall":{"status":
  "success","value":{"name":…,"arguments":{…}}},"_meta":{"goose_extension":
  "developer"}}`. The builtin shell tool's name is **bare `"shell"`** with
  the extension in `_meta.goose_extension`; the todo tool in the same
  message is the composite `"todo__todo_write"` — naming is per-extension,
  not uniform.
- **Tool result block** (lines 255-256): rides a **`role:"user"`** message;
  `{"type":"toolResponse","id":<same call id>,"toolResult":{"status":
  "success","value":{"resultType":"complete","content":[{"type":"text",…}],
  "structuredContent":{"stdout":…,"stderr":…,"exit_code":0},"isError":
  false}}}`.
- **Final `complete` line carries full usage**: `{"type":"complete",
  "total_tokens":17029,"input_tokens":16579,"output_tokens":450,
  "cache_read_input_tokens":0,"cache_write_input_tokens":0}` — OneShot fills
  `OneShotResult.Usage` from the stream alone.
- Chunking: thinking and text arrive as per-token message lines, each
  `{"type":"message","message":{id,role,created,content:[one block],
  metadata:{userVisible,agentVisible,inference:{provider,requestedModel}}}}`.
- stderr was empty (committed anyway as provenance).

**Trap**: never pipe this stream through `head` or any early-closing
consumer — goose panics mid-run with `failed printing to stdout: Broken
pipe`. Always redirect to a file; readers must drain to EOF.

### Set 2 — session export (`oneshot-export.json`, `oneshot-name-list.json`)

```sh
goose session export --session-id 20260829_15 --format json > oneshot-export.json
```

(id resolved as the newest `goose session list --format json` entry whose
`working_dir` matched the scratch cwd — it is the set-1 run's session.)

Evidences:

- Top-level keys include `conversation` (the message array), `usage`,
  `accumulated_usage`, `accumulated_cost` (null on ollama), `working_dir`,
  `goose_mode`, `model_config`, `provider_name`, `session_type`,
  `parent_session_id`, `extension_data`.
- **`usage` is the last request only** (8424 total); **`accumulated_usage`
  is the whole-session total** (17029 — matches the stream's `complete`
  line exactly). Any usage-from-export fallback must read
  `accumulated_usage`.
- `conversation[]` tool blocks are byte-identical in shape to the stream's
  (`toolRequest` in assistant entries, `toolResponse` in `role:"user"`
  entries); entries carry unix-seconds `created` and `metadata.userVisible`.

`oneshot-name-list.json` — the `--name` fallback evidence:

```sh
goose run -q --output-format stream-json --name mads-capture-fallback \
  -t "Reply with exactly the word OK and nothing else. Do not use any tools." >/dev/null
goose session list --format json   # then trimmed, see below
```

The committed file is the **top 3 entries of the list, trimmed to the keys**
`id,name,user_set_name,working_dir,created_at,updated_at,session_type,
message_count` (the full list embeds every historical session and the
user's extension config). It shows the verbatim name match
(`name:"mads-capture-fallback"`) **plus `user_set_name:true`**, an extra
guard the locator can require; auto-named sessions (like set 1's
`"Shell file creation"`) have `user_set_name:false`.

### Set 3 — ACP transcript (`acp-transcript.jsonl`, `acp-load-replay.jsonl`)

Produced by `tools/capture_acp.py` (committed — it *is* the provenance;
stdlib-only Python). Wire format: one JSON object per line,
`{"dir":"c2s"|"s2c","msg":{…}}`, machine-replayable. Session `20260829_19`,
spawned as `goose acp --with-builtin developer` in
`/tmp/mads-goose-capture-acp`.

`acp-transcript.jsonl` (107 lines) sequence and what each pins:

- **initialize** (lines 1-2): echoes `protocolVersion:1`;
  `agentCapabilities{loadSession:true, promptCapabilities{image:true,
  audio:false, embeddedContext:true}, mcpCapabilities{http:true,sse:false},
  sessionCapabilities{list,delete,close}}`, `authMethods`, and
  `agentInfo{name:"goose",version:"1.48.0"}`.
- **session/new** (3-4): result carries `sessionId`, `modes{currentModeId:
  "auto", availableModes:[auto,approve,smart_approve,chat]}`, and
  `configOptions` (`provider`/`model`/`mode`/`thinking_effort` selects with
  `currentValue`).
- **session/set_mode** `{sessionId, modeId:"approve"}` (5-9): empty result;
  a `current_mode_update` notification follows.
- **session/set_config_option** (10-12): the **flat value shape works** —
  `{sessionId, configId:"model", value:"qwen3.8"}` → result echoes the
  updated `configOptions` (no retry with a nested shape was needed).
- **_goose/unstable/session/system-prompt/set** `{sessionId, mode:"append",
  key:"mads-agent", text:…}` (13-14): empty result — works at 1.48.0.
- **Turn 1** (15-85): tool-using prompt in `approve` mode.
  - `tool_call` updates carry `_meta.goose.toolCall` — **`{toolName:
    "shell", extensionName:"developer"}`** for the shell tool and
    `{toolName:"todo__todo_write", extensionName:"todo"}` for todo. The
    ACP toolName for the developer shell is **bare `"shell"`**, *not*
    `developer__shell`; extension identity is the separate
    `extensionName` field. (Same naming as the one-shot stream.)
  - **session/request_permission** (3 round trips: lines 34, 44, 66, each
    immediately preceded by its `tool_call` update at 33, 43, 65): params
    carry `sessionId`, a `toolCall{toolCallId, kind:"other", status:
    "pending", title, rawInput}` — **no `_meta`** on this shape, so trust
    gating must correlate `toolCallId` with the preceding `tool_call`
    update (which always arrived first in this capture) to recover
    `toolName`/`extensionName` — and `options:[{optionId,name,kind}]` with
    all four kinds (`allow_always`, `allow_once`, `reject_once`,
    `reject_always`; optionId happens to equal kind — coincidence, select
    by `kind`). Client replies `{outcome:{outcome:"selected",
    optionId:"allow_once"}}`.
  - `tool_call_update` comes in an **`in_progress`** variant carrying
    `_meta.toolNotification` live-output chunks *and* the terminal
    `status:"completed"` variant with `content[]` — decoders must skip
    non-terminal updates, not error.
  - **Prompt response carries usage** (line 85, turn 1 result):
    `{stopReason:"end_turn", usage:{totalTokens,inputTokens,outputTokens}}`
    — CostReporting Native confirmed.
  - `session_info_update` updates carry `_meta.goose.activeRunId`
    (run start/end) and title/messageCount changes; `usage_update` carries
    `{used, size:128000}` (the model's real context window).
- **Turn 2 + cancel** (86-105, cancel at line 100): prompt sent, 12+
  chunks observed, then the
  `session/cancel` notification. The run terminated promptly (activeRunId
  → null two updates later) **but the response resolved with
  `stopReason:"end_turn"`, not `"cancelled"`** — observed in 3/3 attempts
  at different cancel timings (chunk 1, chunk 12), including runs where
  chunks continued arriving after the cancel. Root cause (goose
  `crates/goose/src/acp/server.rs`, `prompt_stop_reason` + the reply
  loop): `was_cancelled` is only set when *another stream event arrives
  after* the cancel token trips; the token also kills the producer stream,
  which usually just ends. **Adapters must not rely on
  `stopReason:"cancelled"` to detect interruption** — track your own
  interrupted flag (the design doc's `Interrupted: stopReason=="cancelled"
  || interruptFlag` handles this).
- **session/close** (106-107): empty result.

`acp-load-replay.jsonl` (24 lines, fresh process): initialize, then —
deliberately — `session/load {sessionId, cwd}` **without** `mcpServers`,
which 1.48.0 **rejects**: `-32602 Invalid params, data:{error:"missing
field \`mcpServers\`", phase:"deserialization"}`. Retried as
`{sessionId, cwd, mcpServers:[]}` → success. The replayed history arrives
as `session/update` notifications **before** the response resolves
(`user_message_chunk`, `agent_thought_chunk`/`agent_message_chunk`,
`tool_call` + `tool_call_update`, `available_commands_update`,
`usage_update`); replay is coalesced **per message, not per token** (5
thought chunks vs 100+ live). The response carries `modes` with the
session's persisted `currentModeId:"approve"`.

### Set 4 — PTY banner (`pty-banner.raw`)

Produced by `tools/capture_pty_banner.py`: `goose session` spawned with a
pseudo-terminal (TERM=xterm-256color, 120x40), cwd
`/tmp/mads-goose-capture-pty`, raw bytes recorded until output went quiet
(7721 bytes), then Ctrl-D. View with `less -R` or `cat -v`.

Evidences: the startup spinner (extension loading, heavy `\r`/erase-line
redraw), an OSC 0 window-title escape, and the banner proper. The
session-id line is the second art line:

```
   \____)    20260829_20 · /tmp/mads-goose-capture-pty
```

with ANSI dim codes (`ESC[2m…ESC[0m`) around both the id and the
`· <cwd>` segment, so the id is **not** at line start in the raw bytes.
The banner regex must match against ANSI-stripped text (or allow escapes),
e.g. `(\d{8}_\d+)\s+·` unanchored — the design doc's `^\s*` anchor only
holds post-strip. First banner line carries mode/provider/model
(`new session · ollama qwen3.8`); a context meter (`0/128k`) and the input
prompt follow.

## Sanitization

Rules applied (per the task handoff): `/home/<user>` → `/home/user`,
hostname → `capture-host`, redact secret values, keep session ids /
timestamps / token counts / `/tmp` paths intact.

**Outcome: no edits were needed.** All runs used `/tmp` scratch cwds, so
the raw captures contained no home paths, hostname, usernames, or emails
(verified by sweep: `grep -riE 'home|/Users/|<hostname>|<user>|@|api[_-]?key|secret|sk-'`
— the only `token` hits are `*_tokens` usage fields). The export's
`extension_data` embeds the user's enabled-extension list; it was reviewed
and kept — bundled platform extensions plus two public MCP servers
(`npx @playwright/mcp`, `uvx mcp-server-fetch`) with empty `envs`/
`env_keys`. `oneshot-name-list.json` is trimmed as described above; all
other files are byte-for-byte as captured.

## Reproducing

```sh
# sets 1+2: see the exact commands above (fresh /tmp scratch cwd)
# set 3:
python3 tools/capture_acp.py full /tmp/mads-goose-capture-acp acp-transcript.jsonl   # prints sessionId
python3 tools/capture_acp.py load /tmp/mads-goose-capture-acp acp-load-replay.jsonl <sessionId>
# set 4:
python3 tools/capture_pty_banner.py /tmp/mads-goose-capture-pty pty-banner.raw
```

Turns on a local ollama model take 10-60s+; the scripts use generous
timeouts. Reruns produce equivalent shapes, not identical bytes (model
output, ids, and timestamps vary).
