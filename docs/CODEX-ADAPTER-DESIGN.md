# codex adapter — design (P2d)

A fourth provider adapter (spec §11): OpenAI's **Codex CLI** — the one Drew
calls "chatgpt" — driven through `codex app-server`, the stdio JSON-RPC
server codex's own IDE extension talks to. Registry name **`codex`**,
displayed as **"Codex (ChatGPT)"** (`ui/src/features/settings/
providerBehaviors.ts`): the binary is `codex`, and it signs in with a
ChatGPT account, so the label carries both.

**Every claim here is verified against codex 0.154.0 on this machine**:
the provider survey (2026-10-20, `/tmp/mads-provider-survey/codex/
FINDINGS.md`: five turns with a web search and an interrupt, a `kill -9`
mid-tool, a resume, an `exec --json` comparison, and the generated protocol
schema) plus this increment's probes (approvals, steer, compaction, fork,
MCP config, the config-trust write, process groups). The wire evidence is
committed, trimmed and scrubbed, under `providers/codexcli/testdata/
captures/` (provenance in its README). The real-binary smoke
(`MADS_CODEX_SMOKE=1`) runs here on the logged-in account.

## Stance: app-server, not ACP

codex speaks two structured dialects. `codex exec --json` is one process
per turn with whole items and no deltas (`run4-exec`). `codex app-server`
is the long-lived one: threads, turns and items; streamed deltas;
`turn/interrupt`; `turn/steer`; `thread/resume`, `thread/fork`,
`thread/compact/start`; paged `thread/items/list`; and approvals as
server→client requests. An ACP bridge exists (`@zed-industries/codex-acp`,
0.16.0 on npm) but is a third party's translation of this same server,
not installed here, and would hide exactly the parts that make codex
worth adapting (steer, items list, fork, compaction).

