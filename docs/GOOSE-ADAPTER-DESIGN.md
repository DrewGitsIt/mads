# goose adapter — design (proposed W7.8)

A third provider adapter (spec §11): Block's open-source **goose** agent,
driven through its first-class ACP server (`goose acp`, JSON-RPC 2.0 over
newline-delimited stdio) for interactive sessions, `goose run` for one-shots,
`goose session` under a PTY for the terminal transport, and
`goose session export --format json` for normalized transcripts.

**Unlike kirocli, every claim here is verified against a live binary**:
goose **1.48.0** is installed at `~/.local/bin/goose` on this machine (the
early probes ran against 1.46.0; the binary was upgraded 2026-08-29 and all
committed captures are 1.48.0 evidence) with a working local ollama
provider (`qwen3.8`), and the goose source tree is checked out at
`~/repo/goose`. Wire shapes below were captured from real `goose acp`
handshakes and `goose run` invocations — **committed, sanitized, under
`providers/goosecli/testdata/captures/` (see its README for provenance)**
— then cross-checked against the server implementation
(`crates/goose/src/acp/server*.rs`, which builds on the official
`agent-client-protocol` Rust SDK 2.0.0, schema v1). The real-binary smoke
(`MADS_GOOSE_SMOKE=1`) is actually runnable here — a first for a
non-claude adapter.

## Stance: goose is malleable, not gospel

goose is open source, in active development, and checked out locally at
`~/repo/goose`. Unlike claude-code (a closed binary we adapt to) or kiro-cli
(absent, doc-sourced), goose is a **willing counterparty**: where its
interface is awkward, the right move may be a patch or upstream proposal to
goose, not a permanent adapter workaround. Every workaround in this design
is therefore tagged with its upstream-fixable seam. The three known ones:

1. **`kind` never set on model tool calls** — forces tool-name-table trust
   gating. Upstream fix: goose's `tool_calls/conversion.rs` could classify
   its own builtins into ACP's kind taxonomy (it already knows
   `toolName`/`extensionName`); a small, obviously-correct patch.
2. **System prompt only via `_goose/unstable/…`** — upstream fix: propose a
   stable `session/new` param or promote the method out of unstable.
3. **One-shot session-id announcement** — **confirmed absent** (pinned:
   `testdata/captures/oneshot-stream.jsonl`, zero "session" mentions in a
   full 303-line tool-using run). Upstream fix: a session-id line in
   `--output-format stream-json` output would be trivially additive. Until
   then the `--name` fallback carries attribution (see "OneShot").

The adapter should still ship against stock 1.46.0 (mads users install
goose from release channels), but the workarounds are bridges, not
architecture — keep them small and deletable.

### Re-verified against source 1.48.0 (pull of 2026-08-29)

Every wire claim in this doc was re-checked against the goose tree after it
moved 1.46.0 → 1.48.0 (range `3bfc854..8ae4e4b`, +4.7k lines in the ACP
layer). **All load-bearing claims hold**: the core methods, the two
`_goose/unstable` methods we use (a purge commit removed six *other* unused
unstable methods, incl. `session/delete`), the never-set tool-call `kind`
(`tool_calls/conversion.rs` untouched), the three stop reasons, the four
permission optionIds, usage on the prompt result, and the
auth_required/credits_exhausted error shapes. Two behavior changes, both
favorable:

1. **The mcpServers extension-suppression gotcha is fixed in 1.48** —
   `initial_session_extensions` now unions config.yaml extensions, plugin
   MCP servers, and client-supplied `mcpServers` (name-keyed
   push-or-replace) instead of 1.46's either/or. The caveat below applies to
   ≤1.46 only; the `--with-builtin developer` mitigation is correct on both
   and stays.
2. Bare `goose acp` now default-loads the `developer` builtin (1.46 loaded
   none without the flag). Explicit `--with-builtin` still force-loads
   regardless of config — our always-explicit spawn is robust across both.

This also validates the malleability thesis: one of the awkward-seam
*classes* got fixed upstream between two releases without us asking. Note
for the doc's file:line citations — the ACP server was refactored into
`crates/goose/src/acp/server/*.rs` modules in this range, so old
`server.rs`/`dispatch.rs` line references have drifted; the shapes have not.

