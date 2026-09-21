# acprpc knob-fit captures — three real ACP agents, 2026-09-09 (extended 2026-09-21)

Sanitized ACP stdio transcripts from three agents *other than* goose,
captured to validate the acprpc engine's knob set before it ossifies
(docs/DECLARATIVE-PROVIDERS-DESIGN.md §"providers/acprpc"; the
"second-agent validation" task in docs/goose-adapter-tasks.yaml). The
goose evidence lives separately in
providers/goosecli/testdata/captures/ — same log format, same rules.

All captures were driven by `tools/capture_acp_agent.py` (committed here;
generalizes the goosecli driver): every wire line, both directions, as
`{"dir":"c2s"|"s2c","msg":{...}}` JSONL. The client advertised
`protocolVersion: 1` and fs capabilities false; permission requests were
answered by selecting the offered option whose kind is `allow_once` —
never by hardcoded optionId. The "full" scenario is: initialize →
session/new (cwd = a /tmp scratch dir, mcpServers []) → one tool-using
prompt ("create probe.txt…") with permission auto-answer → a second
prompt cancelled mid-turn via session/cancel → session/close.

## The files

| File | Lines / bytes | Agent (version) | Command |
|---|---|---|---|
| `claude-agent-acp-full.jsonl` | 28 / 36628 | `@agentclientprotocol/claude-agent-acp` 0.75.1 wrapping claude 2.1.266 | `claude-agent-acp` |
| `claude-agent-acp-load.jsonl` | 14 / 20220 | same, fresh process | `claude-agent-acp` — initialize → session/load of the full run's session → replayed updates |
| `codex-acp-full.jsonl` | 158 / 65340 | `@agentclientprotocol/codex-acp` 1.10.0 wrapping codex-cli 0.153.4 (ChatGPT-authed) | `codex-acp` |
| `codex-acp-load.jsonl` | 20 / 16794 | same, fresh process, 2026-09-21 | `codex-acp` — initialize → session/load of the full run's (then 12-day-old) session → replayed updates |
| `codex-acp-readonly.jsonl` | 13 / 13009 | same, 2026-09-21 | `codex-acp` — initialize → session/new → session/set_mode `read-only` → prompt; turn resolved by a usage-limit **error**, model never ran (see facts) |
| `hermes-acp-full.jsonl` | 249 / 69516 | Hermes Agent 0.21.1 (native ACP) | `hermes acp` |
| `hermes-acp-load.jsonl` | 14 / 16369 | same, fresh process, 2026-09-21 | `hermes acp` — initialize → session/load of the full run's (then 12-day-old) session → replayed updates |
| `*.stderr.txt` | — | adapter/agent stderr for each run | |

The claude runs were spawned with every `CLAUDE*` env var removed
(`CLAUDECODE`, `CLAUDE_CODE_*`, `CLAUDE_EFFORT`, `CLAUDE_PID`) — the
capture host was itself inside a Claude Code session, and
claude-agent-acp misdetects nesting otherwise. This is the `env_remove`
knob's live evidence.

## Facts these captures pin

- **Cancel stop-reasons diverge per agent**: claude-agent-acp and hermes
  resolve the cancelled `session/prompt` with `stopReason:"cancelled"`;
  codex-acp resolves `"end_turn"` (as does goose). The engine's own
  interrupted flag must be the source of truth; both wire styles are real.
- **optionIds never mirror kinds**: claude offers
  `allow-once`/`allow-with-updates`/`reject` (no reject_always); hermes
  offers only `allow_once`/`deny` (two options, no _always pair); goose's
  ids happen to equal kind names. Kind-based selection with the
  same-polarity fallback ladder is the only correct client behavior.
- **Mode vocabularies are pure vendor invention**: claude
  `default`/`acceptEdits`/`plan`/`auto`/`bypassPermissions`; codex
  `read-only`/`agent`/`agent-full-access` (default `agent` — which
  auto-approved a shell exec, so no permission request appears in any
  codex capture; the 2026-09-21 `read-only` attempt hit the account
  usage limit before the model ran, so codex's permission-request shape
  is still unpinned); hermes `default`/`accept_edits`/`dont_ask` (no
  full-bypass mode at all). The trust→mode-id map must be a descriptor
  knob. Both ACP-org adapters additionally tag modes with a shared
  `_meta.kind` taxonomy (`standard`/`auto_review`/`full_access`/`plan`);
  hermes does not.
- **Mode is also a config option** on claude and codex (`category:
  "mode"`), confirming the runtime mode-transport detection; hermes has
  **no configOptions at all** — the model-option knob must tolerate
  absence (model switching honestly Unsupported).