app-server's thread/turn/item model does not fit `providers/acprpc`'s ACP
engine — there is no session/prompt, no session/update, and approvals are
typed per item kind — so the package carries its own small client
(`rpc.go`, about the size of acprpc's wire layer). What it shares with the
other adapters is the provider contract, `agentenv`, `binlocate`, and the
conformance suite.

**Decisions inherited, not relitigated**: the opaque conversation keys of
docs/CHAT-STREAM-DESIGN.md §1 (C1). Every event and transcript row carries
`Key` = codex's item id; a user row's key is `user:<clientUserMessageId>`
(see "Conversation keys" below).

## Shape of the work

`providers/codexcli` (registry `codex`): `rpc.go` (process + JSON-RPC),
`reap.go` (tool-process reaper), `session.go` (turns, events, approvals),
`items.go` (ThreadItem → ToolUse/result), `codexcli.go` (Provider, Matrix,
Start, trust, thread config, Models), `transcript.go`, `oneshot.go`;
tests: `fakecodex_test.go` (scripted stand-in), `codexcli_test.go`
(conformance + behavior), `wire_test.go` (capture replay),
`transcript_test.go`, `reap_test.go`, `smoke_test.go`.

## Verified binary surface

| Need | Mechanism (verified) |
|---|---|
| Handshake | `initialize {clientInfo, capabilities:{experimentalApi:true}}` → `{userAgent, codexHome, platformFamily, platformOs}`; then the `initialized` notification. No `jsonrpc` member either way. |
| New session | `thread/start {cwd, sandbox, approvalPolicy, model?, developerInstructions?, config?}` → `{thread:{id, path, …}, model, sandbox, approvalPolicy, …}` synchronously, then `thread/started`. The thread id is the ref. |
| Resume | `thread/resume {threadId, cwd, …, excludeTurns:true}` (~40 ms); appends to the same rollout. |
| Fork | `thread/fork {threadId, cwd, …}` → a new thread with `forkedFromId`. |
| Turn | `turn/start {threadId, input:[{type:text,text}], clientUserMessageId, effort?}` → `{turn:{id, status:inProgress}}` **before** `turn/started`; then `item/started`/deltas/`item/completed`; `thread/tokenUsage/updated`; `turn/completed {turn:{status, error}}`. |
| Mid-turn message | `turn/steer {threadId, expectedTurnId, input, clientUserMessageId}` → `{turnId}`; a userMessage item lands in the running turn, which still completes once. |
| Interrupt | `turn/interrupt {threadId, turnId}` → `{}`; `turn/completed` status `interrupted`; the partial agentMessage never completes and is never stored. |
| Compaction | `thread/compact/start {threadId}` → `{}`; its own turn with a `contextCompaction` item; `tokenUsage.last.totalTokens` is the compacted size. No instructions parameter. |
| Approvals | server requests `item/commandExecution/requestApproval` and `item/fileChange/requestApproval` (after the item's `item/started`, keyed by `itemId`), `item/permissions/requestApproval`; answered `{decision: accept|decline|cancel|acceptForSession}`; codex then sends `serverRequest/resolved`. |
| Transcript | `thread/items/list {threadId, limit, cursor, sortDirection?, turnId?}` — paged, same item ids as live, works on an unloaded thread right after `initialize`; `-32601` for ephemeral threads. `nextCursor` is null on a page that reaches the end (even a full one); `backwardsCursor` is anchored on the page's first item, included, and stays valid as items are appended (probed 2026-10-21). |
| Models | `model/list` → `{data:[{id, displayName, hidden, isDefault, supportedReasoningEfforts…}], nextCursor}`, no thread needed. |
| MCP servers | `config.mcp_servers.<name> = {command, args, env}` or `{url}` on the thread — session-only. |
| Usage | `thread/tokenUsage/updated {tokenUsage:{total, last, modelContextWindow}}`: `total` is per app-server **process** (a resumed thread restarts at 0), `inputTokens` includes `cachedInputTokens`, `outputTokens` includes `reasoningOutputTokens`. No money. |

## Capability matrix

Row-by-row reasons live in `providers/codexcli/README.md`; the summary:

| Native | Emulated | Unsupported |
|---|---|---|
| Interactive, ACP (structured), Interrupt, Resume, Branching, RefAnnounce, Transcript, ModelDiscovery, CostReporting (tokens), MCPServers, TrustLimited, TrustReadOnly, Compaction, StableKeys | OneShot, OneShotStream, OneShotResume (an app-server thread, not `codex exec`) | TrustAutoReview (reviewer unproven), CompactionFocus, PTY (not built), CwdReroot, the four Delegate rows |

## Session lifecycle

One app-server per session, spawned in the session cwd with
`agentenv.Build`'s environment (isolated mode passes `CODEX_HOME`), in its
own process group. Start runs the handshake and `thread/start|resume|fork`;
Ref is known at Start-return. The read goroutine is the sole writer to the
events channel and closes it when stdout ends — no turn end before that is
the process-died signal.

Turn state is learned from the wire, in wire order: Send marks the turn
open before writing `turn/start`; `turn/started` supplies the turn id;
`turn/completed` closes it. A Send while a turn is open becomes
`turn/steer` with that id (waiting briefly for the id if the response
landed before `turn/started`); if the steer loses a race with the turn's
end, a fresh `turn/start` follows. This matches claude-code, whose
mid-turn message is absorbed by the running turn. Compaction is refused
mid-turn (`ErrTurnInFlight`), and a Send during a compaction turn is too.

Every `turn/completed` for the session's thread emits exactly one
`EventTurnEnd` (the turn-termination contract), preceded by the compaction
outcome when it was one and an `EventError` when it failed. Notifications
for other threads — a multi-agent run's sub-agents — are ignored.

### Item → Event mapping

- `item/agentMessage/delta` → `EventText` keyed by the item; an item that
  completes with more text than its deltas carried emits the rest.
  Commentary and final answer are both text, each its own keyed item with
  exactly its stored text: nothing is joined between two messages back to
  back (until C1 the adapter inserted a paragraph break; the UI's join
  rule now decides, the same live and stored). The final answer is also
  kept per turn for OneShot.
- reasoning `item/completed` → `EventThinking` with the summary parts,
  whole (summary deltas are not forwarded), keyed by the item.
- commandExecution, fileChange, mcpToolCall, dynamicToolCall, imageView:
  `item/started` → `EventToolUse` (id = item id; kind from codex's own
  `commandActions` parse — reads and listings are `read`, searches
  `search`, anything else `execute`; fileChange is `edit`/`delete` with
  `Mutates`), `item/completed` → `EventToolResult` (output from
  `aggregatedOutput`, else the collected `outputDelta`s; `declined` →
  IsError + blocked; non-zero exit → failed).
- webSearch: one `EventToolUse` + result at completion (its started item
  has an empty query).
- contextCompaction: `EventCompact` start/done, auto or manual.
- With `StartOptions.Keyed`: `EventSettle` (Key = the item's key)
  after every `item/completed` of the thread — codex stores an item as it
  completes it, 0–27 ms behind — and an unkeyed one before each
  `EventTurnEnd`. Never without the option.

### Conversation keys

Key = codex's item id for every item kind (`msg_…`, `rs_…`, `exec-…`, the
compaction item's id): the id on `item/started`, the deltas,
`item/completed` and `thread/items/list` alike. A tool's `EventToolUse`
and `EventToolResult` (and its tool / tool_result rows) share it; the
model-level `call_…`/`ctc_…` ids are never used. A user row's key is
`user:<clientId>` — the `clientUserMessageId` mads sent on `turn/start`
or `turn/steer` (both take one; `SendKeyed` passes the caller's id, `Send`
mints one) — or `user:<item id>` for a message sent without one. An
interrupted partial agentMessage is live only: codex never completes or
stores it. Pinned by the conformance stable_keys probe against the
stand-in and, in the smoke, the real binary.

## Trust enforcement (Native)

codex has the two knobs mads needs: an OS sandbox (`read-only`,
`workspace-write`, `danger-full-access`) and an approval policy
(`untrusted`, `on-request`, `never`, granular). The mapping:

| trust | sandbox | approvalPolicy |
|---|---|---|
| full / unset | danger-full-access | never |
| limited | workspace-write | untrusted |
| auto_review | as limited (row Unsupported) | |
| read_only | read-only | untrusted (every ask declined by mads) |
| unknown | as limited | |

`untrusted` asks before every edit and every command outside codex's
known-safe read list (captured: `touch a.txt` and an apply_patch add
asked, `ls` never did) — the shape of mads' limited ("ask before risky
things, reads don't ask"). Asks go to `StartOptions.Permissions` on a
goroutine of their own; no asker → `decline`, so a headless turn never
stalls; read_only declines every ask itself (under a read-only sandbox
codex asks before every command, `ls` included — `readonly-untrusted.jsonl`
— so no command runs, and the sandbox refuses writes regardless); full
accepts. read_only first rode `never`, which runs read commands inside the
sandbox without asking: command execution all the same, which the
read_only row denies.
Decisions are per call (`accept`/`decline`), never `acceptForSession`.
`on-request` was rejected for limited: there the model decides when to
ask, which is not "codex asks before risky things".

auto_review: codex has an approvals reviewer (`approvalsReviewer:
"auto_review"`, `item/autoApprovalReview/*` notifications,
`thread/approveGuardianDeniedAction`), but the capture that would show
whether a rejected call escalates to the user (mads' contract) or is just
denied (what the override method suggests) does not exist yet.
Unsupported until it does; sessions run as limited, visibly.

### Folder trust is never written

Probed 2026-10-20 with `~/.codex/config.toml` backed up and restored
around each run: `thread/start` with sandbox `workspace-write` or
`danger-full-access` appends `[projects."<realpath or git root>"]
trust_level = "trusted"` to the user's config; `read-only` and no sandbox
do not. mads would have trusted every worktree it ever ran in, and with it
loaded each repository's own `.codex/config.toml` into later codex runs.
A thread `config` override `projects.<resolved cwd>.trust_level =
"untrusted"` suppressed the write in every probe (also for a cwd below a
git root). Every thread carries it; the stand-in refuses a writable thread
without it; the smoke asserts the user's config is byte-for-byte
unchanged.

## Process lifecycle: reaping tool processes

Captured: codex puts every tool command, MCP server and helper it spawns
in a process group of its own (`sleep 37`: pgid == its pid; app-server's
group different). So `killpg(app-server)` misses them, and after
app-server dies they reparent to init — the survey's `sleep 20` outlived
`kill -9`. `reap.go` snapshots app-server's descendants (`ps -A -o
pid,ppid,pgid`, walked from app-server's pid) 300 ms after each command
item starts and again at Close, and when app-server is gone — clean exit,
SIGKILL after the 3 s grace, or a crash noticed by the read loop — kills
each remembered process that still has the same pid and pgid, group-wide
when it leads its group. It never signals pid ≤ 1, its own group, or
anything when it has no process of its own.

Evidence: `TestCloseReapsToolGrandchild` and `TestCrashReapsToolGrandchild`
(the stand-in spawns `sleep 300` in its own group, like codex; both fail
with `reap` disabled), and the smoke closes a session mid-`sleep 41` and
requires it gone.

## Transcript normalization

`thread/items/list`, paged, from the live app-server when the session is
open, else from a short-lived one (~0.8 s). The rollout file was the
alternative and was rejected: codex's private format, where the user
message's `response_item` id differs from its `item_completed` id and
injected context (environment_context, skills_instructions,
recommended_plugins, turn_aborted, world_state/turn_context) appears as
user-role messages to be filtered by name forever. The items list has
neither trap and uses the live ids. Cost: no timestamps
(`Message.Timestamp` zero). Every row carries its item's key.

`TranscriptSince` (the session child's settle reads) runs only on the live
session's app-server — with none it is `ErrUnsupported` and the caller
uses `Transcript`. Its cursor is the last item returned plus a codex
cursor anchored on it: the `backwardsCursor` of a one-item descending page,
which lists ascending from that item, itself included, and stays valid as
the thread grows. Each read drops the anchor (checking it is the item the
cursor named, else `ErrCursorStale`) and re-anchors on the new last item.
`nextCursor` couldn't serve: it is null on the page that reaches the end. Roles: userMessage → user, agentMessage →
agent, tool items → tool + tool_result (no result while still
`inProgress`), contextCompaction → compact; reasoning, plan, review and
hook items are skipped.

## OneShot

A thread at full trust, one turn, close — Emulated, like goose's over
ACP. `exec --json` would have been a second dialect with no deltas.
`Text` is the turn's final answer (phase `final_answer`), so commentary
never precedes a JSON reply; `Ref` is announced before the reply and
`Resume` is `thread/resume`. A usage or rate limit
(`codexErrorInfo` usageLimitExceeded / rateLimitExceeded) returns
`*provider.RateLimitError`.

## Registration & plumbing (done)

1. `providers/codexcli` + `pkg/binlocate` (codex's standalone-installer
   link and release path, and the CLI inside ChatGPT.app).
2. `cmd/mads/run.go` `-provider codex` (+ `-codex-args`), and the Router,
   session and pipeline registries (isolated mode passes `CODEX_HOME`).
3. `pkg/preflight`: binary `codex`, `codex --version` → `codex-cli
   0.154.0`; compat window 0.154.x (Max 0.155.0 exclusive).
4. UI: the "Codex (ChatGPT)" product label (Settings, model picker groups,
   onboarding), and a prompt-in-a-window harness mark on the rail.
5. Context window: runtime only (`modelContextWindow` → turn-end
   `Usage.ContextWindow`); no static table rows — codex model ids churn.

## Risks / open questions

- **Protocol churn**: app-server moves with the CLI and part of what the
  adapter uses rides `experimentalApi`. Mitigation: the one-minor compat
  window and the smoke.
- **auto_review** (above): capture a reviewer rejection.
- **Crash window**: a command started under 300 ms before app-server
  crashed is not in the reaper's snapshot.
- **developerInstructions on resume**: re-sent; whether codex replaces or
  stacks a thread's earlier ones is not captured.
- **User config still applies**: the user's `notify` hook, MCP servers and
  plugins load into mads' threads, as claude-code's settings do for it.
- The Trust pill's generic copy says an Unsupported reviewer means "has
  no reviewer"; for codex it means "not proven".

## Future work (not this increment)

- PTY transport over codex's TUI (`codex`, `codex resume <id>`).
- `HistoryScanner`: import existing codex threads (`thread/list`).
- `turn/plan/updated` → the checklist widget; `turn/diff/updated`;
  `account/rateLimits/updated` → `EventAccount`.
- Delegates: codex's multi-agent threads (`collabAgentToolCall`,
  `subAgentActivity`) as mads delegates.
