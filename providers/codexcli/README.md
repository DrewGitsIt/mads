# providers/codexcli

The OpenAI Codex CLI adapter — the one Drew calls "chatgpt". Registry name
**`codex`**, shown in the UI as **"Codex (ChatGPT)"**: the CLI is `codex`,
and it signs in with a ChatGPT account, so both words are on the label.

It drives `codex app-server` over stdio JSON-RPC. That protocol is codex's
own — threads, turns, items — and does not map onto ACP, so the package
carries a small client of its own (`rpc.go`) instead of riding
`providers/acprpc`. One app-server process hosts one mads session.

**Every claim below is pinned by codex 0.154.0 output** — the provider
survey's captures and this increment's probes, trimmed and scrubbed under
`testdata/captures/` (provenance in its README). Design decisions:
`docs/CODEX-ADAPTER-DESIGN.md`.

## Wire map

| mads | codex app-server |
|---|---|
| `Start` | spawn `codex app-server`, `initialize` (experimentalApi) + `initialized`, then `thread/start` — or `thread/resume` (Resume) / `thread/fork` (Resume+Fork) — with `cwd`, `sandbox`, `approvalPolicy`, `model`, `developerInstructions` (the system prompt, added to codex's own) and a `config` override |
| `Ref()` | the thread id, from the `thread/start` response — known at Start-return |
| `Send` / `SendKeyed` | `turn/start` with `clientUserMessageId` — the caller's id for `SendKeyed`, a minted one for `Send` — (and `effort` when `Config.Effort` is set); **while a turn runs**, `turn/steer` with `expectedTurnId` and the same `clientUserMessageId` — the message joins the running turn and that turn still ends once, the way a mid-turn claude-code message is absorbed |
| `Interrupt` | `turn/interrupt` → `turn/completed` status `interrupted` → `EventTurnEnd{Interrupted}`; open asks answered `cancel` |
| `Compact` | `thread/compact/start` — a turn of its own with a `contextCompaction` item → `EventCompact` start/done, then `EventTurnEnd`; instructions refused (`ErrUnsupported`) |
| `item/agentMessage/delta` | `EventText` keyed by the item (commentary and final answer alike, each its own item with exactly its stored text — nothing joined between two back to back) |
| `item/completed` reasoning | `EventThinking` with the summary, whole, keyed by the item (raw reasoning never reaches a client) |
| `item/started` / `item/completed` commandExecution, fileChange, mcpToolCall, dynamicToolCall, imageView | `EventToolUse` / `EventToolResult`, `Tool.ID` = codex's item id (`exec-…`) — the id approvals and the transcript use, never the model-level `call_…` |
| webSearch | announced at completion, when its query is known (item/started carries an empty one) |
| `thread/tokenUsage/updated` | `Usage` on the turn end: the turn's own tokens (codex's per-process totals differenced), cache reads split out, `ContextTokens` = the last call's `totalTokens`, `ContextWindow` = `modelContextWindow` |
| `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/permissions/requestApproval` | decided by trust (below); forwarded asks become `PermissionAsk{ToolUseID: itemId, Tool: "shell"/"apply_patch", Input, Reason}` |
| other server requests | answered at once (elicitation declined, user input empty, unknown methods `-32601`) — nothing is left waiting |
| `item/completed` (any), `turn/completed` | with `StartOptions.SettleHints` only: `EventSettle` keyed to the item, and an unkeyed one before `EventTurnEnd` |
| other threads' notifications | ignored: a multi-agent run streams sub-agent threads on the same connection |

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | app-server is codex's long-lived structured transport: one process, many turns, streamed deltas (`run1-stdout.jsonl`). |
| ACP | **Native** | The structured transport row; app-server is not ACP, but it is what the row means — structured events, not a terminal. |
| Interrupt | **Native** | `turn/interrupt` ends the turn `interrupted` and the thread serves the next turn (`run1-stdout.jsonl` turn i). |
| Resume | **Native** | `thread/resume` by thread id (~40 ms, survey); the same rollout is appended to. |
| Branching | **Native** | `thread/fork` answers with a new thread id (`forkedFromId` set); the original is not written to. |
| RefAnnounce | **Native** | `thread/start` answers with the thread synchronously. |
| Transcript | **Native** | `thread/items/list` — see "Transcript" below. |
| ModelDiscovery | **Native** | `model/list`, right after `initialize`, no thread; hidden models left out. |
| CostReporting | **Native** | Token counts per turn from `thread/tokenUsage/updated`. Tokens only: codex reports no money, so `CostUSD` stays 0. |
| OneShot / OneShotStream / OneShotResume | **Emulated** | Ride an app-server thread like a session (full trust), not `codex exec` — see "One-shot". |
| MCPServers | **Native** | `config.mcp_servers.<name>` on the thread — session-only, never written to the user's config (probed: codex started the named server). stdio and http; sse has no codex form and is dropped. |
| TrustLimited | **Native** | codex's own sandbox + approval policy asks; mads forwards (below). |
| TrustReadOnly | **Native** | Writes: codex's OS sandbox (Seatbelt / Landlock) refuses them whatever anyone answers. Commands: under a read-only sandbox with `untrusted` approvals codex asks before every one, `ls` included (`readonly-untrusted.jsonl`), and mads declines each ask itself — so nothing runs. (`never` would have run read commands inside the sandbox without asking: still command execution.) |
| TrustAutoReview | **Unsupported** | codex has a reviewer (`approvalsReviewer: auto_review`), but what it does with a call it rejects — deny outright, or escalate to the user as mads' auto_review promises (`thread/approveGuardianDeniedAction` suggests the former) — is not captured. Such sessions run as limited, visibly (`provider.EffectiveTrust`). |
| StableKeys | **Native** | Key = codex's item id, live and in `thread/items/list` (below); user rows `user:<clientUserMessageId>`. The stable_keys probe passes against the stand-in and the real binary (smoke). |
| Compaction | **Native** | `thread/compact/start` (`steer-compact.jsonl`: context 4900 tokens after). |
| CompactionFocus | **Unsupported** | `thread/compact/start` takes only a thread id. |
| PTY | **Unsupported** | codex's TUI could back a terminal transport; not built in this increment. |
| CwdReroot | **Unsupported** | `turn/start` takes a per-turn `cwd`, but mads has no reroot surface to drive it. |
| Delegate* (4 rows) | **Unsupported** | codex has multi-agent threads; no delegate surface is wired, and their notifications are filtered out of the main stream. |

