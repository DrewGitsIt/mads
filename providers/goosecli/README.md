# providers/goosecli

The goose provider adapter (spec §11, proposed W7.8): Block's open-source
goose agent, driven through the generic ACP engine at `providers/acprpc`.
This package holds only the knob *values* (`engineConfig`, goosecli.go),
the trust tool-class table, the PTY transport (pty.go), the
TranscriptReader (transcript.go), and the SessionLocator backstop
(locate.go) — every wire behavior lives in the engine.

**Every claim below is pinned by committed goose 1.48.0 captures
(`testdata/captures/README.md` — full provenance and reproduction
commands) and the goose v1.48.0 source tree** — the opposite evidentiary
posture from kirocli's docs-research README. Where a fact is a guess, the
row says so.

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | `goose acp` is a first-class JSON-RPC agent; the full lifecycle — initialize, session/new, mode + model configOptions, tool-using turns, permission round-trips, close — is captured end-to-end (`acp-transcript.jsonl`, 107 lines). |
| Interrupt | **Native** | `session/cancel` terminates the run promptly, **but the prompt response resolves `stopReason:"end_turn"`, not `"cancelled"`** — 3/3 cancel attempts at different timings (`acp-transcript.jsonl` lines 86-105; root cause in goose `server.rs`'s `was_cancelled`). The engine's own interrupted flag does the detection, so the row is safe despite the wire's lie. |
| OneShot | **Emulated** | Over ACP, not `goose run` — see "The OneShot-over-ACP decision" below. |
| OneShotStream | **Emulated** | Same path; `OnEvent` streams genuinely incrementally (per-chunk `agent_message_chunk`s). |
| Resume | **Native** | `session/load` (`loadSession:true`, `acp-transcript.jsonl` line 2) replays history as coalesced `session/update` notifications before its response resolves (`acp-load-replay.jsonl`); the engine suppresses the replay from the live event stream. |
| RefAnnounce | **Emulated** | The ACP ref is Native-strength — `session/new` returns `sessionId` synchronously, so `Ref()` is set at Start-return. The PTY ref rides a cosmetic banner parse (`pty-banner.raw`: the id sits in ANSI-dim art, matched ANSI-stripped as `(\d{8}_\d+)\s+·`) that a goose redesign could break. Emulated is the honest one-row summary across both transports. |
| Transcript | **Native** | `goose session export --session-id <ref> --format json` (`oneshot-export.json`) — goose's own session store, no ACP spawn. Richer than kirocli's load-replay approach: unix `created` timestamps per entry, `userVisible:false` injected turn-context entries filterable, tool blocks with correlation ids. |
| ModelDiscovery | **Native** | The live agent's `model` configOption carries `options[]` + `currentValue` (`acp-transcript.jsonl` lines 3-4, 10-12); the engine's DiscoverModels reads it — no LLM call, no static list. |
| CostReporting | **Native** | The prompt result carries `usage{totalTokens,inputTokens,outputTokens}` (`acp-transcript.jsonl` line 85), and `usage_update` notifications carry `{used,size}` context occupancy. |
| ACP | **Native** | `goose acp --with-builtin developer` — the captured spawn shape. |
| PTY | **Native** | `goose session` under a pty (pty.go); banner ref discovery plus the SessionLocator backstop (locate.go: `session list` filtered by cwd + created_at, verified by first-message export — declines on any ambiguity). |
| MCPServers | **Native** | See "MCPServers, both transports" below. |
| TrustLimited / TrustReadOnly | **Emulated** | goose's `approve` mode, every ask answered by the engine: limited forwards edits/commands/unknowns to the user and allows reads itself; read_only is the class gate alone — not a harness-level block. See "Trust: why approve, not smart_approve" and the trust table below. |
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
  goose 1.48.0 rejects the field's absence, `acp-load-replay.jsonl` lines
  3-4). http refs ride only when the agent advertised
  `mcpCapabilities.http` (goose 1.48.0 does; sse it refuses).
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
separate `extensionName` field (`acp-transcript.jsonl` line 43,
`_meta.goose.toolCall`). Audited at tag v1.48.0 — the captures' exact
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

## Trust: why approve, not smart_approve

goose has four modes (`crates/goose-provider-types/src/goose_mode.rs`,
September 2026 tree): `auto` ("Automatically approve tool calls"),
`approve` ("Ask before every tool call"), `smart_approve` ("Ask only for
sensitive tool calls") and `chat` (no tools). Its permission inspector
(`crates/goose/src/permission/permission_inspector.rs`, `inspect`) shows
what the two asking modes actually do with a call that has no user grant
in goose's own permission config:

- `approve`: RequireApproval — always. The capture confirms it asks even
  for `todo__todo_write` (`acp-transcript.jsonl` line 34).
- `smart_approve`: allow a tool annotated read-only; otherwise defer to
  `detect_read_only_requests` — **an extra LLM call** in which goose's own
  model judges whether the call is read-only — and ask only if it says no.

mads' `limited` means "follow the harness": ask before risky things,
reads don't ask — claude-code's normal mode. `smart_approve` reaches that
shape by having a model judge risk: nondeterministic, token-spending, and
exactly the "auto-review" idea a separate trust level would own — not
limited's. So limited (and read_only) stay on `approve`, the only
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
`_goose/unstable/session/system-prompt/set` — works at 1.48.0
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

## Testing without goose installed

- `fakegoose_test.go` re-execs the test binary as a scripted goose
  (`MADS_FAKE_GOOSE=1`). Where acprpc's fake profiles are deliberately
  adversarial, this fake's job is **goose shape fidelity**: the captured
  initialize capability set, the session/new modes + configOptions body,
  `-32602` on missing `mcpServers` (new AND load), bare `_meta` tool
  names, optionIds that mirror kinds (goose's real coincidence), the
  capture's usage numbers, end_turn-after-cancel, and the argv assertion
  `acp --with-builtin developer`.
- `TestConformanceAgainstScriptedBinary` runs the full
  `providers/conformance` suite against that stand-in in CI — every
  declared matrix row, zero real-binary dependency. Cross-process resume,
  recall, and transcript ride one `MADS_FAKE_EXPORT_DIR` store the fake
  rewrites synchronously inside each prompt handler.
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
