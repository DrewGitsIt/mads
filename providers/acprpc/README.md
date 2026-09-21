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
- Unknown tool identity **fails closed** below full trust: an
  unclassifiable call denies under limited/read_only — over-denying after
  an agent renames a tool is safe, under-enforcing is not.

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
| `ModelConfigOptionID` | Usually `"model"`, a flat string select (goose capture `acp-transcript.jsonl` lines 10-12; same shape on claude/codex). Empty = honestly unsupported: hermes advertises no configOptions at all, amp exposes no picker (goose `amp_acp.rs:85`). |
| `PreseedConfigOptions` | goose-as-client's `session_config_options` (`provider.rs:69`): copilot/pi want the model pinned at session/new (`copilot_acp.rs:73`), claude sets it lazily after. |
| `SystemPrompt` | Most ACP agents have NO system-prompt surface — goose-as-client drops system prompts entirely (`provider.rs:820-826`); goose-as-agent has only `_goose/unstable/session/system-prompt/set` (works at 1.48.0, `acp-transcript.jsonl` lines 13-14). mads must never silently drop an agent persona, so the zero value is first-send prefixing and the extension strategy falls back to it on `-32601`. |
| `ToolIdentity` | ACP gives clients no canonical tool name: claude sets `kind` + `_meta.claudeCode.toolName`, codex/hermes set `kind` only, goose sets neither and puts identity in `_meta.goose.toolCall` (`testdata/captures/README.md` "Tool identity varies"). All three strategies occur in the wild; TitleOnly (the zero value) means limited/read_only degrade to deny-all. |
| `ToolClassTable` | goose's `tool_call` updates carry only the `_meta` name and no `kind` (`goosecli/testdata/captures/acp-transcript.jsonl` line 33), so classification must be supplied, not read off the wire. Unknown identities deny below full trust. |

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
