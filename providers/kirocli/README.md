# providers/kirocli

The second provider adapter (spec §11, work item W7.1): kiro-cli driven
through its `acp` JSON-RPC agent for interactive sessions, `kiro-cli chat
--no-interactive` for one-shots, `--resume-id` for reattach, and ACP's
`session/load` replay for normalized transcript history.

The ACP transport is the generic `providers/acprpc` engine — this package
carries no JSON-RPC plumbing of its own, only the knob values in
`startACP` (kind-based tool identity, no vendor mode calls, no model
configOption) plus the PTY and plain-CLI strategies. See "Engine
migration" below for the behavior that deliberately changed when the
hand-rolled ACP layer was deleted.

**Every claim below is sourced from kiro-cli's public docs
(kiro.dev/docs/cli/*), its GitHub issues, and agentclientprotocol.com — not
from running the binary.** kiro-cli is not installed on this machine. Until
someone runs the real-binary smoke test (see bottom), treat this as
best-effort research, not fact.

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | `kiro-cli chat` (interactive REPL, PTY) and `kiro-cli acp` (structured JSON-RPC agent over stdio) are both real, documented, first-class entry points. |
| Interrupt | **Native** | Ctrl-C is the standard REPL interrupt for `chat`. For ACP, the standard protocol's `session/cancel` notification is the documented mechanism for stopping an in-flight turn; Kiro's ACP docs describe `session/new`/`session/prompt`/`session/load` as "stable" core methods it implements per the public spec. |
| OneShot | **Native** | `kiro-cli chat --no-interactive --trust-all-tools "<prompt>"` is a real, documented non-interactive invocation flag. |
| Resume | **Native** | `kiro-cli chat --resume` / `--resume-id <ID>` / `--resume-picker` / `--list-sessions` are real, documented flags; ACP's `session/load` is the structured-transport equivalent. Sessions are auto-saved every turn. |
| Transcript | **Native** | Kiro's ACP agent advertises `loadSession: true`. See "Least-certain row" below. |
| ModelDiscovery | **Native** | `kiro-cli chat --list-models --format json` is a real, scriptable, structured command — a genuine improvement over claude-code's pinned-list Emulated declaration. |
| CostReporting | **Unsupported** | Kiro's billing is credit-based, not token-based ("you pay by request, not by token"). No documented flag or ACP field surfaces per-turn token counts; `/context` only shows *percentage* usage, not raw counts or cost. A third-party tool (`kiro-usage`) exists specifically because Kiro exposes no token/cost API and has to reverse-engineer a local SQLite store and approximate input tokens from character count. This is a genuine, citable gap — declared, not emulated with an invented number. |
| ACP | **Native** | `kiro-cli acp` is a real, documented, standards-compliant JSON-RPC 2.0 agent over stdio (newline-delimited JSON, matching the public Agent Client Protocol transport spec). |
| PTY | **Native** | `kiro-cli chat`'s interactive terminal REPL. |
| Branching | **Unsupported** | `/rewind` is real and does create a new conversation branch, but it is an interactive-only TUI turn-picker (arrow-key selection UI), not a scriptable flag or stable API. A GitHub issue on Kiro's ACP crate notes session-fork support is behind an `unstable_session_fork` feature flag and is not currently active (`sessionCapabilities` reports empty). No stable, scriptable surface exists to declare this Native or Emulated honestly. |
| CwdReroot | **Unsupported** | No documented flag or command changes a live session's working directory mid-session. |
| TrustLimited | **Emulated** | kiro's own per-request asks reach the user: on the ACP transport each `session/request_permission` is forwarded to `StartOptions.Permissions` (with no asker wired, the engine's kind gate denies command-execution kinds instead); on PTY, kiro-cli's own interactive confirmation prompt asks the human at the terminal. Neither is a harness-level block the way claude-code's `--disallowedTools` is — hence Emulated, not Native. |
| TrustReadOnly | **Emulated** | The engine's kind gate answers every ask without asking the user, denying both command execution and file mutation kinds. On PTY, the human declining kiro's prompt is the only restriction. |
| TrustAutoReview | **Unsupported** | kiro documents no reviewer — no mode in which anything but the user (or a pre-granted trust) approves a tool call; its ACP modes select agents. An `auto_review` session runs as `limited` instead: `Start` maps it through `provider.EffectiveTrust` (the Runner already does), and the Router reports `effective_trust: "limited"` on the session so the UI shows "Auto-review → Limited" rather than downgrading silently. |
| MCPServers | **Unsupported** | No documented flag or config mechanism threads an MCP server into kiro-cli's own launch was found in its public docs/GitHub issues (same citation bar as the rest of this table — kiro-cli is not installed in this environment). The acprpc engine does now place descriptors on `session/new`'s spec-required `mcpServers` field, but no kiro doc confirms the agent actually connects them, so the declaration stays honest-unsupported: the L2c leader capability (`docs/LINKED-SESSIONS-DESIGN.md` item 5) is not promised to a kiro-cli-backed agent. Revisit with `MADS_KIRO_SMOKE=1` against a real binary. |
| RefAnnounce | **Emulated** | ACP's `session/new` response carries the ref synchronously, before `Start` even returns — no announcement subscription is needed there (Native-strength). The PTY transport prints nothing and has no on-disk transcript format to discover from (unlike claude-code) — a fresh terminal session's ref is simply never learned. Emulated is the honest one-row summary across both. |

Net shape vs. claude-code: kiro-cli is **more capable** on ModelDiscovery,
Interrupt, and Transcript (all Native here vs. claude-code's
Emulated/Native mix), and **less capable** on CostReporting (Unsupported vs.
claude-code's Native) and on both trust rows (Emulated here vs. claude-code's
Native `--disallowedTools`). Branching and CwdReroot are Unsupported for
both. MCPServers is also Unsupported here, unlike claude-code's Native
`--mcp-config` wiring — the L2c orchestrate capability simply doesn't
materialize for kiro-cli sessions. This asymmetry is the point of W7.1 —
the matrix is not a copy of claude-code's.

## Ref announcement, transport by transport

`BehaviorRefAnnounce` (`providers/provider.RefAnnouncer`) lets the runner
persist a session's provider-native ref the moment it's known, instead of
only at turn end.

- **ACP transport:** the acprpc engine sets the session ref from
  `session/new`'s synchronous JSON-RPC response before `Start` returns at all — so
  `Session.Ref()` is already non-empty by the time the caller has a
  session in hand. No `RefAnnouncer` implementation is needed; the runner's
  early-return `if ref := sess.Ref(); ref != ""` path covers this case for
  every provider, not just kiro-cli.
- **PTY transport:** kiro-cli's `chat` REPL does not print its session id
  anywhere on exit (kirodotdev/Kiro#7788), and unlike claude-code there is
  no documented on-disk transcript format this adapter could discover a ref
  from. `ptySession` simply does not implement `RefAnnouncer` — the
  runner's `watchRefAnnounce` treats a missing interface as the same
  "never announces" case as a closed-empty channel, so this never hangs.
  `Provider` also does not implement `providers/provider.SessionLocator`
  (no `LocateSession` method): claude-code's backstop content-correlates
  against its own local `history.jsonl` index and on-disk transcripts,
  and kiro-cli has neither, so there is nothing for a locator to scan. A
  fresh PTY session's ref is simply never learned by any mechanism.

## Trust enforcement, transport by transport

- **ACP transport (structured):** `session/request_permission` is a real
  mid-turn request the agent sends before a tool call runs. The acprpc
  engine's trust gate (with this package's `ToolIdentity: KindBased` knob)
  answers it per the session's trust level ("follow the harness": mads
  asks the user only when kiro itself asks):
  - `limited` with an asker (the Runner always wires one): the request is
    forwarded to the user, unfiltered — kiro's own policy already trusts
    reads by default and asks for the rest (kiro.dev/docs/cli/chat/
    permissions), the claude-code-normal shape, so there is no session
    mode to set and nothing for the engine to second-guess
    (`PermissionAsksEveryCall` off). The reply is the offered `_once`
    option of the user's decision, never a persistent "Trust" option.
  - `limited` without an asker: the engine's kind gate classifies the
    tool call's ACP `kind`
    (`read`/`edit`/`delete`/`move`/`search`/`execute`/`think`/`fetch`/`other`,
    per agentclientprotocol.com) — approves `read`/`edit`/`search`/
    `think`/`fetch`; denies `execute`/`delete`/`move`/`other`.
  - `read_only` approves only `read`/`search`/`think`/`fetch` by kind;
    denies everything else, including `edit` — never asking.
  - `full` (and the zero value) approves everything — unchanged from before
    this row existed.
  The kind comes from the announced `tool_call` update with the same
  `toolCallId`, or — when the agent asks before announcing — from the
  request's own embedded tool call. Wherever the gate decides, an absent
  or unrecognized kind denies — never a silent under-enforce.
  The reply is selected from the request's *offered options* by option
  kind (`allow_once`/`reject_once`, with the `_always` same-polarity
  fallback), since optionIds are agent-chosen opaque strings per spec.
- **PTY transport (terminal):** `--trust-all-tools` is passed only for
  `full` trust (and the zero value). For `limited`/`read_only`, it is
  omitted entirely, so kiro-cli falls back to its own interactive
  per-tool confirmation prompt. **This is a materially different
  enforcement mechanism than the ACP transport's automated kind-based
  deny: it defers to a human at the terminal, not to this adapter.** A
  PTY session with nobody watching the terminal is not actually
  trust-restricted in any enforceable sense — only ACP sessions get an
  automated guarantee.

## IntentMatrix

`require_approval` is **Native**: kiro's own per-request policy decides
what to ask, and both transports put those asks in front of the user —
forwarded over ACP, kiro's own prompt on PTY (`--trust-all-tools` is
withheld below full trust; `TestIntentMatrixDeclarations` pins both).
Like the rest of this table it is doc-sourced, not yet observed against a
live binary. `deny_commands`, `path_scope` and `read_only` stay
**Unsupported**: no command blocklist flag, no path-scoped write
restriction, no flag that blocks writes/commands outright (read_only
trust is the engine's kind gate, a coarser mechanism).

## Real, citable gaps beyond the Matrix

- **PTY sessions never learn their own ref.** kiro-cli does not print its
  session ID on exit from the `chat` REPL (kirodotdev/Kiro#7788, "Display
  session ID and resume command on session exit") — the same limitation
  claude-code's PTY transport has. `ptySession.Ref()` returns
  `StartOptions.Resume` when resuming, empty otherwise.
- **Permission-request timing is still unconfirmed for kiro
  specifically.** The old guessed optionIds are gone (see "Engine
  migration"), but whether kiro announces the `tool_call` before
  requesting permission — and, if it asks first, whether the request's
  embedded tool call carries the `kind` — has not been observed against a
  live binary. The engine handles both orders, and either way an absent
  kind denies below full trust (fail-closed, the opposite of the old
  layer's silent-always-allow failure mode). `MADS_KIRO_SMOKE=1` against a
  real binary is the only way to pin the actual shape.
- **Least-certain row: Transcript.** ACP's `loadSession: true` capability is
  generally understood to mean `session/load` replays a session's prior
  turns as `session/update` notifications before its own response resolves —
  that is the convention this adapter's `transcript.go` commits to. It is
  not directly confirmed for kiro-cli specifically. If kiro's `session/load`
  turns out not to replay history that way, this row should be downgraded to
  Unsupported, not left as a silently-broken Native claim.
- **Design-not-binary risk applies to every Native claim above.** A live
  GitHub issue already shows kiro's documented `session/prompt` field name
  was wrong at one point (`content` vs. `prompt`) — if kiro-cli's real ACP
  implementation has drifted from its own docs elsewhere, this design could
  be wrong in ways only a real binary would catch.

## Conversation keys (unverified)

`stable_keys` is **Unsupported**, and the binding emits **no** keys, live
or stored: the engine's `Keys` knob is left at its zero value (the
conformance probe fails an unsupported row that emits any). kiro-cli isn't
installed, so nothing below has been checked against a binary or a store —
it is the ACP-shape derivation that *would* apply, recorded so a future
capture has something to confirm or refute
(docs/CHAT-STREAM-DESIGN.md §1, providers/acprpc README "Keys"):

- **Text / thinking**: if kiro stamps `agent_message_chunk` /
  `agent_thought_chunk` with a `messageId` per model call (the spec field
  goose and hermes both use), `KeyModeMessageRun` gives
  `<messageId>:<run>`. Whether it repeats ids (goose's ollama ids do,
  hence goose's `created` stamp) or stores them at all (hermes doesn't) is
  unknown.
- **Tools**: `toolCallId`, the spec's own id — native only if kiro's store
  keeps the same id.
- **Stored side**: today's reader is `session/load` replay
  (transcript.go), whose chunks would need to carry the same `messageId`
  as the live ones. goose's replay does; hermes's replay carries none. A
  replay without ids can't key anything, which would leave either an
  on-disk reader (kiro's store format is undocumented) or a hermes-style
  position match.
- **Settle hints**: none until the store's write timing is known.

To flip the row: capture a live `kiro-cli acp` run (text, tool, text; an
interrupt) together with whatever kiro persists for it, compare ids, then
set `Keys`/`Settle` in `engineConfig` and declare Native or Emulated with
the capture cited.

## Engine migration (behavior that deliberately changed)

The hand-rolled ~430-line ACP layer (`wire.go`/`session.go`) is deleted;
the `providers/acprpc` engine speaks the wire now, driven by the knob
values in `startACP`. The ACP v1 spec is normative
(`docs/GOOSE-ADAPTER-DESIGN.md`), so where the old code encoded a guess
the engine contradicts, the engine wins:

- **Permission replies select offered options by kind, never by
  optionId.** The old layer replied with hardcoded
  `optionId: "allow"`/`"reject"` — pure guesses, flagged here as a risk
  that could silently degrade `limited`/`read_only` to always-allow. The
  engine picks from the request's `options[]` by option kind
  (`allow_once`/`reject_once`, `_always` same-polarity fallback, outcome
  `"cancelled"` when no option of the needed polarity exists), which is
  what the spec means the ids to be: agent-chosen opaque strings.
- **No `modelId` param on `session/new`.** The old layer sent
  `{"modelId": …}` — a field the spec's `session/new` params do not have.
  kiro documents no model configOption either, so `ModelConfigOptionID`
  stays empty and an ACP `Start` naming a model now fails wrapping
  `provider.ErrUnsupported` instead of sending a non-spec param and hoping.
  `OneShot`'s and the PTY transport's `--model` flag are unchanged.
- **`session/load` is gated on the advertised `loadSession` capability.**
  The old layer called it unconditionally; the engine refuses a resume
  loudly (typed `ErrUnsupported`) when the agent doesn't advertise it.
  kiro's agent advertises `loadSession: true`, so nothing changes on a
  conforming binary — only the failure mode against a non-conforming one.
- **`mcpServers` is always present on `session/new`/`session/load`** (a
  spec requirement the old layer omitted). The Matrix row stays
  Unsupported — see the row's justification.
- **`StartOptions.SystemPrompt` is no longer silently dropped.** The old
  layer ignored it; the engine's default strategy prefixes it onto the
  first send. kiro documents no system-prompt surface, so emulation is the
  honest alternative to dropping an agent persona on the floor.

## Testing without kiro-cli installed

`fakekiro_test.go` re-execs the test binary as a scripted ACP/chat stand-in
(`MADS_FAKE_KIRO=1`, the standard `os/exec` TestMain trick) — real subprocess
spawn, real JSON-RPC request/response correlation, real `session/cancel`
mid-turn, real `session/load` replay across separate process spawns via a
shared `MADS_FAKE_KIRO_DIR`. Its permission menu offers deliberately
opaque optionIds (`fk-yes`/`fk-no`, never `allow`/`reject`) so a client
that hardcodes ids fails the round trip, and it asks before announcing
the tool call, exercising the engine's embedded-identity gating path.
`providers/conformance`'s full suite
(`TestConformanceAgainstScriptedBinary`) runs against that stand-in in CI —
no kiro-cli required, and no live LLM is ever called.

`TestRealKiroSmoke` is opt-in and exercises the actual binary:

```sh
MADS_KIRO_SMOKE=1 go test ./providers/kirocli -run Smoke
```

It skips cleanly if `kiro-cli` isn't on `PATH`. Running it with a real
install is the only way to confirm or refute this README's Matrix claims.