## ACP v1 spec is normative

The malleability stance above is about *goose*; the **protocol itself is
gospel**. The ACP v1 spec (agentclientprotocol.com, pinned by the official
`agent-client-protocol-schema` 1.5.0 crate goose vendors) defines the wire
contract; the captures in this doc are *evidence of goose's behavior within
that contract*, never a redefinition of it. Where a capture and the spec
disagree, the spec wins and the discrepancy is an upstream goose bug to
report (T10). Verified: every method this design uses — `initialize`,
`session/new`, `session/load`, `session/set_mode`,
`session/set_config_option`, `session/prompt`, `session/cancel`,
`session/close`, `session/update`, `session/request_permission` — is
standard v1; only `_goose/unstable/*` methods and `_meta.goose.*` fields
are extensions, and both ride spec-sanctioned extension points
(underscore-prefixed methods, `_meta`).

Concrete executor rules that follow:

- **Derive `wire.go` types from the spec schema, verify against captures**
  — not captures-first. Goose today is one conforming agent; the adapter
  should survive any conforming agent's output.
- **Handle all five spec `stopReason` values** — `end_turn`, `max_tokens`,
  `max_turn_requests`, `refusal`, `cancelled` — even though goose emits
  only three today. `cancelled` → `Interrupted`; `refusal` → `EventError`
  then TurnEnd; the `max_*` pair → plain TurnEnd. An unrecognized string →
  plain TurnEnd, never a decode error. (Captured practice: goose resolves
  cancelled runs with `end_turn`, not `cancelled` — see the Interrupt
  matrix row — so `cancelled` handling is spec-driven defense, not the
  interruption signal.)
- **Select permission replies by option `kind`, never by hardcoded
  optionId.** The spec says the agent offers an `options[]` array of
  `{optionId, name, kind}` with kind ∈ `allow_once`/`allow_always`/
  `reject_once`/`reject_always`; `optionId` is an *agent-chosen opaque
  string*. Goose's optionIds happen to equal the kind names — coincidence,
  not contract. Pick the offered option whose kind is `allow_once` (or
  `reject_once`), falling back to the `_always` variant of the same
  polarity if `_once` is absent; if no option of the needed polarity
  exists, reply `{outcome: "cancelled"}`.
- **Tolerate every official `sessionUpdate` variant.** The v1 schema
  defines 12 (including `plan` and `plan_removed`, which goose never emits
  today); the decoder must ignore unhandled/unknown variants silently, per
  the mapping table's "ignored in v1" convention.
- **Quarantine extensions.** Keep `_meta.goose.*` parsing in one
  clearly-named type (`gooseMeta`) and `_goose/unstable/*` calls in
  clearly-named helpers, so every extension dependency is greppable and
  the T10 upstream-proposals task can enumerate them mechanically.

**Existing-code findings** (outside this pipeline, flagged for follow-up):
`providers/kirocli` deviates from the spec on exactly the points above —
it replies with *guessed* hardcoded optionIds `"allow"`/`"reject"`
(`permission handling in wire.go`) instead of selecting from the offered
options by kind, and sends a non-spec `modelId` param on `session/new`.
Separately, `provider.ModeACP` as claude-code uses it is stream-json
mislabeled as ACP — a naming smell, not a wire bug. None of these block
the goose adapter; the goose adapter should *not* copy them.

## Shape of the work

New package `providers/goosecli` (registry name `"goose"`), mirroring
`providers/kirocli`'s layout: `goosecli.go` (Provider, Matrix, OneShot,
Models), `session.go` (ACP streamSession), `wire.go` (RPC types +
normalization), `pty.go`, `transcript.go`, `fakegoose_test.go` (scripted
stand-in), `README.md` (row-by-row matrix justification, kirocli
conventions), conformance run in `goosecli_test.go`.

**Decision: self-contained adapter, no shared ACP package in v1.** kirocli's
ACP code is ~430 lines and research-grade (doc-sourced, unverified); goose's
update variants, config-option surface, and permission shapes diverge enough
that extraction now would couple two harnesses' quirks and risk destabilizing
kirocli. Copy the JSON-RPC plumbing pattern (pending-request table, single
read loop, `beginRequest`/`request`/`notify`, `closeWait`) and adapt.
"Extract shared `providers/acprpc` transport" is a candidate follow-up task
once both adapters have real-binary smoke coverage.

