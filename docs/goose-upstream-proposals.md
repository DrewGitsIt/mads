# goose upstream proposals

Issue/PR-ready drafts for the two remaining upstream-fixable seams the
goose adapter bridges (`docs/GOOSE-ADAPTER-DESIGN.md` "Stance: goose is
malleable, not gospel"). Each section states goose's current behavior with
a capture citation, mads' shipped workaround, the proposed upstream change
with source pointers, and exactly which mads code becomes deletable if it
lands. Filing happens outside this repo; these drafts are the evidence
package.

**Evidence base.** Wire captures were taken against the goose **1.48.0**
release binary (`providers/goosecli/testdata/captures/`, see its README for
provenance). Source pointers are against the local goose checkout at
`c0be6ce97` (post-1.48.0, 2026-09); every load-bearing claim was re-checked
there — line numbers cite that revision, function names are the durable
anchor.

**Context: the third seam is moot for mads.** The design doc's original
seam 3 — `goose run --output-format stream-json` never announces a session
id (pinned: `testdata/captures/oneshot-stream.jsonl`, zero "session"
mentions across a full 303-line tool-using run) — no longer needs an
upstream fix from mads' perspective: OneShot now rides the ACP engine, not
`goose run` (`providers/goosecli/goosecli.go` Matrix, BehaviorOneShot
comment: "supersedes the design doc's `goose run` plan"), and
`session/new` returns the session id synchronously. A session-id line in
stream-json would still be a trivially additive courtesy to other
stream-json consumers, but mads no longer has skin in it — do not lead
with it.

---

## Proposal 1 — populate ACP `kind` on tool_call updates for goose builtins

**Suggested title**: `acp: classify builtin tools onto the ACP ToolKind
taxonomy in tool_call updates`

### Current behavior (goose ≤ HEAD)

goose never sets the ACP `kind` field on the `tool_call` session updates
it streams for model-initiated tool calls. In the entire capture
transcript (`providers/goosecli/testdata/captures/acp-transcript.jsonl`,
goose 1.48.0), `"kind"` appears exactly 3 times — all on
`session/request_permission` payloads (lines 34, 44, 66), where
`build_permission_tool_call_update` hardcodes `ToolKind::default()`
(`crates/goose/src/acp/server/tool_calls/conversion.rs:145`), i.e.
`"other"` for every tool including a shell execute. The 3 `tool_call`
updates in the same transcript (lines 33, 43, 65) carry no `kind` at all:
`build_initial_tool_call` (conversion.rs:94-124) sets title, status,
rawInput, and `_meta.goose.toolCall{toolName, extensionName}` — never
kind.

The identity information needed to classify is already in hand at both
call sites: `goose_tool_call_meta` (conversion.rs:66-92) extracts
`toolName` and `extensionName` from the request via `ToolNameParts`, and
puts them in vendor `_meta`. So goose knows *exactly* which builtin is
running; it just doesn't say so in the spec field.

Capture, line 43 (`_meta` present, `kind` absent):

```json
{"sessionUpdate": "tool_call", "toolCallId": "call_wll8j2aq",
 "title": "shell · echo acp-capture-ok", "rawInput": {"command": "echo acp-capture-ok"},
 "_meta": {"goose": {"toolCall": {"toolName": "shell", "extensionName": "developer"}, ...}}}
```

### mads' workaround

Because there is no spec-level identity, the mads ACP engine grew a whole
knob for it: `ToolIdentity{Mode: MetaNamespace, Namespace: "goose"}` reads
the stable name out of `_meta.goose.toolCall` and classifies it through a
hand-audited, point-in-time table of goose's builtin tool names
(`gooseToolClassTable`, `providers/goosecli/goosecli.go:81-106`, audited
at tag v1.48.0). Unknown names deny below full trust — safe, but it means
the table silently over-denies every time goose renames or adds a builtin
(a risk the design doc carries explicitly under "Tool-name classification
drift"). The engine-side machinery is
`providers/acprpc/session.go` `classify` (MetaNamespace arm),
`decodeNSToolCallMeta`, `rawInputCommand`, and the `ToolClassRule` type
with its `ByCommand` discriminator (`providers/acprpc/acprpc.go:193-206`).

### Proposed upstream change

In `crates/goose/src/acp/server/tool_calls/conversion.rs`, add a
classifier from `(extension_name, tool_name, arguments)` to
`Option<ToolKind>` (the `agent-client-protocol-schema` 1.5.0 taxonomy:
read, edit, delete, move, search, execute, think, fetch, switch_mode,
other) and call it from both `build_initial_tool_call` and
`build_permission_tool_call_update` (replacing the hardcoded
`ToolKind::default()` at conversion.rs:145). The concrete mapping for the
builtins, exactly what mads' trust gate wants and what
`gooseToolClassTable` pins today:

| extension | tool | kind |
|---|---|---|
| developer | `shell` | `execute` |
| developer | `write` | `edit` |
| developer | `edit` | `edit` |
| developer | `tree` | `read` |
| developer | `read_image` | `read` |
| developer | `text_editor` (legacy vocab) | by `command` arg: `view` → `read`; `write`/`str_replace`/`insert` → `edit` |
| analyze | `analyze` | `read` |
| todo | `todo__todo_write` | `think` |

(Developer tool names per
`crates/goose/src/agents/platform_extensions/developer/mod.rs`
`get_tools`; the todo composite name per `platform_extensions/todo.rs`.)
Remaining builtins (`apps`, `chatrecall`, `code_execution`,
`orchestrator`, `scheduler`, `summarize`, `summon`, `tom`) classify at
goose's discretion — the point is that goose owns the mapping and it ships
with the tools, instead of every client re-auditing it per release.
Third-party MCP/extension tools stay unset (clients treat absent as
"other"/unknown), which is honest: goose can't know what an arbitrary MCP
tool does.

Why goose should want this independent of mads: every ACP client that
renders tool activity by kind (icons, grouping, permission summaries)
currently shows goose tools as undifferentiated generic calls, and the
permission dialog claims `kind: "other"` even for `rm -rf` shells. The
change is small, additive, purely in `conversion.rs` + a test, and the
information is already computed two lines above where it would be used.

### mads code deletable if accepted

Once mads' minimum supported goose includes the fix (mads ships against
stock release channels, so the table must survive until then):

- `gooseToolClassTable` (`providers/goosecli/goosecli.go:81-106`) — the
  whole audited table, including the `text_editor` ByCommand split — and
  the descriptor flips to
  `ToolIdentity{Mode: acprpc.ToolIdentityKindBased}` (kirocli's mode),
  dropping `Namespace` and `ToolClassTable` from the goose binding.
- goose is the **only** MetaNamespace binding (kirocli is KindBased,
  `providers/kirocli/kirocli.go:212`), so the engine's entire
  MetaNamespace path goes with it: the `classify` MetaNamespace arm and
  `decodeNSToolCallMeta` (`providers/acprpc/session.go`,
  `providers/acprpc/wire.go:301`), `rawInputCommand`
  (`wire.go:349`), and the `ToolClassRule`/`ByCommand` types plus the
  `ToolClassTable` knob (`providers/acprpc/acprpc.go:118-126, 193-206`).

The trust policy itself (`classAllowed`, `session.go:461-484`) stays — it
is already expressed over the ACP kind taxonomy, deliberately, so this
proposal makes the wire feed it directly.

---

## Proposal 2 — a stable per-session system-prompt surface

**Suggested title**: `acp: promote session system-prompt out of
_goose/unstable` (alternatively: accept a system-prompt param at
`session/new`)

### Current behavior (goose ≤ HEAD)

The only way an ACP client can install a per-session system prompt is the
explicitly-unstable extension method
`_goose/unstable/session/system-prompt/set` — request type
`SetSessionSystemPromptRequest`
(`crates/goose-sdk-types/src/custom_requests.rs:205-219`, method string at
line 207), dispatched in
`crates/goose/src/acp/server/custom_dispatch.rs:121-127`, handled by
`on_set_session_system_prompt`
(`crates/goose/src/acp/server/manage_sessions.rs:57-94`). The semantics
are good and already better than most agents': `mode:"set"` replaces the
base prompt, `mode:"append"` adds keyed text under "Additional
Instructions", empty text clears the key. Capture-pinned in production
shape (`providers/goosecli/testdata/captures/acp-transcript.jsonl` line
13):

```json
{"method": "_goose/unstable/session/system-prompt/set",
 "params": {"sessionId": "20260829_19", "mode": "append", "key": "mads-agent",
            "text": "You are being driven by an automated capture harness."}}
```

The problem is purely the stability contract: `_goose/unstable/*` methods
are removed without notice — the 1.46→1.48 re-verification watched a purge
commit delete six *other* unused unstable methods, including
`session/delete` (`docs/GOOSE-ADAPTER-DESIGN.md` "Re-verified against
source 1.48.0"). A client that carries an agent persona (mads' agent
definitions; any orchestrator, really) cannot silently drop it, so every
client must build a fallback for the day the method vanishes.

### mads' workaround

The engine's `SystemPromptStrategy` knob
(`providers/acprpc/acprpc.go:135-165`): the goose binding declares
`SystemPromptExtensionMethod` with the unstable method name and a
vendor-shape `Params` closure (`providers/goosecli/goosecli.go:147-153`),
and the engine falls back to `SystemPromptFirstSendPrefix` — prefixing the
first user Send with the prompt text — whenever the method returns
`method_not_found` (-32601) (`providers/acprpc/session.go:194-213`). The
design doc carries this as a standing risk ("Unstable goose surface",
mitigations: compat pin, first-Send fallback, smoke test). The fallback is
strictly worse than the real thing: the persona rides a user turn, gets
compacted like conversation, and is visible in transcripts as user text.

### Proposed upstream change

Minimal, zero-behavior-change variant: move the method to a stable name —
`_goose/session/system-prompt/set` — keeping the request shape verbatim,
and keep the unstable name as an alias for a deprecation window. The
handler and semantics need no changes; this is a rename in
`custom_requests.rs:207` plus a dispatch alias, and a one-line stability
note in the SDK docs. goose already treats this surface as
production-grade — goose Desktop's recipe path composes system prompts via
the same agent methods (`extend_system_prompt`,
`crates/goose/src/acp/server/recipe/mod.rs:327`) — so the "unstable"
label is the only thing being changed.

Better variant, worth proposing as the follow-on: accept the same payload
at session creation, e.g. a `systemPrompt: {mode, key, text}` param (or
`_meta.goose.systemPrompt`) on `session/new`/`session/load`. That removes
the client-side ordering obligation (today the client must interleave the
extension call between `session/new` and the first `session/prompt`) and
makes the persona part of the session's birth record, which matters for
goose's own session export/import surface (`on_export_session`,
`manage_sessions.rs:117-131`).

Why goose should want this: system-prompt injection is table stakes for
embedding goose as a sub-agent (ACP's growth direction), the
implementation already exists and is tested, and stabilizing it costs a
rename. Every serious ACP client of goose is otherwise forced to build the
same first-message-prefix hack mads built, which degrades goose's own
prompt architecture (the text lands in the conversation instead of the
"Additional Instructions" section the handler maintains).

### mads code deletable if accepted

- The rename variant deletes only configuration: the unstable method
  string in `providers/goosecli/goosecli.go:149` becomes the stable name
  (with a version-gated alias until the compat floor passes), and the
  design doc's "Unstable goose surface" risk paragraph closes. The
  `-32601` → FirstSendPrefix fallback machinery
  (`providers/acprpc/session.go:194-213, 297`) stays as engine policy —
  it is the generic safety net for *any* vendor extension method — but
  stops being load-bearing for goose.
- The `session/new`-param variant additionally deletes the goose binding's
  `Params` closure (`goosecli.go:150-152`) and the extra pre-first-prompt
  round-trip in the engine's session-start sequence, folding system-prompt
  delivery into the session-creation call the engine already makes.