## Trust

codex has two knobs; the adapter sets both on every thread (`trustPolicy`):

| mads trust | sandbox | approvalPolicy | Who decides |
|---|---|---|---|
| full (and unset) | `danger-full-access` | `never` | nobody asks — codex's own `--yolo` pair, claude-code's bypassPermissions |
| limited | `workspace-write` | `untrusted` | codex asks before every edit and every command outside its known-safe read list (captured: `touch a.txt` and an apply_patch add asked; `ls` never does) → the user via `StartOptions.Permissions`; no asker → `decline`, so the turn never stalls |
| auto_review | as limited | as limited | (row Unsupported) |
| read_only | `read-only` | `untrusted` | the OS sandbox refuses writes; codex asks before every command and mads declines each one itself — the user is never asked |
| anything else | as limited | as limited | never falls through to full |

Answers are `accept` / `decline` per call — never `acceptForSession`: mads
asks per call on every harness. A declined call completes `declined`
(`EventToolResult` IsError, status blocked) and the model carries on.

### The trust write mads suppresses

With any writable sandbox, `thread/start` makes codex persist the folder
(or its git root) as `trust_level = "trusted"` in the user's
`~/.codex/config.toml` — probed: workspace-write and danger-full-access both
appended a `[projects."…"]` entry. mads would have marked every worktree it
ever ran in as trusted. Every thread therefore carries the config override
`projects.<resolved cwd>.trust_level = "untrusted"`, which suppressed the
write in every probe, and which also keeps a repository's own
`.codex/config.toml` from loading into an agent mads launched. The sandbox
and approval policy are explicit, so the trust level changes nothing else.
The real-binary smoke checks the user's config is byte-for-byte unchanged.

## Process lifecycle: the tool grandchild

app-server starts in its own process group, and Close kills the group if
it does not exit on stdin EOF. That alone is not enough: codex moves every
tool command (and MCP server) it spawns into a group of **its own** —
captured, `sleep 37` had pgid == its own pid — so a group kill misses
exactly the processes that outlive app-server, and the survey saw a
`sleep 20` survive `kill -9` of app-server. `reap.go` keeps a snapshot of
app-server's descendants (taken 300 ms after each command item starts, and
again at Close) and, once app-server is gone however it went, kills each
one that is still the same process (same pid and pgid), group-wide when it
leads its group. Tests: `TestCloseReapsToolGrandchild` and
`TestCrashReapsToolGrandchild` (the stand-in spawns `sleep 300` in its own
group, exactly like codex; both fail with the reaper disabled), and the
smoke's mid-command Close of a real `sleep 41`.

Limitation: a command started less than 300 ms before app-server crashed
is not in the snapshot.

## Transcript

`thread/items/list` (paged), asked of the session's own app-server while
one runs, else of a short-lived one spawned for the call (~0.8 s; it needs
only `initialize`, no thread load). Not the rollout file
(`~/.codex/sessions/YYYY/MM/DD/rollout-*-<id>.jsonl`): that is codex's
private format, where the user message's `response_item` carries a
different id from its `item_completed`, and injected context
(environment_context, skills_instructions, recommended_plugins,
turn_aborted, world_state/turn_context) is written as user-role messages
that would have to be recognised and dropped by name, forever. The items
list has none of that and the same ids the live stream used. It has no
timestamps, so `Message.Timestamp` stays zero. An interrupted turn's partial
reply is in neither — codex stores an item only once it completes.

`TranscriptSince` reads incrementally on the live session's app-server only
(no live session: `ErrUnsupported`; use `Transcript`). Its cursor is the last
item read plus a codex cursor anchored on it — the `backwardsCursor` of a
one-item descending page, which reads ascending from that item and stays
valid as the thread grows; `nextCursor` is null on the page that reaches the
end, so it can't continue from there. Evidence in `transcript.go`
(`itemsCursor`).