## Verified binary surface

| Need | Mechanism (verified) |
|---|---|
| Interactive | `goose acp` — stdio JSON-RPC. `initialize` echoes the client's `protocolVersion` (send `1`); result: `agentCapabilities{loadSession:true, promptCapabilities{image,embeddedContext}, mcpCapabilities{http:true}, sessionCapabilities{list,close}}`. |
| One-shot | `goose run -q --output-format stream-json -t <prompt> [--system S] [--provider P] [--model M] [--name N]` — JSONL of `{"type":"message","message":{role,content:[{type:text\|thinking\|…}]}}`. `-q` suppresses the banner. |
| Resume | ACP `session/load {sessionId, cwd, mcpServers}` (replays history as `session/update` notifications *before* the response resolves; `mcpServers` mandatory at 1.48 — see the lifecycle section); CLI `goose run --resume --session-id <id>`. |
| Transcript | `goose session export --session-id <ref> --format json` — `conversation[]` with roles, content blocks, unix `created` timestamps, plus top-level `usage`/`accumulated_usage`/`accumulated_cost`/`working_dir`. No ACP spawn needed. |
| Session index | `goose session list --format json` — id, `working_dir`, name, `created_at`/`updated_at`, type. Backs SessionLocator. |
| Modes | `session/new` result carries `modes{currentModeId, availableModes}`: `auto` (approve all), `approve` (ask every tool call), `smart_approve` (ask for sensitive), `chat` (tools never execute, model told "tool skipped"). Change via `session/set_mode {sessionId, modeId}`. |
| Model/provider | `session/new` result carries `configOptions` (`provider`, `mode`, `model`, `thinking_effort` selects with `currentValue` + options). Change via `session/set_config_option {sessionId, configId, value}`. CLI: `--provider`/`--model`; env: `GOOSE_PROVIDER`/`GOOSE_MODEL`. |
| System prompt | ACP: `_goose/unstable/session/system-prompt/set {sessionId, mode:"append", key, text}` (append preserves goose's base prompt — matches claude-code's `--append-system-prompt` semantics). One-shot: `goose run --system`. |
| MCP servers | `session/new` params `mcpServers[]` (stdio/http; SSE rejected). **Gotcha (≤1.46 only):** on the installed 1.46 binary, a non-empty `mcpServers` suppresses the user's config.yaml extensions entirely; 1.48 unions them instead (see the 1.48 re-verification note above). |
| Fork | `session/fork` exists and is stable — but mads has no branching surface yet (conformance: "not probed"), so declared Unsupported in v1. |

