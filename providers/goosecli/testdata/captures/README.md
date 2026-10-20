# goose wire captures — provenance

Real-binary evidence for the goosecli adapter (`docs/GOOSE-ADAPTER-DESIGN.md`).
Every fact the adapter's parsers rely on is pinned by a file in this
directory; the design doc cites these files by name.

- **Binary**: goose **1.50.1** — the Goose Desktop bundle at
  `/Applications/Goose.app/Contents/Resources/bin/goose`, which is what
  `pkg/binlocate.Find("goose")` resolves (`goose --version` prints
  ` 1.50.1`, leading space; the ACP `initialize` response in
  `acp-transcript.jsonl` line 2 self-reports `agentInfo: {name:"goose",
  version:"1.50.1"}`).
- **Captured**: 2026-10-20, macOS arm64. Re-taken with the same scripts
  and prompts as the original 2026-08-29 goose **1.48.0** set (Linux
  x86_64, model `qwen3.8`). One 1.48.0 file is kept alongside:
  `acp-transcript-1.48.0.jsonl` (set 3b) — it pins goose's old
  `end_turn`-on-cancel resolution, thought chunks, and a three-permission
  turn the 1.50.1 run did not produce. Every other 1.48.0 file was
  replaced in place.
- **Model**: local ollama, `qwen3.8-coder-nothink` (free — no paid tokens
  involved). A no-think model, so the 1.50.1 files carry no thinking
  blocks; model *output* is nondeterministic (1.50.1's one-shot run made
  three tool calls where 1.48.0's made two); the wire *shapes* are the
  evidence.
- **Isolation**: every goose process ran with `GOOSE_PATH_ROOT` pointed at
  a `/tmp` scratch root (a copy of the user's `config.yaml`, telemetry
  off), so the real session store was never touched and session ids
  restart at `20261020_1`.
- **Scratch cwds**: every run used a fresh `/tmp/mads-goose-capture-*`
  directory, so no home paths, hostnames, or user-identifying strings ended
  up on the wire. On macOS goose records the *resolved* cwd:
  `working_dir` and the banner show `/private/tmp/…` for a `/tmp/…` spawn
  cwd.

## Drift 1.48.0 → 1.50.1

Wire-diffed (initialize, session/new keys, configOption ids, mode ids,
every `session/update` variant, prompt result shapes): the **only**
behavioral change is the cancel stop reason — after `session/cancel`,
1.50.1 resolves the in-flight prompt with **`stopReason:"cancelled"`**
(`acp-transcript.jsonl` line 55), where 1.48.0 resolved `"end_turn"`
(`acp-transcript-1.48.0.jsonl` line 105). Additive only: stream-json tool
request blocks gained `_meta["goose.toolCall.providerIndex"]`; the PTY
banner now loads extensions in the background (no spinner) and prints
`goose is ready`.

## Files

### Set 1 — one-shot stream (`oneshot-stream.jsonl`, `oneshot-stream.stderr.txt`)

```sh
cd /tmp/mads-goose-capture-oneshot
goose run -q --output-format stream-json \
  -t "Use the shell tool to create a file /tmp/mads-goose-capture-oneshot/x.txt containing the word hello, then tell me you did." \
  > oneshot-stream.jsonl 2> oneshot-stream.stderr.txt
```

Evidences (9 lines, exit 0, tool really ran — `x.txt` contained `hello`):

- **No session-id line anywhere in the stream.** `grep -ci session` = 0.
  Every line is `{"type":"message",…}` except the final
  `{"type":"complete",…}`. One-shot ref attribution therefore *needs* the
  `--name` fallback (set below).
- **Tool request block** (line 3, inside an assistant message's
  `content[]`): `{"type":"toolRequest","id":"call_…","toolCall":{"status":
  "success","value":{"name":…,"arguments":{…}}},"_meta":{"goose_extension":
  "developer","goose.toolCall.providerIndex":0}}`. The builtin shell
  tool's name is **bare `"shell"`** with the extension in
  `_meta.goose_extension`; the todo tool (lines 1 and 5) is the composite
  `"todo__todo_write"` — naming is per-extension, not uniform.
- **Tool result block** (lines 2, 4, 6): rides a **`role:"user"`** message;
  `{"type":"toolResponse","id":<same call id>,"toolResult":{"status":
  "success","value":{"resultType":"complete","content":[{"type":"text",…}],
  "structuredContent":{"stdout":…,"stderr":…,"exit_code":0},"isError":
  false}}}`.
- **Final `complete` line carries full usage**: `{"type":"complete",
  "total_tokens":26188,"input_tokens":25995,"output_tokens":193,
  "cache_read_input_tokens":24919,"cache_write_input_tokens":0}` —
  OneShot fills `OneShotResult.Usage` from the stream alone.
- Chunking: text arrives as per-token message lines (lines 7-8 share one
  `id`), each `{"type":"message","message":{id,role,created,content:[one
  block],metadata:{userVisible,agentVisible,inference:{provider,
  requestedModel}}}}`. (The 1.48.0 run's thinking model also streamed
  per-token `thinking` blocks in the same envelope.)
- stderr was empty (committed anyway as provenance).

**Trap**: never pipe this stream through `head` or any early-closing
consumer — goose panics mid-run with `failed printing to stdout: Broken
pipe`. Always redirect to a file; readers must drain to EOF.

### Set 2 — session export (`oneshot-export.json`, `oneshot-name-list.json`)

```sh
goose session export --session-id 20261020_1 --format json > oneshot-export.json
```

(id resolved as the newest `goose session list --format json` entry whose
`working_dir` matched the scratch cwd — it is the set-1 run's session.)

Evidences:

- Top-level keys include `conversation` (the message array), `usage`,
  `accumulated_usage`, `accumulated_cost` (null on ollama), `working_dir`,
  `goose_mode`, `model_config`, `provider_name`, `session_type`,
  `parent_session_id`, `extension_data` — the same key set as 1.48.0.
- **`usage` is the last request only** (6630 total); **`accumulated_usage`
  is the whole-session total** (26188 — matches the stream's `complete`
  line exactly). Any usage-from-export fallback must read
  `accumulated_usage`.
- `conversation[]` tool blocks are byte-identical in shape to the stream's
  (`toolRequest` in assistant entries, `toolResponse` in `role:"user"`
  entries); entries carry unix-seconds `created` and `metadata.userVisible`.
  The typed user message is followed by a `userVisible:false`
  `<turn-context>` entry the normalizer drops.

`oneshot-name-list.json` — the `--name` fallback evidence:

```sh
goose run -q --output-format stream-json --name mads-capture-fallback \
  -t "Reply with exactly the word OK and nothing else. Do not use any tools." >/dev/null
goose session list --format json   # then trimmed, see below
```

The committed file is the **top 3 entries of the list (the isolated store
held only 2), trimmed to the keys** `id,name,user_set_name,working_dir,
created_at,updated_at,session_type,message_count` (the full list embeds
every session's conversation and the user's extension config). It shows the
verbatim name match (`name:"mads-capture-fallback"`) **plus
`user_set_name:true`**, an extra guard the locator can require;
auto-named sessions (like set 1's `"Goose capture oneshot"`) have
`user_set_name:false`.

### Set 3 — ACP transcript (`acp-transcript.jsonl`, `acp-load-replay.jsonl`)

Produced by `tools/capture_acp.py` (committed — it *is* the provenance;
stdlib-only Python). Wire format: one JSON object per line,
`{"dir":"c2s"|"s2c","msg":{…}}`, machine-replayable. Session `20261020_3`,
spawned as `goose acp --with-builtin developer` in
`/tmp/mads-goose-capture-acp`.

`acp-transcript.jsonl` (57 lines) sequence and what each pins:

- **initialize** (lines 1-2): echoes `protocolVersion:1`;
  `agentCapabilities{loadSession:true, promptCapabilities{image:true,
  audio:false, embeddedContext:true}, mcpCapabilities{http:true,sse:false},
  sessionCapabilities{list,delete,close}}`, `authMethods`, and
  `agentInfo{name:"goose",version:"1.50.1"}`.
- **session/new** (3-4): result carries `sessionId`, `modes{currentModeId:
  "auto", availableModes:[auto,approve,smart_approve,chat]}`, and
  `configOptions` (`provider`/`mode`/`model`/`thinking_effort` selects with
  `currentValue`).
- **session/set_mode** `{sessionId, modeId:"approve"}` (7-9): empty result;
  a `current_mode_update` notification precedes the result.
- **session/set_config_option** (10-12): the **flat value shape works** —
  `{sessionId, configId:"model", value:"qwen3.8-coder-nothink"}` → result
  echoes the updated `configOptions`.
- **_goose/unstable/session/system-prompt/set** `{sessionId, mode:"append",
  key:"mads-agent", text:…}` (13-14): empty result — still works at 1.50.1.
- **Turn 1** (15-35): tool-using prompt in `approve` mode.
  - The `tool_call` update (line 18) carries `_meta.goose.toolCall` —
    **`{toolName:"shell", extensionName:"developer"}`**, bare name,
    extension identity in the separate field — plus `_meta.goose.messageId`
    and `created`; no `kind`.
  - **session/request_permission** (line 19, one round trip; the model
    skipped the todo tool this run) is immediately preceded by its
    `tool_call` update; params carry `sessionId`, a `toolCall{toolCallId,
    kind:"other", status:"pending", title, rawInput}` with **no `_meta`**,
    and all four option kinds (optionId equals kind — select by `kind`).
    The request id is a string UUID the reply echoes.
  - `tool_call_update` `in_progress` with `_meta.toolNotification` live
    output (line 21), then the terminal `status:"completed"` (line 22).
  - `agent_message_chunk`s (lines 23-32) all share `messageId`
    `chatcmpl-885`.
  - **Prompt response carries usage** (line 35):
    `{stopReason:"end_turn", usage:{totalTokens,inputTokens,outputTokens}}`;
    `usage_update` (line 34) carries `{used, size:128000}`.
  - `session_info_update`s carry `_meta.goose.activeRunId` (run start line
    16, end line 33) and title/messageCount changes.
- **Turn 2 + cancel** (36-55, cancel at line 52): prompt sent, 13 chunks
  observed, then the `session/cancel` notification. The run terminated
  promptly (activeRunId → null on the next update) and **the response
  resolved `stopReason:"cancelled"`** (line 55) — the 1.48.0 behavior
  (`end_turn`, set 3b) is gone. The engine still tracks its own
  interrupted flag, which covers both.
- **session/close** (56-57): empty result.

`acp-load-replay.jsonl` (16 lines, fresh process): initialize, then —
deliberately — `session/load {sessionId, cwd}` **without** `mcpServers`,
which 1.50.1 still **rejects** (lines 3-4): `-32602 Invalid params,
data:{error:"missing field \`mcpServers\`", phase:"deserialization"}`.
Retried as `{sessionId, cwd, mcpServers:[]}` → success. The replayed
history arrives as `session/update` notifications **before** the response
resolves (`user_message_chunk`, `tool_call` + `tool_call_update`,
`agent_message_chunk` — lines 6-11, one coalesced chunk per message, each
with its `messageId`; the cancelled turn's partial reply is replayed too);
`usage_update` and `available_commands_update` follow. The response (line
12) carries `modes` with the session's persisted `currentModeId:"approve"`.

### Set 3b — the kept 1.48.0 ACP transcript (`acp-transcript-1.48.0.jsonl`)

The original 2026-08-29 capture (goose 1.48.0, model `qwen3.8`, session
`20260829_19`; 107 lines), kept because it pins what the 1.50.1 run does
not: cancel resolving `end_turn`, `agent_thought_chunk`s, and a turn with
three permission round trips including one for `todo__todo_write`. Lines
1-14 match set 3's sequence. Replayed by acprpc's
`TestReplayGooseTranscriptCapture` alongside the 1.50.1 file.

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
  which usually just ends. At 1.48.0 **adapters could not rely on
  `stopReason:"cancelled"` to detect interruption** — track your own
  interrupted flag (the design doc's `Interrupted: stopReason=="cancelled"
  || interruptFlag` handles this).
- **session/close** (106-107): empty result.

The `acp-load-replay.jsonl` taken from this session at 1.48.0 was replaced
by the 1.50.1 one; it showed the same shape, plus replayed thought chunks
(coalesced per message: 5 thought chunks vs 100+ live).

### Set 4 — PTY banner (`pty-banner.raw`)

Produced by `tools/capture_pty_banner.py`: `goose session` spawned with a
pseudo-terminal (TERM=xterm-256color, 120x40), cwd
`/tmp/mads-goose-capture-pty`, raw bytes recorded until output went quiet
(519 bytes), then Ctrl-D. View with `less -R` or `cat -v`.

Evidences: an OSC 0 window-title escape, then the banner proper; 1.50.1
loads extensions in the background, so the 1.48.0 spinner redraw is gone.
The session-id line is the second art line:

```
   \____)    20261020_4 · /private/tmp/mads-goose-capture-pty
```

with ANSI dim codes (`ESC[2m…ESC[0m`) around the id and around the
`· <cwd>` segment, so the id is **not** at line start in the raw bytes.
The banner regex must match against ANSI-stripped text (or allow escapes),
e.g. `(\d{8}_\d+)\s+·` unanchored — the design doc's `^\s*` anchor only
holds post-strip. First banner line carries mode/provider/model
(`new session · ollama qwen3.8-coder-nothink`); a context meter (`0/128k`)
and the input prompt follow.

## Sanitization

Rules applied: `/home/<user>` or `/Users/<user>` → a placeholder,
hostname → `capture-host`, redact secret values, keep session ids /
timestamps / token counts / `/tmp` paths intact.

**Outcome: no edits were needed** (both the 1.48.0 and the 1.50.1 runs).
All runs used `/tmp` scratch cwds and an isolated `GOOSE_PATH_ROOT`, so
the raw captures contained no home paths, hostname, usernames, or emails
(verified by sweep: `grep -iE 'home|/Users/|<hostname>|<user>|@|api[_-]?key|secret|sk-'`
— the only hits are the provider list's "iFlytek Spark" label and
`*_tokens` usage fields). `/private/tmp` is macOS's resolved `/tmp`, kept
as captured. The export's `extension_data` embeds the enabled-extension
list; it was reviewed and kept — bundled platform extensions only.
`oneshot-name-list.json` is trimmed as described above; all other files
are byte-for-byte as captured.

## Reproducing

```sh
export GOOSE_BIN=/Applications/Goose.app/Contents/Resources/bin/goose
export GOOSE_ACP_MODEL=qwen3.8-coder-nothink
export GOOSE_PATH_ROOT=/tmp/goose-capture-root   # config/config.yaml copied in
# sets 1+2: see the exact commands above ($GOOSE_BIN, fresh /tmp scratch cwd)
# set 3:
python3 tools/capture_acp.py full /tmp/mads-goose-capture-acp acp-transcript.jsonl   # prints sessionId
python3 tools/capture_acp.py load /tmp/mads-goose-capture-acp acp-load-replay.jsonl <sessionId>
# set 4:
python3 tools/capture_pty_banner.py /tmp/mads-goose-capture-pty pty-banner.raw
```

Turns on a local ollama model take 10-60s+ (the 1.50.1 runs took ~10s
each); the scripts use generous timeouts. Reruns produce equivalent
shapes, not identical bytes (model output, ids, and timestamps vary), so a
re-capture means re-pinning the ids and token counts the tests assert
(acprpc `replay_test.go`, goosecli `pty_test.go` / `transcript_test.go`,
`testdata/transcript.golden.json` via `-update`).
