# providers/goosecli

The goose provider adapter (spec §11, proposed W7.8): Block's open-source
goose agent, driven through the generic ACP engine at `providers/acprpc`.
This package holds only the knob *values* (`engineConfig`, goosecli.go),
the trust tool-class table, the PTY transport (pty.go), the
TranscriptReader (transcript.go), and the SessionLocator backstop
(locate.go) — every wire behavior lives in the engine.

**Every claim below is pinned by committed goose captures — re-taken
against 1.50.1 on 2026-10-20, with the original 1.48.0 ACP transcript kept
as `acp-transcript-1.48.0.jsonl` (`testdata/captures/README.md` — full
provenance, reproduction commands, and the 1.48.0→1.50.1 drift) — and the
goose v1.48.0 source tree** — the opposite evidentiary
posture from kirocli's docs-research README. Where a fact is a guess, the
row says so.

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | `goose acp` is a first-class JSON-RPC agent; the full lifecycle — initialize, session/new, mode + model configOptions, tool-using turns, permission round-trips, close — is captured end-to-end (`acp-transcript.jsonl`, 57 lines; `acp-transcript-1.48.0.jsonl`, 107 lines, adds a three-permission turn). |
| Interrupt | **Native** | `session/cancel` terminates the run promptly and, at 1.50.1, the prompt response resolves `stopReason:"cancelled"` (`acp-transcript.jsonl` lines 36-55). goose 1.48.0 resolved `"end_turn"` instead — 3/3 cancel attempts at different timings (`acp-transcript-1.48.0.jsonl` lines 86-105; root cause in 1.48's `server.rs` `was_cancelled`). The engine's own interrupted flag does the detection, so the row holds on both versions. |
| OneShot | **Emulated** | Over ACP, not `goose run` — see "The OneShot-over-ACP decision" below. |
| OneShotStream | **Emulated** | Same path; `OnEvent` streams genuinely incrementally (per-chunk `agent_message_chunk`s). |
| Resume | **Native** | `session/load` (`loadSession:true`, `acp-transcript.jsonl` line 2) replays history as coalesced `session/update` notifications before its response resolves (`acp-load-replay.jsonl`); the engine suppresses the replay from the live event stream. |
| RefAnnounce | **Emulated** | The ACP ref is Native-strength — `session/new` returns `sessionId` synchronously, so `Ref()` is set at Start-return. The PTY ref rides a cosmetic banner parse (`pty-banner.raw`: the id sits in ANSI-dim art, matched ANSI-stripped as `(\d{8}_\d+)\s+·`) that a goose redesign could break. Emulated is the honest one-row summary across both transports. |
| Transcript | **Native** | goose's own store, `sessions.db`, read directly (store.go) — read-only, incrementally by row id (`TranscriptSince`), with `goose session export --session-id <ref> --format json` (`oneshot-export.json`) as the fallback when the store's private schema isn't the known one. Both give unix `created` stamps, `userVisible:false` turn-context rows to filter, and tool blocks with correlation ids. See "Conversation keys and the store". |
| StableKeys | **Native** | goose's live ids are its stored ids: text `<messageId>@<created>:<run>`, tools `toolCallId`, user rows `user:<stored id>` (no KeyedSender: goose stores no caller id). See "Conversation keys and the store". |
| ModelDiscovery | **Native** | The live agent's `model` configOption carries `options[]` + `currentValue` (`acp-transcript.jsonl` lines 3-4, 10-12); the engine's DiscoverModels reads it — no LLM call, no static list. |
| CostReporting | **Native** | The prompt result carries `usage{totalTokens,inputTokens,outputTokens}` (`acp-transcript.jsonl` line 35), and `usage_update` notifications carry `{used,size}` context occupancy. |
| ACP | **Native** | `goose acp --with-builtin developer` — the captured spawn shape. |
| PTY | **Native** | `goose session` under a pty (pty.go); banner ref discovery plus the SessionLocator backstop (locate.go: `session list` filtered by cwd + created_at, verified by first-message export — declines on any ambiguity). |
| MCPServers | **Native** | See "MCPServers, both transports" below. |
| TrustLimited / TrustReadOnly | **Emulated** | goose's `approve` mode, every ask answered by the engine: limited forwards edits/commands/unknowns to the user and allows reads itself; read_only is the class gate alone — not a harness-level block. See "Trust: approve, and smart_approve for auto-review" and the trust table below. |
| TrustAutoReview | **Native** | goose's `smart_approve` mode is a reviewer of goose's own: read-only-annotated tools run, an extra LLM call judges the rest, and only what it judges not read-only asks — every such ask forwarded to the user unfiltered. ACP only: the PTY transport sets no mode for any trust level. |
| Branching | **Unsupported** | goose's session fork is real, but mads has no provider-layer branching surface — flip when it grows one. |
| CwdReroot | **Unsupported** | No mechanism at all. |

### The OneShot-over-ACP decision

OneShot/OneShotStream are emulated over the engine's ACP path, superseding
the design doc's `goose run` plan, because:

1. `goose run --output-format stream-json` announces **no session id
   anywhere in its stream** (`oneshot-stream.jsonl`: `grep -ci session` =
   0), forcing the fragile `--name` + `session list` fallback
   (`oneshot-name-list.json`) for ref attribution — while `session/new`
   returns the ref synchronously.
2. `OnEvent` streams genuinely incrementally over ACP.
3. One wire path is shared with interactive sessions instead of a second
   parser for the stream-json dialect.
4. It avoids the `goose run` broken-pipe panic trap entirely (captures
   README "Trap": goose panics if its stdout consumer closes early).

Emulated, not Native, because it is an emulation atop the interactive
path, not a purpose-built one-shot surface.

### MCPServers, both transports

- **ACP**: `StartOptions.MCPServers` → `session/new`/`session/load`
  `mcpServers[]` stdio entries (engine-owned, always present per spec —
  goose 1.48.0 and 1.50.1 reject the field's absence, `acp-load-replay.jsonl`
  lines 3-4). http refs ride only when the agent advertised
  `mcpCapabilities.http` (goose does; sse it refuses).
- **PTY**: `--with-extension '[name:]ENV1=v1 ENV2=v2 command args...'`
  per stdio ref (pty.go `ptyArgs`; flag format goose v1.48.0
  `crates/goose-cli/src/cli.rs:174-210`, repeatable `ArgAction::Append`,
  shared between the Session and Run commands). http refs are silently
  dropped on this path — goose has `--with-streamable-http-extension`,
  but no capture exercises it, so it stays out of scope.
- **The ≤1.46 caveat**: on goose ≤1.46 a non-empty `mcpServers` param
  suppresses ALL config.yaml extensions; 1.48 unions them instead
  (design doc "Re-verified against source 1.48.0"; `--no-profile`,
  cli.rs:205-209, proves CLI-specified extensions are additive at 1.48).
  Neutralized on both versions by the always-on `--with-builtin developer`
  (`Config.Builtins` default) on the ACP spawn, and the same flag
  prepended alongside any PTY `--with-extension`. A bare PTY spawn stays
  flag-free, matching the committed `pty-banner.raw` capture spawn.

## Trust tool-name table

`gooseToolClassTable` (goosecli.go) classifies goose's `_meta` tool
identities onto the ACP kind taxonomy for the engine's gate. ACP names are
BARE (`"shell"`, not `developer__shell`): the extension identity rides the
separate `extensionName` field (`acp-transcript.jsonl` line 18,
`_meta.goose.toolCall`). Audited at tag v1.48.0 — the original captures'
version; rows are additive across goose versions (a name goose no longer
emits is harmless; an unlisted one denies).

| Extension | Tool | Class | Evidence |
|---|---|---|---|
| developer | shell | execute | capture line 43: `{toolName:"shell", extensionName:"developer"}` |
| developer | text_editor | ByCommand: view→read; write/str_replace/insert→edit | pre-1.48 vocabulary — one tool whose `rawInput.command` splits reading from editing; unlisted commands deny |
| developer | write | edit | v1.48.0 `crates/goose/src/agents/platform_extensions/developer/mod.rs`, `get_tools` |
| developer | edit | edit | same |
| developer | tree | read | same |
| developer | read_image | read | same |
| analyze | analyze | read | v1.48.0 `platform_extensions/analyze/mod.rs:25,220` — extension and tool share the name |
| todo | todo__todo_write | think | the COMPOSITE-name exception to the bare-name rule (capture line 33; `todo.rs` `TODO_WRITE_TOOL_NAME_COMPLETE`) |
| fetch | fetch | fetch | **guess-grade**: task-prescribed, but no fetch builtin exists at v1.48.0 — a harmless additive row |

Fail-closed rule: wherever the engine decides alone (read_only; limited
with no asker), unlisted identities deny (the kirocli/goose unknown-deny
precedent — over-denying after a rename is safe, under-enforcing is not).
At limited with an asker, an unlisted identity is asked about instead.
Full trust maps to goose mode `auto` explicitly, so a user config.yaml
default of `approve` can't stall a headless session.

## Trust: approve, and smart_approve for auto-review

goose has four modes (`crates/goose-provider-types/src/goose_mode.rs`,
September 2026 tree): `auto` ("Automatically approve tool calls"),
`approve` ("Ask before every tool call"), `smart_approve` ("Ask only for
sensitive tool calls") and `chat` (no tools). Its permission inspector
(`crates/goose/src/permission/permission_inspector.rs`, `inspect`) shows
what the two asking modes actually do with a call that has no user grant
in goose's own permission config:

- `approve`: RequireApproval — always. The capture confirms it asks even
  for `todo__todo_write` (`acp-transcript-1.48.0.jsonl` line 34).
- `smart_approve`: allow a tool annotated read-only; otherwise defer to
  `detect_read_only_requests` — **an extra LLM call** in which goose's own
  model judges whether the call is read-only — and ask only if it says no.

mads' `limited` means "follow the harness": ask before risky things,
reads don't ask — claude-code's normal mode. `smart_approve` reaches that
shape by having a model judge risk: nondeterministic, token-spending, and
exactly what the separate `auto_review` level ("Auto-review") asks for —
so `auto_review` maps to `smart_approve`, and the engine forwards every
ask it makes to the user unfiltered (each is the reviewer's escalation;
goose caches a not-read-only verdict per tool as AskBefore, so a tool
judged risky once asks thereafter without another LLM call). Note what
goose's reviewer judges: "is this call read-only?", not claude's broader
"is this risky?" — so under goose an edit always escalates, where
claude's classifier would usually let a routine one through.

limited (and read_only) stay on `approve`, the only
deterministic asking mode, and the binding sets the engine's
`PermissionAsksEveryCall`: at limited the engine allows the read/search/
think/fetch classes itself and forwards only edits, commands and unknown
tools to the user (`StartOptions.Permissions`), answering with the
`_once` option of the user's decision (never `_always`, which goose would
persist). read_only never asks: the class gate allows reads and denies
the rest.

Caveat, both modes: a tool the user has granted `AlwaysAllow` /
`NeverAllow` in goose's own permission config (`goose configure`) is
decided by goose without asking, so neither mads' card nor the engine's
class gate ever sees it — a pre-existing grant for `shell` would run
commands even under read_only. mads never writes such grants (no `_always`
replies), but cannot see or override ones the user made.

## The unstable system-prompt dependency

`StartOptions.SystemPrompt` is delivered via
`_goose/unstable/session/system-prompt/set` — works at 1.48.0 and 1.50.1
(`acp-transcript.jsonl` lines 13-14: `{sessionId, mode:"append", key,
text}` → empty result) — with the engine's `-32601` fallback to
first-send prefixing. This is a version-sensitive surface: the method is
explicitly namespaced unstable, so any goose release may drop or rename
it (pkg/preflight's compat note; design doc "Registration & plumbing" §3).
The fallback means a removal degrades delivery, never drops the persona.

## Upstream seams

The stance is "goose is malleable, not gospel"
(`docs/GOOSE-ADAPTER-DESIGN.md`): where the adapter leans on something
fragile, the fix belongs upstream, not in more parsing heroics.

1. **PTY session id only via the cosmetic banner** — propose a stable
   announce (machine-readable ref line or env hook).
2. **System prompt only via the `_goose/unstable/*` method** — propose
   stabilization.
3. **One-shot stream announces no session id** → the `--name` fallback.
   Moot while OneShot rides ACP, but the seam stands for any future
   `goose run` path.

Seams 1 and 2 have drafted upstream proposals:
`docs/goose-upstream-proposals.md` (commit 6445b7f).

## IntentMatrix

`require_approval` is **Emulated**: on the ACP transport goose's `approve`
mode asks and the engine forwards each ask to the user — less the safe
classes it answers itself — while the PTY transport sets no mode at all
(the user's own goose config decides whether the terminal asks). The rest
are honestly **Unsupported**: no command blocklist, no path-scoped write
restriction, and the read_only trust emulation above is a coarser
mechanism than the `read_only` intent claims (goosecli.go `IntentMatrix`).

## Conversation keys and the store

docs/CHAT-STREAM-DESIGN.md §1 keys every item identically live and stored.
Evidence: `testdata/captures/keys/` (goose 1.50.1, survey 2026-10-20 — the
wire of five turns next to every row goose inserted into its store).

| Item | Key | Why it holds |
|---|---|---|
| Assistant text | `<messageId>@<created>:<run>` | Every chunk carries the model call's `messageId` and `_meta.goose.created`; the stored row has the same `message_id` and, as `created_timestamp`, the **first** chunk's `created` (later chunks carry later seconds: `chatcmpl-17` spans 372–375, stored 372). ollama's ids are `chatcmpl-0..999` and repeat within a session (27 repeated pairs in a real store), so `created` is part of the key. `run` is the item's index among the message's text items; thinking runs are numbered apart (`<messageId>@<created>:thinking:<n>`, acprpc `ThinkingApart`), because the store can move a message's thinking off its text row (below). |
| Tool call / result | `toolCallId` | Live, a tool call's `_meta` carries the *preceding text's* messageId; stored, the call is a row of its own (`msg_<uuid>`, sometimes a chatcmpl id), so only the call id is the same on both sides. |
| User message | `user:<stored message id>` | goose doesn't echo the prompt and stores no caller id (no `KeyedSender`), but stores the text byte-identical; the session child matches mads's send to this row on settle. |

**Stored thinking: unsupported.** goose does store thinking (a
`{"type":"thinking","thinking":…}` block; 1.48.0 replays it on load), but
not where the live key says. Live, a model call's thought chunks carry
its `messageId` and the first chunk's `created`. Stored, a call that ends
in tool calls has its thinking moved onto the tool-call messages
(goose 1.48.0 `agents/agent.rs`, "Thinking/reasoning belongs on the
tool-call messages"): the first carries the call's `messageId` but is a
new `Message::assistant()` with a later `created` — the 1.48.0 capture's
`chatcmpl-846` thinks from 1788032672, its tool call is stamped
1788032675 — and further parallel calls get generated ids and a copy of
the same thinking. So a thinking row would come back under a key nothing
streamed (and twice for parallel calls); only a reply message's thinking
(thinking then text, no tools) stays on its own row. The same move strips
thinking from a text-then-tool message's text row, which is why text
runs don't count thinking on either side: the stable_keys probe against
the stand-in playing a thinking model (`MADS_FAKE_GOOSE_THINKING=1`,
`TestStableKeysThinkingModel`) fails when they do. (qwen3.8 on ollama
can't show it live: goose gets its reasoning inline as text, with a
literal `</think>`, never as thought chunks.) Live thinking ends
`unsaved`: the chat pane shows it only while it is in the session's
unsettled tail (through the next turn), and not after; the
`session.thinking` events the runner writes aren't rendered in the chat
(docs/CHAT-STREAM-DESIGN.md, "What the providers actually do").

Filtered: the hidden `<turn-context>` row goose writes after every prompt
(`userVisible:false`) — as is anything goose marks `userVisible:false`,
the same rule the export reader always had.

**Why `sessions.db` and not `export`.** The session child re-reads the
store after every settle hint (§3) — each tool completion and each turn
end. `goose session export` spawns goose and re-serialises the whole
session as JSON every time: a long session would pay a process start and a
full dump per tool call. The store is a SQLite file goose already writes,
in conversation order and (live) insert-only, so a row-id cursor reads only
what's new. It's read `mode=ro`/`query_only`, from where the spawned goose
keeps it: `$GOOSE_PATH_ROOT/data/sessions/sessions.db` for an absolute
`GOOSE_PATH_ROOT`, else `$XDG_DATA_HOME` (absolute) or `~/.local/share`,
then `goose/sessions/sessions.db` (goose `paths.rs`, macOS included).

**Guards on a private schema.** The preflight window (goose 1.46–1.51)
keeps an untested goose from running at all; `checkStoreSchema` checks
every table and column the reader touches (and a row that won't decode
counts the same). On a mismatch — or no store, or no row for the session —
`Transcript` falls back to `goose session export` (same ids, same keys) and
`TranscriptSince` returns `ErrUnsupported`.

**The cursor.** `{last row id, rows at or below it}`. goose rewrites a
session only by deleting rows (`replace_conversation` for compaction,
truncation for edits); either changes that count, and the read answers
`ErrCursorStale`.

**Settle hints** (`EventSettle`, only with `StartOptions.Keyed`):
after each tool completion — goose writes the tool call, its result and
the text before them only once the tool is done (6 s after the call for
`sleep 6`) — and at the prompt response. A long tool keeps that text
unsettled for its whole run; that's goose, not the reader.

## Testing without goose installed

- `fakegoose_test.go` re-execs the test binary as a scripted goose
  (`MADS_FAKE_GOOSE=1`). Where acprpc's fake profiles are deliberately
  adversarial, this fake's job is **goose shape fidelity**: the captured
  initialize capability set, the session/new modes + configOptions body,
  `-32602` on missing `mcpServers` (new AND load), bare `_meta` tool
  names, optionIds that mirror kinds (goose's real coincidence), the
  capture's usage numbers, 1.50.1's `cancelled`-after-cancel (with
  `MADS_FAKE_GOOSE_CANCEL_STOP=end_turn` replaying 1.48.0's `end_turn`),
  and the argv assertion
  `acp --with-builtin developer`.
- `TestConformanceAgainstScriptedBinary` runs the full
  `providers/conformance` suite against that stand-in in CI — every
  declared matrix row, stable_keys included, zero real-binary dependency.
  Cross-process resume, recall, transcript and keys ride a real
  `sessions.db` (goose's schema, `testdata/sessions-schema.sql`) under the
  test's scratch `GOOSE_PATH_ROOT`, written the way goose writes it: user
  and `<turn-context>` rows when a prompt arrives, a message under its
  messageId and first created stamp, ids from a pool of three (ollama's
  repeat), and a tool call — with the text before it — only once the tool
  is done. Every test provider gets a scratch `GOOSE_PATH_ROOT`, so no test
  reads the real store.
- `store_test.go` reads the survey session's own store rows
  (`testdata/captures/keys/run.store.jsonl`) and expects exactly the keys
  acprpc derives live from that session's wire (`acprpc/keys_test.go`):
  collisions told apart by `created`, turn-context filtered, chained
  `TranscriptSince` = `Transcript`, a rewrite → `ErrCursorStale`, and a
  renamed column → export fallback + `ErrUnsupported`.
- The real-binary evidence base is the committed captures themselves
  (`testdata/captures/`) — byte-replayed through the engine in
  `providers/acprpc/replay_test.go`, which is stronger than a smoke test:
  the engine is proven against recorded goose output, and reruns don't
  spend tokens or drift with model output.

## Real-binary smoke (opt-in)

```
MADS_GOOSE_SMOKE=1 go test ./providers/goosecli -run RealGoose
```

Needs the goose binary on PATH and a **serving local ollama** (free
tokens, but slow — minutes per turn on a cold model; once opted in, ollama
being down fails the run rather than skipping). The smoke asserts protocol
mechanics only — ref shape at Start-return, turn ends, usage presence,
interrupt semantics, transcript normalizer roles — never model output
content: a local model's instruction-following is not a protocol claim,
and a turn could legally be tool-only. `TestRealGooseOneShotSmoke` covers
the OneShot-over-ACP path the same way (ref + usage, `Text` untouched).