## Proposed capability matrix

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | `goose acp` verified live: initialize → session/new → session/prompt with streamed `session/update` notifications. |
| Interrupt | **Native** | `session/cancel` notification terminates the run promptly (`_meta.goose.activeRunId` → null), **but do not rely on the stop reason**: in 3/3 captured cancels the in-flight `session/prompt` response resolved `stopReason:"end_turn"`, not `"cancelled"` (pinned: `testdata/captures/acp-transcript.jsonl` turn 2 — goose's `prompt_stop_reason` only sets `was_cancelled` if another stream event arrives *after* the token trips, and the token usually kills the stream first). The adapter's own interrupted flag is the source of truth; still parse `"cancelled"` defensively (it remains one of source's three stop reasons: `end_turn`, `cancelled`, `max_tokens`). Session survives. |
| OneShot | **Native** | `goose run -q -t …` verified: clean text on stdout, exit 0. |
| OneShotStream | **Native** | `--output-format stream-json` verified: per-chunk JSONL → `OnEvent`. Filter `content[].type=="thinking"` chunks; forward `text` chunks as EventText, tool calls as EventToolUse. |
| Resume | **Native** | `loadSession:true` in initialize; `session/load` replay-then-respond confirmed live (pinned: `testdata/captures/acp-load-replay.jsonl`). Ref = goose sessionId (`20260829_2` format). |
| Transcript | **Native** | `session export --format json` verified — richer than kirocli's session/load-replay approach (timestamps, usage, no process spawn). Filter entries with `metadata.userVisible == false` (injected `<turn-context>` blocks) and `metadata.turnContext == true`. |
| ModelDiscovery | **Native** | Probe: spawn `goose acp`, initialize, `session/new` (cwd = temp dir), read the `model` configOption's options + `currentValue` (→ `Default`), `session/close`, exit. No LLM call involved. v1 returns the *active provider's* models (bare IDs); cross-provider composite IDs (`provider/model`) are a noted extension. |
| CostReporting | **Native** | `session/prompt` result carries `usage{totalTokens,inputTokens,outputTokens}` — confirmed live on the captured turn-1 result (pinned: `testdata/captures/acp-transcript.jsonl` line 85) → `Usage` on EventTurnEnd. `usage_update` notifications additionally carry `{used, size, cost{amount,currency}}`. Smoke asserts usage presence. |
| ACP | **Native** | The real thing, official SDK, schema v1. |
| PTY | **Native** | `goose session` interactive REPL under creack/pty (mirror kirocli/claudecode pty.go). |
| Branching | **Unsupported** | goose's `session/fork` is real and stable, but mads has no provider-layer branching surface or conformance probe yet. Flip this row when one exists — goose would be the first Native. |
| CwdReroot | **Unsupported** | No mechanism. |
| MCPServers | **Native** | `StartOptions.MCPServers` → `session/new` `mcpServers[]` (ACP), `--with-extension 'ENV=v cmd args'` (one-shot/PTY). See "Extensions" below for the suppression caveat. |
| TrustLimited | **Emulated** | Mode + tool-name gating — see "Trust enforcement". Not a harness-level block. |
| TrustReadOnly | **Emulated** | Same mechanism, stricter allowlist. |
| RefAnnounce | **Emulated** | ACP: `session/new` returns the ref synchronously — `Ref()` non-empty at Start-return, no subscription needed (Native-strength). PTY: `goose session` prints the id in its startup banner (pinned: `testdata/captures/pty-banner.raw`; ANSI-wrapped — see "PTY") — parseable but cosmetic-fragile; SessionLocator backstops. Emulated as the honest one-row summary (same reasoning as kirocli, but goose's PTY side actually works). |

IntentMatrix: all four intents Unsupported, kirocli-style — goose has no
command blocklist, path scoping, or `PermissionPromptURL` wiring. (The
`approve` mode + a Runner bridge could honestly upgrade `require_approval`
later — see Future work.)

## ACP session lifecycle (session.go)

Spawn: `goose acp --with-builtin developer` (+ `Config.ExtraArgs`), `cmd.Dir
= opts.Cwd`, env via `agentenv.Build` (goose needs HOME/XDG to find
`~/.config/goose/config.yaml` — confirm the isolated-mode base allowlist
covers it; add passthrough if not).

1. `initialize {protocolVersion:1, clientCapabilities:{fs:{readTextFile:false, writeTextFile:false}}}`.
   **Never advertise fs/terminal client capabilities** — doing so reroutes
   goose's developer file/shell tools through client-side `fs/*` and
   `terminal/*` callbacks we would then have to implement. With them false,
   tools run in-process in goose.
2. Fresh start: `session/new {cwd, mcpServers: fromStartOptions}` → ref from
   `sessionId`. Resume: `session/load {sessionId, cwd, mcpServers}` —
   **`mcpServers` is required**: 1.48.0 rejects its absence with -32602
   `missing field \`mcpServers\`` (pinned:
   `testdata/captures/acp-load-replay.jsonl` lines 3-4; the retry with
   `mcpServers:[]` succeeded). Use the kirocli `replaying` flag pattern
   (buffer replayed updates, don't stream them; they also feed
   Transcript's live-session path if wanted, though export is the primary
   source). Replay notifications are coalesced **per message, not per
   token** (5 thought chunks on replay vs 100+ live), and the load
   response carries the session's persisted `currentModeId`. Unknown ref ⇒
   `resource_not_found` → wrap ErrUnsupported? No — return the error;
   resume of a bad ref is a real failure.
3. Trust wiring (see below): `session/set_mode`.
4. Model: if `opts.Model != ""`, `session/set_config_option {sessionId,
   configId:"model", value:"<id>"}` — `value` is a **flat string** (pinned:
   `testdata/captures/acp-transcript.jsonl` lines 10-12; the result echoes
   the updated `configOptions`, no nested-shape retry needed). Errors
   surface at Start (a bad model should fail loudly, not silently run the
   default).
5. System prompt: if `opts.SystemPrompt != ""`,
   `_goose/unstable/session/system-prompt/set {mode:"append",
   key:"mads-agent", text}`. On `method_not_found` (future goose dropping the
   unstable method), fall back to prefixing the first `Send` with the prompt
   text — claude-code-era emulation, better than dropping the agent
   definition on the floor.
6. `Send` = `session/prompt {sessionId, prompt:[{type:"text",text}]}` sent
   async (kirocli's `beginRequest` + `awaitTurnEnd` goroutine — mid-turn
   updates must flow while the response is pending). Response →
   `EventTurnEnd{Interrupted: stopReason=="cancelled" || interruptFlag,
   Usage: fromResult}` — the `|| interruptFlag` arm is load-bearing, not
   belt-and-braces: captured cancels resolve `end_turn` (see the Interrupt
   matrix row), so the flag is what actually detects interruption. A second Send mid-turn gets `invalid_params "session
   already has active run …"` — surface as EventError + TurnEnd, same as
   kirocli's error path (the Runner's busy-tracking should prevent it
   anyway).
7. `Interrupt` = `session/cancel` notification + set interrupted flag.
8. `Close` = best-effort `session/close` (cancels any active run, short
   timeout), then stdin EOF + `closeWait` grace/kill.

### session/update → Event mapping (wire.go)

| goose `sessionUpdate` | mads Event |
|---|---|
| `agent_message_chunk` (text content) | `EventText` |
| `agent_thought_chunk` | dropped in v1 (no thinking kind in the platform vocabulary) |
| `user_message_chunk` | ignored live (echo of our own prompt); replay → `Message{Role:"user"}` |
| `tool_call` | `EventToolUse{Tool{ID: toolCallId, Name: _meta.goose.toolCall.toolName (fallback: title), Input: rawInput, Path: toolPath(rawInput)}}` |
| `tool_call_update` (status `completed`/`failed`) | `EventToolResult{Tool.ID: toolCallId, IsError: status=="failed", Text: joined content}` |
| `usage_update` | not an Event in v1; note `size` is goose's real per-session context window (better than the static `ContextWindowForModel` table — see Future work) |
| `available_commands_update`, `current_mode_update`, `config_option_update`, `session_info_update` | ignored in v1 |
| `plan`, `plan_removed`, any unrecognized variant | ignored in v1 — goose never emits these today, but they're official v1 variants; the decoder must skip, not error |

`toolPath`: dig `path`/`file_path` out of rawInput, kirocli convention.
goose's `_meta.goose.toolCall` gives `{toolName, extensionName}` — use
`toolName` as the normalized Name; pinned examples are `{toolName:"shell",
extensionName:"developer"}` and `{toolName:"todo__todo_write",
extensionName:"todo"}` (`testdata/captures/acp-transcript.jsonl` —
naming is per-extension, not uniform, so never synthesize
`developer__shell`-style composites). Titles are humanized display strings
(`"developer: shell · cargo build"`), not stable identifiers. Live
`tool_call_update`s also arrive in a non-terminal **`in_progress`**
variant carrying `_meta.toolNotification` output chunks before the
terminal `completed`/`failed` one — decoders must skip non-terminal
updates, not error.

## Trust enforcement (Emulated)

goose's ACP `kind` field is **never set on model tool calls** (source:
`tool_calls/conversion.rs` — it stays `Other` and is omitted on the wire),
so kirocli's kind-based gating cannot work here. Instead:

- `TrustFull` (and `""`): `session/set_mode "auto"` explicitly — don't
  inherit the user's config default (which might be `approve` and would
  stall a headless session on a permission request nobody answers).