## Conversation keys (chat stream C1)

Every live item event and every transcript row carries `Key`
(docs/CHAT-STREAM-DESIGN.md §1):

| Item | Key |
|---|---|
| agentMessage (commentary, final answer), reasoning, every tool item, contextCompaction | codex's item id (`msg_…`, `rs_…`, `exec-…`) — a tool's use and result share it; never the model-level `call_…`/`ctc_…` |
| userMessage | `user:<clientId>` — the `clientUserMessageId` mads sent on `turn/start` or `turn/steer`; `user:<item id>` when it was sent without one |

Live text is per item and equals the stored item text: two agentMessages in a
row (commentary, then the final answer) are two keyed runs with nothing
inserted between them — the UI's join rule (C4) decides how adjacent text
items read, the same way live and stored. An interrupted partial message is
live only (codex never stores it). Thinking arrives whole at the reasoning
item's completion, so one item's chunks can't interleave with another's.

## One-shot

`OneShot` starts a thread at full trust, sends one turn, returns the
turn's **final answer** (phase `final_answer` — commentary streams through
`OnEvent` but is not the result, so a JSON-parsing caller never sees "I'll
check…" before its JSON), and closes. `Resume` is `thread/resume`; the ref
is announced (`EventSessionRef`) before the reply. `codex exec --json` was
the alternative: one process per turn too, but no deltas (whole items,
`item_0…` ids, `run4-exec`) and a second dialect to parse. A usage- or
rate-limit failure (`codexErrorInfo` `usageLimitExceeded` /
`rateLimitExceeded`) returns `*provider.RateLimitError`.

## Testing without codex installed

- `fakecodex_test.go` re-execs the test binary as a scripted
  `codex app-server` (`MADS_FAKE_CODEX=1`) with codex's shapes: no
  `jsonrpc` member, the response before `turn/started`, clientId echoes,
  approvals as server requests after `item/started`, `declined` items, steer
  inside the running turn, compaction as its own turn, per-process token
  totals. It refuses a writable-sandbox thread without the trust override
  (real codex would write the user's config), runs a gated tool only on
  `accept`, and lets the sandbox fail a read-only write.
  `MADS_FAKE_CODEX_ASK_ALWAYS=1` makes it ask even under `never`, so the
  tests prove read_only and full answer by trust and never forward.
- `TestConformanceAgainstScriptedBinary` runs the full
  `providers/conformance` suite against it: every row above, no binary.
- `wire_test.go` replays the real captures through a session with no
  process behind it — events out, approval replies written back.
- `transcript_test.go` normalizes the real `thread/items/list` captures;
  `keys_test.go` replays the real streams (`run1`, `toolturn`) against
  their items lists — same keys and text on both sides — and covers
  `SendKeyed`, `TranscriptSince`, stale cursors and settle hints. The
  stand-in pages `thread/items/list` with codex's cursor rules.

## Real-binary smoke (opt-in)

```
MADS_CODEX_SMOKE=1 go test -run Smoke ./providers/codexcli/ -v
```

Runs codex in a scratch `CODEX_HOME` under `/tmp` with the real
`auth.json` symlinked in (never copied or read), so the smoke's sessions
and caches never land in the user's codex home;
`MADS_CODEX_SMOKE_REAL_HOME=1` uses the real home instead. Uses the
logged-in codex account and a handful of short gpt-5.6-luna turns
at low effort, in a scratch git repo under `/tmp`, at limited trust
(workspace-write sandbox) with every ask allowed: model list, a marker
turn, an approved write, an interrupt, a mid-command Close that must reap a
real `sleep 41`, the transcript through a fresh app-server, a resume that
recalls the marker, a fork, and a one-shot — and the user's
`~/.codex/config.toml` byte-for-byte unchanged at the end.
`TestRealCodexSmokeStableKeys` runs the conformance stable_keys probe
(`conformance.CheckStableKeys`) against the real binary: a marker reply, a
sentence / `ls` / sentence turn, and an interrupted count to 300, on one
settle-hinted session, from a scratch repo at the probe's default (full)
trust.

## Open seams

- Tool-result text: when a completed commandExecution carries no
  `aggregatedOutput`, the live result falls back to the collected
  `outputDelta`s while the stored row has none. Every capture so far
  carries `aggregatedOutput` (or no output at all), so live and stored
  agree; a capture of the other shape would need the fallback dropped.
- Text chunks are dropped, not waited for, when the consumer is 256 events
  behind (acprpc's policy); live text then falls short of the stored item
  until it settles.
- auto_review: capture what codex's reviewer does with a rejected call.
- `turn/plan/updated` (codex's checklist) and `turn/diff/updated` are not
  mapped; `account/rateLimits/updated` could feed `EventAccount`.
- No `HistoryScanner` (importing existing codex threads) and no PTY.
- `developerInstructions` is re-sent on resume; whether codex replaces or
  appends a thread's earlier ones is not captured.
- The user's own `notify` hook (and other config) still applies to mads'
  threads, as claude-code's hooks do.
