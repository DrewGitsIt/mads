# providers/acprpc

The generic ACP v1 client engine (proposed W7.9,
`docs/DECLARATIVE-PROVIDERS-DESIGN.md` §"providers/acprpc"): a JSON-RPC 2.0
stdio client any spec-conforming ACP agent rides, parameterized by the
small enumerated knob set in `Config` (acprpc.go). This README makes the
package's doc-comment facts navigable; the doc comments themselves remain
the source of truth closest to the code.

## What it is / isn't

- The **ACP v1 spec is normative** (agentclientprotocol.com, pinned by the
  official `agent-client-protocol-schema` crate): `wire.go`'s types derive
  from the spec schema, never from any one agent's output.
- The committed captures are **evidence of individual agents' behavior
  within the spec, never shapes to bake in**: goose 1.48.0 under
  `providers/goosecli/testdata/captures/`, claude-agent-acp 0.75.1 /
  codex-acp 1.10.0 / hermes 0.21.1 under `testdata/captures/` here. Each
  directory has a provenance README with capture commands and
  sanitization notes.
- The engine is **not a `provider.Provider`** — it has no Name, Matrix, or
  registration. Bindings wrap it and own those: `providers/goosecli` is
  the first instantiation, and `providers/kirocli` was migrated onto it
  (deleting its hand-rolled ~430-line ACP layer).

## Engine invariants

Spec obligations, deliberately NOT knobs — a binding cannot opt out of any
of these:

