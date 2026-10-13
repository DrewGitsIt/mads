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
| `goose-acp-compact.jsonl` | 29 / 24702 | goose 1.50.1, local ollama, 2026-10-12 | `goose acp` — the native-compaction probe (see "Native compaction"); stderr not kept |

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
  live codex captures); hermes `default`/`accept_edits`/`dont_ask` (no
  full-bypass mode at all). The trust→mode-id map must be a descriptor
  knob. Both ACP-org adapters additionally tag modes with a shared
  `_meta.kind` taxonomy (`standard`/`auto_review`/`full_access`/`plan`);
  hermes does not.
- **codex permission-request shape — SOURCE-PINNED** (live capture is
  quota-blocked until 2026-10-09; see §"codex permission options" below).
  Read from the installed `codex-acp` 1.10.0 bundle
  (`dist/index.js`, `src/permissions/*`), which is more complete than a
  single live probe: it gives *every* option set codex can offer, not
  just the one a probe happens to trigger. The load-bearing finding —
  **codex optionIds do NOT uniformly mirror kinds**: a file-edit approval
  offers `optionId:"allow_for_session"` with `kind:"allow_always"` and
  `optionId:"cancel"` with `kind:"reject_once"`. This is direct
  confirmation of the kind-based-selection engine invariant (never key on
  optionId), from a second agent, at doc-sourced evidence tier
  (kirocli precedent) pending live re-confirmation.
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

## Native compaction (2026-10-12)

ACP v1 has no compaction method. Agents that can compact advertise a
slash command in `available_commands_update`, invoked by sending it as an
ordinary `session/prompt`. Advertised `compact`-like commands across every
committed capture:

| Agent | Command | `input` hint (focus text?) | Evidence |
|---|---|---|---|
| goose 1.48.0 / 1.50.1 | `compact` "Compact the conversation history" (`_meta.commandType: Builtin`) | none | `providers/goosecli/testdata/captures/acp-transcript.jsonl`, `goose-acp-compact.jsonl` line 8 |
| claude-agent-acp 0.75.1 | `compact` "Free up context by summarizing the conversation so far" | `<optional custom summarization instructions>` | `claude-agent-acp-full.jsonl` |
| codex-acp 1.10.0 | `compact` "Summarize conversation to avoid hitting the context limit." | none | `codex-acp-full.jsonl` |
| hermes 0.21.1 | `compress` "Compress conversation context" | none | `hermes-acp-full.jsonl` |
| kiro-cli | — no capture exists | — | no binary on the probe host |

Advertising is not proof the command compacts, so goose — the one ACP
binding mads ships that advertises it — was probed live
(`goose-acp-compact.jsonl`, 29 lines, goose 1.50.1 from Goose.app, local
ollama `qwen3.8-coder-nothink` — free tokens; driver
`tools/probe_compact.py`, cwd `/tmp/mads-goose-compact-probe`). Wire:
initialize → session/new → a one-word warm-up turn → `/compact` →
`/compact keep only the word PINEAPPLE` → session/close.

- **`/compact` really compacts.** Occupancy (`usage_update.used`) went
  6339 → 172 across the `/compact` turn (lines 10, 16); the turn resolves
  `end_turn` with 0 output tokens and its only streamed text is the ack
  `"Compaction complete"` (line 17). No dedicated progress notification —
  just `session_info_update` activeRunId on/off around the turn. `goose
  session export` of the session shows the replaced history: a
  `# Conversation Summary` user message plus "Your context was compacted
  at the user's request…", both `agentVisible`.
- **Focus text is accepted but ignored.** `/compact keep only the word
  PINEAPPLE` also answers "Compaction complete" (line 24), but the
  resulting summary (export) never mentions PINEAPPLE — goose's compact
  command takes no argument, matching its missing `input` hint.