- `TrustLimited` / `TrustReadOnly`: `session/set_mode "approve"` — goose
  then sends `session/request_permission` for *every* tool call. The
  request params carry **no `_meta`** (pinned:
  `testdata/captures/acp-transcript.jsonl` lines 34/44/66 — just
  `sessionId`, `toolCall{toolCallId, kind:"other", status, title,
  rawInput}`, and `options[]`), so gating must correlate `toolCallId` with
  the immediately preceding `tool_call` session/update — which carries
  `_meta.goose.toolCall` and arrived first in every observed case — to
  recover the (`extensionName`, `toolName`) pair; **deny if no prior
  update matches**. Classify on that pair against a table of goose's
  builtin tools (executor: enumerate from `crates/goose/src/developer`
  etc. — at minimum (`developer`, `shell`) = execute — note the ACP
  toolName is bare `"shell"`, *not* `developer__shell` — text_editor =
  edit ∪ read (rawInput `command` field: `view` is read;
  `write`/`str_replace`/`insert` are edit), analyze/fetch/todo tools =
  read/fetch/think). Unknown tool names **deny** under both trust levels —
  never silently under-enforce (kirocli precedent).
- Respond `{outcome:{outcome:"selected", optionId:<chosen>}}`, choosing
  the offered option whose **kind** is `allow_once`/`reject_once` (see
  "ACP v1 spec is normative" — optionIds are agent-chosen; goose's
  observed ids happen to mirror the kind names, but don't rely on that).
  Confirmed live: all four kinds offered on every request, and the
  `allow_once` reply executed the tool (pinned:
  `testdata/captures/acp-transcript.jsonl`).
  Prefer the `_once` pair so a denial doesn't persist into a later,
  differently-trusted resume.
- `chat` mode is not used for read_only: it blocks *all* tools including
  reads, and feeds the model "tool skipped" — too restrictive.

## OneShot (goosecli.go)

`goose run -q --output-format stream-json -t <prompt> [--system S]
[--model M] [--name mads-<random>]`, `cmd.Dir = req.Cwd`, agentenv.Build.

- Stream parsing: each line `{"type":"message","message":{content:[…]}}`.
  `text` blocks → accumulate final text + `OnEvent(EventText)`; `thinking`
  blocks → skip; tool blocks → `OnEvent(EventToolUse)`. Shapes pinned
  (`testdata/captures/oneshot-stream.jsonl`): tool requests are
  `{"type":"toolRequest","id":…,"toolCall":{"status":"success","value":
  {"name":…,"arguments":{…}}},"_meta":{"goose_extension":…}}` inside
  assistant messages; tool results are `{"type":"toolResponse","id":<same
  call id>,"toolResult":{"status":…,"value":{resultType,content:[…],
  structuredContent:{stdout,stderr,exit_code},isError}}}` riding
  **`role:"user"`** messages. Tool naming is per-extension, not uniform:
  the developer shell is bare `"shell"` (+ `_meta.goose_extension:
  "developer"`), the todo tool is composite `"todo__todo_write"`. Final
  text = joined text blocks of the last assistant message.
- **Ref attribution — resolved**: no session-id line appears anywhere in
  the stream (pinned: `testdata/captures/oneshot-stream.jsonl`, full
  303-line tool-using run, zero mentions of "session"), so the
  `--name mads-<random>` fallback **is needed — and confirmed working**:
  resolve the id via `goose session list --format json` on a verbatim
  `name` match, additionally requiring `user_set_name:true` as a guard
  (auto-named sessions carry `false`; pinned:
  `testdata/captures/oneshot-name-list.json`). Emit `EventSessionRef` once
  resolved, set `OneShotResult.Ref`. Do **not** use `--no-session` (it
  would destroy attribution and transcript access).
- Usage: the stream's final `{"type":"complete"}` line carries full usage
  (`total_tokens`, `input_tokens`, `output_tokens`,
  `cache_read/write_input_tokens`) — fill `OneShotResult.Usage` from the
  stream alone, no export needed. If an export fallback is ever used, read
  **`accumulated_usage`** (whole-session total; matched the stream's
  `complete` line exactly, 17029), never top-level `usage`
  (last-request-only, 8424) — pinned:
  `testdata/captures/oneshot-export.json`.
- Rate limits: no structured surface — stderr/stdout heuristic like
  kirocli's `rateLimit()` (goose retries internally; what escapes is a
  generic provider error string). Executor: keep the phrase list
  conservative and note it as guess-grade in the README.

