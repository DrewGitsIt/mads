# acprpc knob-fit captures — three real ACP agents, 2026-09-09

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
| `hermes-acp-full.jsonl` | 249 / 69516 | Hermes Agent 0.21.1 (native ACP) | `hermes acp` |
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
  auto-approved a shell exec, so no permission request appears in the
  codex capture); hermes `default`/`accept_edits`/`dont_ask` (no
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
- **session/load works on claude-agent-acp** (`loadSession:true`;
  replayed `session/update` notifications precede the response —
  goose-compatible lifecycle). codex and hermes also advertise
  `loadSession:true` (not exercised here).
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
(replaced); stderr logs carried home paths (replaced). The literal
`api-key` in codex is an authMethod *id*, not a secret — kept. Sweep
(`grep -rE '/home/drew|<hostname>|<email>|api[_-]?key|sk-ant|Bearer '`)
is clean of identifying values. Wire JSONL files are otherwise
byte-for-byte as captured; permission-request rawInput contains only the
probe file contents.

## Reproducing

See `tools/capture_acp_agent.py --help`. The claude/codex adapters were
installed via `npm i -g @agentclientprotocol/claude-agent-acp
@agentclientprotocol/codex-acp`; hermes speaks ACP natively. Each full
run costs one small model turn on the respective account (~$0.08 on
claude opus at capture time). Not yet captured (extend here, same
conventions): a codex run in `read-only` mode to pin its
permission-request shape, and session/load on codex/hermes.