Hence: goosecli declares `compaction` native and `compaction_focus`
unsupported, with `Config.CompactCommand = "/compact"` (the engine's
`Compact` prompts it, maps the turn to an EventCompact start/done pair and
keeps the ack out of the text stream; instructions return ErrUnsupported
unless `Config.CompactFocus`). kirocli declares both unsupported: no
capture shows kiro advertising a compaction command over ACP, and no
binary was available to probe. claude-agent-acp's argument hint is
consistent with claude-code's own native `compaction_focus`; codex and
hermes are not mads bindings.

**Transcript and replay.** Live, a successful compaction turn is recorded
in `Transcript()` as one `compact` entry (pre/post occupancy, duration) in
place of the `/compact` prompt and the "Compaction complete" ack. On
`session/load` replay nothing is rewritten: goose replaces the compacted
history with a `# Conversation Summary` user message (export, above), so
the `/compact` prompt/ack are not expected in a replay, but no committed
capture replays a compacted session, and matching that summary text by
prefix would be guessing at goose's wording. Replayed history is left as
the agent reports it.

## codex permission options (source-pinned from codex-acp 1.10.0)

Live capture is quota-blocked (below); these shapes are read directly
from the installed adapter bundle
(`@agentclientprotocol/codex-acp@1.10.0/dist/index.js`, modules
`src/permissions/option-ids.ts` and the `*DecisionOptions` /
`*ApprovalOptions` builders). optionId constants: `AllowOnce:"allow_once"`,
`AllowForSession:"allow_for_session"`, `Cancel:"cancel"`,
`AllowPermissionsForTurn:"allow_permissions_turn"`, etc.

- **File-edit approval** (`handleFileChange` → `fileChangeDecisionOptions`),
  the read-only-mode path for a `Write`/edit tool, offers three options:
  | optionId | name | kind |
  |---|---|---|
  | `allow_once` | "Yes, proceed" | `allow_once` |
  | `allow_for_session` | "Yes, and don't ask again for these files" | `allow_always` |
  | `cancel` | "No, and tell Codex what to do differently" | `reject_once` |
- **Command/exec approval** (`handleCommandExecution` →
  `commandDecisionOptions`) builds options dynamically from the
  authoritative decision set: `accept`→`allow_once` (name "Yes, proceed",
  or "Yes, just this once" under a network-approval context),
  `acceptForSession`→`allow_always`, plus a `cancel`/`reject_once`. It
  refuses (returns `{decision:"cancel"}`) rather than emit a partial set.
- **MCP-tool approval** uses a parallel id set
  (`McpApprovalOptionId`: `allow_once`/`allow_session`/`allow_always`/
  `decline`/`cancel`); **permission-profile** approval offers two
  `allow_once` variants + one `allow_always` + a `reject_once`.
- codex validates its own accepted replies against
  `zPermissionOptionKind = allow_once|allow_always|reject_once|reject_always`
  and orders options `allow_once`(0) < `allow_always`(1) < reject(2).

**Why this is decisive for the engine**: optionIds are agent-chosen and
do **not** track kinds (`allow_for_session`/`allow_always`,
`cancel`/`reject_once`) — a second agent confirming the invariant the
engine already enforces (select by kind, never optionId). No new knob; the
"all knobs covered" verdict holds. Evidence tier: doc/source-sourced
(kirocli precedent), to be re-confirmed live once quota clears.

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
claude opus at capture time). codex's `session/request_permission` shape
is source-pinned above; a **live** capture remains blocked — the
2026-09-21 `read-only` attempt (`codex-acp-readonly.jsonl`) hit the
ChatGPT Free usage limit before the model ran, and a local-model
workaround (codex `model_provider = "ollama"`) does not help: codex's
app-server gates `session/new` on stored credentials
(`authRequired()` → `-32000`) independent of the selected provider, and
API-key login needs a real OpenAI-validated key (`wire_api="responses"`).
So a live re-confirmation needs a working codex auth: wait for the
2026-10-09 reset, upgrade the ChatGPT account, or supply a real
`OPENAI_API_KEY`. To reproduce the live path once auth exists:
`python3 tools/capture_acp_agent.py full <cwd> codex-acp-readonly.jsonl
--mode read-only --prompt "edit a file" -- codex-acp`.