## PTY (pty.go)

`goose session [--resume --session-id <ref>]` under creack/pty, mirroring
kirocli's pty.go: Events() nil, Terminal(), Resize, Send injects a line,
Interrupt writes `\x03`. Ref discovery: the banner's session-id line reads
`20260829_20 · /tmp/mads-goose-capture-pty`, but in the raw bytes the id
sits inside ANSI dim escapes mid-art-line (pinned:
`testdata/captures/pty-banner.raw`) — a `^\s*` anchor only holds after
ANSI stripping. Match unanchored `(\d{8}_\d+)\s+·` against ANSI-stripped
output → announce via RefAnnouncer; on failure, the Runner's
SessionLocator backstop resolves it.

## SessionLocator (transcript.go or locate.go)

`goose session list --format json` filtered by `working_dir == cwd` and
`created_at >= spawnedAfter`; verify the surviving candidate's first user
message via `goose session export --session-id <id>` (skip
`userVisible:false` context entries). 0 or 2+ candidates ⇒ `ok=false` —
never guess (provider.go contract).

## Transcript normalization (transcript.go)

`goose session export --session-id <ref> --format json` →
`conversation[]` → `[]provider.Message`:
- role `user` + text content → `Message{Role:"user"}`; **skip** entries with
  `metadata.userVisible == false` (injected turn-context).