- `initialize` sends `protocolVersion: 1` and empty client capabilities
  (never advertise `fs`/`terminal` the engine can't serve).
- Runtime capability gating off the initialize result: resume without an
  advertised `loadSession` fails with typed `provider.ErrUnsupported`
  (never a silent fresh session); `sessionCapabilities` entries are
  presence-keyed empty objects, never bools (codex advertises
  `"close": {}`, hermes omits the key — the three `*-full.jsonl` line 2);
  http MCP entries ride only when `mcpCapabilities.http` was advertised,
  sse never.
- `mcpServers` is **always present** on both `session/new` and
  `session/load`, even when empty — goose 1.48.0 rejects its absence on
  load with `-32602 missing field mcpServers`
  (`providers/goosecli/testdata/captures/acp-load-replay.jsonl` lines 3-4).
- One in-flight `session/prompt` per session, enforced at `Send`.
- All five `stopReason`s handled; all twelve `sessionUpdate` variants
  tolerated (unknown ones skipped silently, never an error); a prompt turn
  may resolve as a JSON-RPC *error* rather than a result (codex over its
  usage limit, `codex-acp-readonly.jsonl`) — surfaced as EventError + turn
  end.
- Permission replies are selected from the request's **offered options by
  kind, never by optionId** — optionIds are agent-chosen opaque strings:
  claude offers `allow-once`/`reject`, hermes only `allow_once`/`deny`
  (no `_always` pair — the same-polarity fallback ladder is real), and
  codex's source pairs `optionId:"allow_for_session"` with
  `kind:"allow_always"` (`testdata/captures/README.md`, "codex permission
  options").
- Interruption is tracked by the engine's own flag — **never trust
  `stopReason:"cancelled"`**: goose resolved `end_turn` in 3/3 cancel
  attempts (`acp-transcript.jsonl` lines 86-105), codex likewise, while
  claude/hermes do resolve `cancelled`. Both wire styles are real.
- `session/load` replay updates are suppressed from the event stream
  (buffered as `ReplayMessages` for bindings' transcript use), so a resume
  never leaks history as live events.
- Unknown tool identity **fails closed** wherever the engine decides
  alone: an unclassifiable call denies under read_only, and under limited
  when no asker is wired — over-denying after an agent renames a tool is
  safe, under-enforcing is not. (With an asker, limited forwards it: the
  user sees what the agent asked about. See "Who answers a permission
  request".)
- A user's decision is answered with the **`_once` option only** — never
  `allow_always`/`reject_always`, which would persist a one-off answer
  agent-side (goose writes it to its permission config; claude-agent-acp's
  "allow all edits during this session") so later calls stop reaching
  mads. No `_once` option of the decided polarity → outcome `"cancelled"`.

## The knob table

One row per `Config` field; the provenance column is the evidence that
made it a knob. goose-as-client citations are to the `~/repo/goose`
September 2026 tree — goose's own generic ACP client drives five agents
(claude-agent-acp, codex-acp, copilot, amp, pi) through the nine-knob
`AcpProviderConfig` (`crates/goose/src/acp/provider.rs:61-76`) — and
capture citations are to committed files.

| Knob | Provenance |
|---|---|
| `Bin` | Trivially per-agent: `goose acp …`, `copilot --acp` (design-doc knob table). |
| `SpawnArgs` | Same evidence: `["acp", "--with-builtin", "developer"]` is the captured goose spawn (`goosecli/testdata/captures/README.md`, set 3). |
| `BaseEnv` | The seam for a binding's `agentenv.Build` composition — the base half of the env model goose-as-client's `env`/`env_remove` pair operates on (`provider.rs:61-76`). |
| `Env` | goose-as-client's per-agent env additions knob (`AcpProviderConfig.env`, `provider.rs:64`). |
| `EnvRemove` | claude-agent-acp misdetects a nested session unless `CLAUDECODE` is stripped (goose `claude_acp.rs:87-88`); verified live — the capture host was itself a Claude Code session and every `CLAUDE*` var was removed (`testdata/captures/README.md`). |
| `TrustModes` | Mode vocabularies are pure vendor invention: goose `auto`/`approve`, claude `bypassPermissions`/`default`/`acceptEdits`/`plan`, codex `agent-full-access`/`read-only`, copilot URL-shaped ids (goose per-agent `mode_mapping`, `provider.rs:74`; `testdata/captures/README.md` "Mode vocabularies"). The mode *transport* is never a knob: detected at runtime from the session result — a configOption with category `"mode"` means `session/set_config_option`, else `session/set_mode` (`provider.rs:718-747`). |
| `SendModeAtSessionNew` | goose-as-client's apply order (`provider.rs:1578-1589`); its per-agent `session_mode_id` is Some for claude/copilot/amp, None for codex/pi — both behaviors exist in the wild. |
| `ModelConfigOptionID` | Usually `"model"`, a flat string select (goose capture `acp-transcript.jsonl` lines 10-12; same shape on claude/codex). Empty is fine when the agent instead ships ACP's own `models` selector — hermes does, and the engine reads that (see "Two model surfaces"). Empty *and* no selector = honestly unsupported: amp exposes no picker (goose `amp_acp.rs:85`). |
| `PreseedConfigOptions` | goose-as-client's `session_config_options` (`provider.rs:69`): copilot/pi want the model pinned at session/new (`copilot_acp.rs:73`), claude sets it lazily after. |
| `SystemPrompt` | Most ACP agents have NO system-prompt surface — goose-as-client drops system prompts entirely (`provider.rs:820-826`); goose-as-agent has only `_goose/unstable/session/system-prompt/set` (works at 1.48.0, `acp-transcript.jsonl` lines 13-14). mads must never silently drop an agent persona, so the zero value is first-send prefixing and the extension strategy falls back to it on `-32601`. |
| `ToolIdentity` | ACP gives clients no canonical tool name: claude sets `kind` + `_meta.claudeCode.toolName`, codex/hermes set `kind` only, goose sets neither and puts identity in `_meta.goose.toolCall` (`testdata/captures/README.md` "Tool identity varies"). All three strategies occur in the wild; TitleOnly (the zero value) means limited/read_only degrade to deny-all. |
| `PermissionAsksEveryCall` | goose's `approve` mode asks before EVERY tool call, reads included (`goose_mode.rs`: "Ask before every tool call"; `permission_inspector.rs` sends every call without a user grant to RequireApproval; `acp-transcript.jsonl` line 34 asks for `todo__todo_write`). Set, the engine answers read/search/think/fetch itself at limited trust instead of making each a card. Unset (kiro-cli, whose own policy trusts reads), every ask is forwarded. Never applied at auto_review, whose reviewer mode has already filtered. |
| `ToolClassTable` | goose's `tool_call` updates carry only the `_meta` name and no `kind` (`goosecli/testdata/captures/acp-transcript.jsonl` line 33), so classification must be supplied, not read off the wire. Unknown identities deny below full trust. |

## Who answers a permission request

mads asks the user only when the agent itself asks ("follow the
harness" — `providers/provider` `PermissionAsker`). `handlePermission`
(session.go) routes each `session/request_permission` by trust:

| Trust | Asker wired (`StartOptions.Permissions`) | Who decides |
|---|---|---|
| `full` | either | the engine: allow (full modes never ask; nobody would answer) |
| `read_only` | either | the engine's class gate: read/search/think/fetch allow, the rest deny — a hard guarantee never waits on a click |
| `limited` | yes | **the user**, via the asker — except read/search/think/fetch when `PermissionAsksEveryCall` is set, which the engine allows itself |
| `limited` | no | the engine's class gate: read/edit/search/think/fetch allow; execute/other/unknown deny — never a stall on an ask nobody answers |
| `auto_review` | yes | **the user**, every ask unfiltered — the agent is in its reviewer mode (`TrustModes[auto_review]`, goose `smart_approve`), so an ask is already the reviewer's escalation; `PermissionAsksEveryCall` (a property of the *limited* mode) doesn't apply |
| `auto_review` | no | as `limited` without an asker |

An agent with no reviewer mode never sees `auto_review`: its binding
declares `BehaviorTrustAutoReview` unsupported and the session runs as
`limited` (`provider.EffectiveTrust`, applied by the Runner and again by
the binding — kirocli's `Start`). A binding that declares it working but
maps no `TrustModes[auto_review]` fails `Start` loudly (`applyTrustMode`),
like any other unmapped restricted level.

The forwarded path runs the asker on its own goroutine — the read loop
keeps streaming `session/update`s (and a later cancel) while the user
decides — and fills `PermissionAsk` from the request: `ToolUseID` =
`toolCall.toolCallId` (the same id as the call's `EventToolUse`, so the
Runner's card and `pkg/toolstatus` pending/blocked land on the right row,
whether the agent announced the call first like goose or asks first like
kiro), `Tool` = the classified identity (`_meta` tool name or title),
`Input` = `rawInput` (the request's, else the announced call's).
`ParentToolUseID`, `AgentID` and `Reason` stay empty: none of the four
captured agents (goose, claude-agent-acp, codex, hermes) puts subagent identity or an ask reason on the wire
(claude-agent-acp's `_meta.permission` carries only a title). A deny's
`Message` has nowhere to go — ACP's response carries only the outcome —
so the agent reports the refused call itself (goose: a failed
`tool_call_update`), which the Runner keeps `blocked`.

Withdrawal, each cancelling the asker's ctx exactly once (the `asks` map
delete is the arbitration; a decision arriving later is discarded):

- **Interrupt** sends `session/cancel` and then answers every open ask
  with outcome `"cancelled"` — the spec's obligation once a client has
  cancelled.
- **`$/cancel_request`** from the agent (ACP's request-cancellation RFD,
  LSP-style `{requestId}`; no captured agent has sent one yet) answers
  that request with the `-32800` RequestCancelled error.
- **Process exit / `Close`** withdraws every ask without a reply — nobody
  is left to read one.

## Two model surfaces

ACP agents advertise their selectable models in one of two independent
ways, and an agent may ship either, both, or neither:

| Surface | Read from | Set with | Entry shape |
| --- | --- | --- | --- |
| `models` selector | `session/new` result `models.availableModels` / `currentModelId` | `session/set_model` | `{modelId, name, description}` |
| `model` configOption | `session/new` result `configOptions[ModelConfigOptionID]` | `session/set_config_option` | `{value, name, description}` |

Committed evidence: hermes has only the selector (70 models, no
`configOptions` at all), goose and claude-agent-acp have only the
configOption, codex has both.

`DiscoverModels` prefers the **selector** where both exist, and
`modelSetter` (session.go) resolves the setter in the same order — that
lockstep is what keeps a discovered id selectable. Codex shows why the
order matters: the two surfaces share no ids, the selector lists 15
entries (the `gpt-5.6-terra[low|medium|high]` reasoning-effort variants)
against the configOption's 3 coarse families, and only the selector can
name the live selection (`currentModelId` is `gpt-5.6-terra[medium]`
where the configOption flattens to `gpt-5.6-terra`). Sending an id to the
wrong setter is rejected by the agent.

An agent exposing **neither** surface gets `ErrUnsupported`, kept distinct
from a surface that exists and is empty (`nil, nil`). Collapsing the two
is what made hermes read as a model-less agent.

## The rule: enumerated knobs or a native adapter

A harness needing anything outside this enumerated set is the **signal it
needs its own adapter**, not a new knob. Every knob addition requires a
capture or goose-as-client citation — the design doc's knob-creep rule
(`docs/DECLARATIVE-PROVIDERS-DESIGN.md`: "Knob creep is how a 'generic'
engine becomes two harnesses' quirks in a trench coat"). The 2026-09
knob-fit exercise against three live agents beyond goose found every knob
covered and no gaps (`testdata/captures/README.md`, "Facts these captures
pin") — the set is considered closed until a citation-bearing counterexample
appears.

## Testing

- **Adversarial fake** (`fakeacp_test.go`): scripted stand-in profiles
  whose optionIds deliberately never mirror kinds and which withhold
  capabilities, so a client that hardcodes ids or skips gating fails.
- **Byte-replay of the committed goose captures** (`replay_test.go`): the
  fake serves each captured s2c line verbatim through the engine, proving
  it against real goose 1.48.0 output, not a hand-written approximation.
  The claude/codex/hermes captures pin `wire.go`'s types and the knob-fit
  verdict by citation (see wire.go's per-type comments).
- The `gooseConfig` fixture (`session_test.go`) **deliberately duplicates
  goosecli's production knob values**: an acprpc test cannot import
  `providers/goosecli` without a cycle, so the fixture pins the same facts
  independently (both cite the same captures).

Everything runs in CI with no agent installed; the bindings' own
conformance suites (goosecli, kirocli) exercise the engine end-to-end
through `providers/conformance`.