- **Model options where present are flat string selects** (`category:
  "model"`): claude `default`/`opus[1m]`/…, codex `gpt-5.6-terra`/… —
  same shape as goose.
- **Tool identity varies**: claude sets ACP `kind` AND
  `_meta.claudeCode.toolName` (`"Write"`); codex and hermes set `kind`
  only (`execute`/`edit`) with no `_meta` tool name; goose sets neither
  (kind omitted, `_meta.goose.toolCall`). All three ToolIdentity
  strategies (kind-based / meta-namespace / title-only) occur in the wild.
- **Usage rides the prompt result** on all three (`inputTokens`/
  `outputTokens`/`totalTokens`, plus `cachedRead/Write` on claude,
  `thoughtTokens` on codex/hermes), and all three emit `usage_update`
  `{used,size}` (claude adds `cost{amount,currency}`).
- **session/load works on all three** (`loadSession:true`), even against
  12-day-old sessions (codex/hermes loads captured 2026-09-21). claude
  and codex replay all `session/update` notifications *before* the
  response — goose-compatible lifecycle — and codex's load result
  re-states `modes`/`models`/`configOptions` like session/new. hermes
  replays before the response too, but then emits
  `available_commands_update` and a `usage_update` *after* it — the load
  response is not end-of-replay-stream, and its result carries
  `_meta.hermes.sessionProvenance` plus modes/models (still no
  configOptions).
- **Replayed history diverges on cancelled turns and chunking**: codex
  replays both turns — including the cancelled one, whose full 1–50
  output appears (matching its live behavior: it streamed to 50 after
  session/cancel and resolved `end_turn`) — with each turn's streamed
  chunks coalesced into one whole-message chunk. hermes omits the
  cancelled turn from replay entirely: only the completed first turn
  comes back.
- **A prompt turn can resolve as a JSON-RPC *error*, not a result**:
  codex over its account usage limit answers `session/prompt` with error
  `-32603`, `data.codexErrorInfo: "usageLimitExceeded"` — after first
  emitting the limit text as an ordinary `agent_message_chunk` and a
  `session_info_update` with `_meta.codex.threadStatus
  {type:"systemError"}` (`codex-acp-readonly.jsonl`). There is no
  stopReason at all on such a turn; decoders must not assume prompt ⇒
  result. (`session/set_mode` to `read-only` itself succeeded with `{}`.)
- codex emits an unsolicited `_auth/status_update` notification
  (extension method, `authStatus{kind,label,account{email,plan}}`) right
  after session/new and session/load — skippable like the other
  unsolicited updates, and a sanitization target (it carries the account
  email).
- **codex tool output uses `content:[{type:"terminal"}]`** — the
  ToolCallContent variant goose-as-client explicitly ignores; decoders
  must tolerate it.
- `available_commands_update` and `session_info_update` (with
  `_meta.codex.*` / `_meta.hermes.*`) arrive unsolicited and must be
  skippable; claude's initialize result carries `_meta.claudeCode.*` and
  `_meta.jetbrains.*` extension blobs.
- hermes is the only one with non-empty `authMethods` (`custom`,
  `setup`); claude and codex delegate auth to their wrapped CLIs.

## Sanitization

Rules (goosecli captures convention): `/home/<user>` → `/home/user`,
hostname → `capture-host`, account emails → `user@example.com`, secrets
redacted; session ids / timestamps / token counts / `/tmp` paths kept.
Applied: the account email appeared in claude/codex `_meta` auth blobs
and (2026-09-21) in codex's `_auth/status_update` notifications
(replaced); stderr logs carried home paths (replaced). The literal
`api-key` occurrences in the codex captures are an authMethod *id*, not
a secret — kept. Sweep
(`grep -rE '/home/<user>|<hostname>|<email>|api[_-]?key|sk-ant|Bearer '`)
is clean of identifying values. Wire JSONL files are otherwise
byte-for-byte as captured; permission-request rawInput contains only the
probe file contents.

## Reproducing

See `tools/capture_acp_agent.py --help`. The claude/codex adapters were
installed via `npm i -g @agentclientprotocol/claude-agent-acp
@agentclientprotocol/codex-acp`; hermes speaks ACP natively. Each full
run costs one small model turn on the respective account (~$0.08 on
claude opus at capture time). Still not captured (extend here, same
conventions): codex's `session/request_permission` shape — the
2026-09-21 `read-only` attempt (`codex-acp-readonly.jsonl`) hit the
ChatGPT Free usage limit before the model ran; retry after 2026-10-09
or on an upgraded account.