- role `assistant`: `text` blocks → `Message{Role:"agent"}`; `thinking`
  blocks skipped; `toolRequest` blocks → `Message{Role:"tool", Tool:…}`.
- tool results ride **`role:"user"`** entries as `toolResponse` blocks
  (pinned: `testdata/captures/oneshot-export.json` — `conversation[]` tool
  blocks are byte-identical in shape to the stream's) →
  `Message{Role:"tool_result", IsError: toolResult.value.isError}`; don't
  misread these entries as user turns. Tool naming is per-extension, not
  uniform: bare `"shell"` + `_meta.goose_extension:"developer"` for the
  developer shell, composite `"todo__todo_write"` for todo — the ACP
  surface agrees (`_meta.goose.toolCall = {toolName:"shell",
  extensionName:"developer"}` / `{toolName:"todo__todo_write",
  extensionName:"todo"}`).
- `created` (unix seconds) → `Timestamp`.
Golden-pin the normalization against a captured real export
(claudecode/transcript convention).

## Extensions & MCP servers

- Always spawn ACP with `--with-builtin developer` (Config knob `Builtins
  []string`, default `["developer"]`) — explicit builtins are force-loaded
  regardless of config.yaml, guaranteeing file/shell tools exist even when
  `mcpServers` is non-empty (which on ≤1.46 **suppresses all config.yaml
  extensions**; 1.48 unions them — document both in the README's MCPServers
  row). The explicit flag is correct on both versions.
- `StartOptions.MCPServers` → `session/new` `mcpServers[]` as stdio entries
  (shell-split `Command` per `MCPServerRef` convention; env from `Env`).
  One-shot/PTY: `--with-extension 'K=V cmd args…'`.

## Error surfaces to handle

| Condition | Wire | Adapter behavior |
|---|---|---|
| Provider unconfigured | `auth_required`, code **-32000** (mid-stream or at agent creation) | EventError with actionable text ("goose provider not configured — run `goose configure`"); turn ends |
| Credits exhausted | -32603 with `data:{reason:"credits_exhausted", url?}` | EventError incl. top-up URL |
| Generic provider/stream failure (incl. rate limits) | -32603 "Error in agent response stream: …" | EventError + TurnEnd; apply the rate-limit heuristic to the message for `*RateLimitError` on OneShot |
| Bad cwd | `invalid_params` on session/new (must be absolute + exist) | fail Start |
| Unknown resume ref | `resource_not_found` "Session not found: <id>" | fail Start |
| Concurrent prompt | `invalid_params` "session already has active run `<id>`…" | EventError + TurnEnd |
| Early-closing stdout consumer (one-shot) | goose panics mid-run: `failed printing to stdout: Broken pipe` | OneShot's stream reader must drain stdout to EOF — never close early on "enough output" |

## Registration & plumbing (executor checklist)

1. `providers/goosecli/` — the adapter (files per "Shape of the work").
2. Register in `services/router/main.go`, `services/session/main.go`,
   `services/pipeline/main.go` (mirror the kirocli blocks), and the
   `cmd/mads/run.go` `-provider` switch (`goose`, plus `-goose-args`).
3. `pkg/preflight/providers.go`: binary `goose`, version probe
   `goose --version` (output is ` 1.48.0` — note the leading space).
   `pkg/preflight/compat.go`: proven range 1.46–1.48 (binary-verified at
   1.48.0 — all committed captures are 1.48.0 evidence; the earlier probes
   ran at 1.46.0. The `_goose/unstable/*` system-prompt method and the
   `--output-format stream-json` flag are the version-sensitive surfaces,
   and the 1.46→1.48 deltas — including the ≤1.46 mcpServers suppression —
   are documented in the re-verification note above).
4. `providers/README.md`: add the goosecli bullet.
5. Conformance: `conformance.Run(t, New(Config{Bin: fakegoose}))` with a
   scripted stand-in implementing the verified wire shapes above
   (fakekiro_test.go is the template — but script *goose's* shapes:
   `_meta.goose.toolCall`, four-option permissions, `usage` on prompt
   result, `session/list`). `MADS_GOOSE_SMOKE=1` real-binary smoke —
   runnable on this machine against ollama; assert protocol mechanics
   (ref format, turn end, usage present, export parses), never model
   output content.
6. UI: nothing goose-specific — `/providers`, `/providers/behaviors`,
   `/providers/{name}/models` render from the registry. Check
   `ui/src/router/modelDisplay.ts` context-window table if goose models
   should show non-200k meters (qwen3.8 reported `size:128000`).

## Risks / open questions

- **Unstable goose surface**: `_goose/unstable/session/system-prompt/set` is
  explicitly unstable; goose releases fast. Mitigations: compat pin, the
  first-Send fallback, and the smoke test on this machine.
- **Tool-name classification drift**: trust gating's allow/deny table is a
  point-in-time snapshot of goose's builtin tool names; unknown-deny keeps
  it safe but may over-deny after goose renames. README must carry the
  table + provenance.
- ~~OneShot ref line~~ **resolved**: stream-json announces no session id —
  confirmed gap (pinned: `testdata/captures/oneshot-stream.jsonl`); the
  `--name` + `session list` fallback is committed to and confirmed working
  (pinned: `testdata/captures/oneshot-name-list.json`).
- ~~Tool-block shapes in stream-json/export~~ **resolved**:
  `toolRequest`/`toolResponse` shapes pinned on both surfaces, results on
  `role:"user"` messages (pinned: `testdata/captures/oneshot-stream.jsonl`,
  `testdata/captures/oneshot-export.json`; see "OneShot" and "Transcript
  normalization").
- **Model axis**: v1 exposes only the active provider's models. If mads
  wants cross-provider selection (`ollama/qwen3.8` vs `anthropic/…`),
  that's a composite-ID convention change touching ModelDiscovery + the
  model popover — deliberately out of scope.
- **contextwindow**: goose reports the *real* window per session
  (`usage_update.size`); mads' static table would misreport local models.
  Candidate follow-up: a `ContextWindowReporter` session strategy, or at
  minimum add goose model entries to both static tables.

## Future work (explicitly not v1)

- Bridge `session/request_permission` to the Runner's platform permission
  UI (`PermissionPromptURL` / EventPermissionRequest) — goose's `approve`
  and `smart_approve` modes make it a natural fit, and it would honestly
  upgrade `IntentRequireApproval`.
- `session/fork` → BehaviorBranching once mads grows a branching surface.
- `_goose/unstable/session/steer` (mid-turn message injection) — no mads
  surface exists for it today.
- Shared `providers/acprpc` transport extraction (goosecli + kirocli).
