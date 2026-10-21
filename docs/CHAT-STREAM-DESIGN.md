# Chat stream: one keyed conversation, replayable live tail

Status: **proposed** (2026-10-20), revised after the four-provider survey
(claude code 2.1.285, codex 0.154.0, goose 1.50.1, hermes 0.22.2; raw captures
and per-agent findings in `/tmp/mads-provider-survey/<agent>/FINDINGS.md`).
Mockup for the text join rule: `docs/mockups/chat-text-blocks.html`.

## Problem

The chat pane stitches two feeds that share no identity:

| | Live `/output` | Stored `/transcript` |
|---|---|---|
| Owner | session service, ephemeral | the agent's own store (claude JSONL); for ACP agents today, acprpc's in-memory buffer (`runner/transcript.go:65–70`) |
| A new connection gets | only frames from now: "no replay/cursor" (`services/session/internal/api/api.go:231`); SSE `id` restarts at 1 per connection | everything written so far |
| The user's message | not on it; mads records the text nowhere (`runner.go:903–962`) | written by the agent some time after the send (claude: ~110 ms warm, **~1 s cold**) |
| Assistant text | deltas with no item identity or boundary (`providers/claudecode/wire.go:647–669`) | one row per item, written after the item finishes |
| Thinking | inline as it arrives | claude reader drops it; mads re-merges `session.thinking` events by timestamp |

The UI renders `messages` (stored) + `liveItems` (streamed) and swaps one for
the other at turn end. Four mid-turn paths replace `messages` wholesale
(stream `open`, tab refocus, `link.message_injected`, mount), so nothing
keeps the two layers from overlapping. Reproduced on claude (2026-10-20,
evidence in `/tmp/mads-chat-repro/`, recipes in the test plan):

1. **Duplicate reply.** Agents store each finished item mid-turn, so any
   mid-turn refetch puts the turn's earlier items under the live items, which
   still hold them. Both show until turn end.
2. **The user's message vanishes until turn end.** On a cold start the stream's
   `open` refetch ran ~20 ms after the send and found the transcript without
   the message; the bubble was gone for the rest of the turn. Tab refocus does
   the same. In a multi-hour supervisor turn, that's hours.
3. **Switching sessions loses the turn so far.** `SessionWorkspace` is keyed
   by session (`SessionsSurface.tsx:274`), so a switch remounts the pane; the
   new stream has no replay, the transcript lacks the in-flight item.
4. **A jump at turn end.** Live text for the whole turn is one entry; stored,
   it is split per item, with bubble gaps (~30 px) where the live view had
   paragraph gaps, and markdown that parses differently once split (a heading
   glued to the previous sentence, a list split in two). Any difference
   between the live and stored *text* of an item (hermes prefixes `"\n\n"`
   live, not stored) shows the same way.

The architecture premise, "current state is always recoverable from the
control plane" (REBUILD-SPEC, "Two Communication Planes"), doesn't hold for
conversation content: the turn in flight exists nowhere mads owns.

## Goals

- One identity for every piece of a conversation, the same live and stored,
  **for every provider**, not shaped around one agent's wire format.
- A viewer that connects, reconnects or switches back mid-turn sees the whole
  turn so far, immediately, with no duplicates.
- The user's message shows from the moment it's accepted and never vanishes.
- Live and stored render identically, so turn end changes nothing on screen.
- Cost stays flat in turn length (multi-hour supervisor turns).

## Non-goals

- **A mads-owned conversation log.** The agent's own store stays the only
  durable history. mads keeps only the *unsettled tail* in memory: what the
  agent hasn't stored yet. For ACP agents, acprpc's in-memory buffer becomes
  exactly that tail (memory only, capped, dies with the session child), not a
  transcript source.
- Recovering what the agent itself never stores. A child killed mid-turn loses
  the in-flight item (claude loses the whole in-flight message, including a
  thinking block finished seconds earlier; goose and hermes lose the partial
  text); codex never stores an interrupted partial at all. The tail shows those
  only while the child that streamed them lives.

## What the providers actually do

Measured 2026-10-20 (findings files above). These facts drive every section
below; the design must hold for all four columns.

| | claude code | codex | goose | hermes |
|---|---|---|---|---|
| Transport | stream-json (persistent `-p`) | `codex app-server` JSON-RPC | ACP (`goose acp`) | ACP (`hermes-acp`) |
| Store | JSONL, append-only | rollout JSONL + SQLite, append-only; paged `thread/items/list` | SQLite, insert-only | SQLite `state.db`, **rewritten on load/compaction** |
| Native identity, same live and stored | message id + block index (after de-duping re-appended lines by `uuid`) | item id (one item = one block) | `toolCallId`; text `messageId` collides under Ollama (`chatcmpl-0..999`) | none: live `messageId` is never stored; stored `message_uid` matched by position |
| Text per message/item | one (0 multi-text messages in 16,250) | one per item; commentary and final answer are separate items | one per model call | one per model call |
| Store lag | message ~30 ms after stop; user ~1 s cold | <30 ms; only completed items | tool call and the text before it: **after the tool finishes** (6 s for `sleep 6`) | 10–200 ms; pre-tool text ~1 s |
| User message id round-trips | yes (stdin `uuid`) | yes (`clientUserMessageId`) | no (text identical) | no (whitespace stripped; rewritten after a cancel) |
| Mid-turn send | `queued_command` attachment, written at the next tool result | `turn/steer` | queued by mads | queued prompt, echoed when drained |
| Live ≠ stored text | — | — | — | leading `"\n\n"` live after a tool |
| Live-only / stored-only rows | interrupt markers; "No response requested." after a resume | interrupted partials (live only); injected context (rollout only) | hidden `<turn-context>` row (store only) | `failed_turn` "Your request was not processed." (store only) |

Kiro (ACP) isn't installed; its column is unverified and stays so until a
binary is available.

## Design

### 1. Keys: an opaque, provider-supplied identity

The provider layer owns identity. Every item an adapter emits, live or from its
`TranscriptReader`, carries a `Key`: an opaque string, unique within the
session, that the adapter **derives from the agent's native ids** (it never
invents one where the agent has one). Above the adapter (session service,
Router, UI) keys are only compared for equality.

**The contract**, enforced by a new conformance probe: for a short scripted
turn (plain reply; text, tool, text; an interrupt), every item that is both
streamed and stored has the **same key and the same text** on both sides, and
the store lists items in conversation order. An adapter that can only meet the
contract partly says so in its matrix row (`native` / `emulated`), as for any
other behaviour.

Per-provider derivation:

| Item | claude | codex | goose | hermes |
|---|---|---|---|---|
| Assistant text / thinking | `<message_id>:<index>` (index counted after de-duping by `uuid`, including signature-only thinking) | item id | `<messageId>@<created>:<run>` (the `created` stamp disambiguates colliding ids) | while the child lives: the live `<messageId>:<run>`, which the reader puts on the stored row it matches by position (the Nth model call of the turn = the Nth new assistant row); after the child: stored `message_uid` (see "Hermes" below) |
| Tool call / result | `tool_use_id` | item id | `toolCallId` | while the child lives: the live `tc-…`, matched to the stored calls in order; after the child: stored `call_…` id |
| User message | `user:<mads id>`, sent as stdin `uuid` | `user:<mads id>` via `clientUserMessageId` | `user:<mads id>` live; matched on settle | `user:<mads id>` live; matched on settle |
| Mid-turn send | `user:<mads id>` via `queued_command.source_uuid` | `user:<mads id>` via `turn/steer` | as user message | as user message |
| Synthetic rows | the row's own `uuid` | item id | row id | `message_uid` |

**User messages.** mads mints the id when a send is accepted. Where the agent
stores it back (claude, codex) the key survives restarts with no matching.
Otherwise the session child matches on settle: the **next unlabelled user row
after the previous settled user row**, by position, with text equality only as
a sanity check (hermes rewrites text, so text can't be the primary match). Rows
that predate this change or come from sessions resumed outside mads get a key
from the row's native id.

**Text equality** is part of the contract, so an adapter normalises live text
to what the agent stores (hermes: drop the injected leading `"\n\n"`). That is
what makes the swap at settle invisible. Where normalising changes what a
consumer sees today (hermes swaps a tool's title for its stored name and its
rendered result for the stored output), the adapter does it only for a
session started with `StartOptions.Keyed`, the keyed consumer's opt-in.

**Hermes keys are stable per session child, not per conversation**
(`stable_keys: emulated`). hermes stores no id that existed live: the live
`messageId` is never written, and `message_uid` exists only once the row is.
So live items carry provisional keys, the adapter keeps a ledger of what each
turn streamed, and its readers match stored rows to it by position and serve
matched rows under the live key for as long as that child lives. A stored row
of the in-flight turn with nothing live to match yet (hermes writes a
tool-call row a few ms before the tool-call frame), and the rows after it, are
held back by `Transcript` and `TranscriptSince` alike, so no reader sees a
row under two keys while the child lives. When the child exits the ledger goes
with it and the same rows read under `message_uid` and `call_…`: a viewer
**re-reads the transcript and replaces hermes items** at a new epoch rather
than merging by key across it (the tail is gone then anyway, §3). Limits: a
turn whose stored user row doesn't read back as the text sent isn't matched
(its rows keep native keys), and text rows are matched by count.

### 2. Frame contract on `/output?v=2`

*Built (C2); this section is the contract C4 builds on.* The keyed stream is
opt-in: `GET /output?v=2` on the session child, or through the Router's
`GET /sessions/{id}/output?v=2`, which forwards `?v=`, `?after=` and the
`Last-Event-ID` header and flushes every frame as the child writes it.
`GET /output` with no `v` is the legacy stream, unchanged (same kinds, data,
per-connection numeric ids and text-join shim; settle hints never reach it).
Terminal (PTY) bytes stay on the legacy stream only.

**Framing.** Every frame is SSE:

```
id: <epoch>:<seq>
event: <kind>
data: <one-line JSON, always with "kind">
```

`epoch` is 8 hex characters minted when the session child starts (any
restart is a new epoch); `seq` is one counter per child, shared by every
subscriber, so the same frame has the same id on every connection. Two frames
carry **no `id:` line**, so they never move the client's `Last-Event-ID`:
`hello` and `resync`.

**`hello`** is the first frame of every connection:

```json
{"kind":"hello","epoch":"1f3a9c0e","seq":412,"provider":"claude-code",
 "keyed":true,"resume":"fresh",
 "turn":{"active":true,"started_at":"2026-10-21T12:00:00Z","interrupted":false},
 "queued":[{"id":"q_…","text":"…","queued_at":"…"}]}
```

- `seq`: the journal head when the connection attached; the replay brings the
  client up to it.
- `keyed`: false when the provider's matrix doesn't declare `stable_keys`
  working (kiro). Keys are then runner-local (`local:<n>`, `tool:<call id>`),
  each `turn_end` is followed by `resync{reason:"unkeyed"}`, and the client
  refetches the transcript at turn end instead of merging by key.
- `resume`: `fresh`, `resumed` or `resync` (§5).
- `turn`: whether a turn (a send, an inbound wake, a compaction pass) is in
  flight; `started_at` when it began; `interrupted` once Interrupt was asked
  of it. `queued`: the user's queue, the shape of `contracts.QueueChanged.Queued`
  (changes still arrive as `session.queue_changed` events).

**Conversation frames.** Keys are opaque (§1); compare only for equality.

| kind | data | when |
|---|---|---|
| `user_message` | `{key, text, attribution?, queued?, ts}` | a send is accepted: direct send, queue delivery (`queued:true`), linked-session inject, mads's notice and digest injections. Before the provider sees it, so it precedes the turn's frames (and follows the previous `turn_end`). |
| `item_start` | `{key, item:"text"\|"thinking"\|"tool_use"\|"notice", parent_tool_use_id?, subagent_type?, task_description?}` | once per key, ever |
| `text_delta` / `thinking_delta` | `{key, text}` | text as the adapter keyed it (before the legacy join shim) |
| `item_stop` | `{key}` | a text or thinking item ends: the key changes, a tool starts, or the turn ends (also when the child exits mid-turn) |
| `tool_use` | `{key, tool}` | `tool` is today's ToolUse JSON with its synthesized status; a repeat for the same key is an update |
| `tool_result` | `{key, text, is_error, tool, truncated?}` | live: the full text. Replayed from the journal: a 2 KB preview with `truncated:true` |
| `notice` | `{key, notice_kind, text}` | reserved: no live producer yet (stored notices arrive as `/transcript` rows with `notice:true`) |
| `inbound` | today's rendered inbound body plus `key` when the provider gave one | a peer message reached the agent |

`user_message` keys: mads mints a UUID when it accepts a send (for a queued
message, when it is queued) and the key is `"user:"+id`; a provider that
implements `provider.KeyedSender` (claude, codex, the fake) stores the row
under that same key. `text` and `attribution` are exactly what `/transcript`
renders for the stored row: the wire the runner hands the provider, through
`linkwrap.Expand`. A send Expand splits into n > 1 segments (a coalesced
digest run, a re-root note ahead of its block) is one frame per segment,
keyed `linkwrap.SegmentKey("user:"+id, i, n)` = `"user:<id>#<i>"` (0-based,
in Parse order, the outside-the-wrapper notice included) — the same rule as
the stored rows.

A tool's call and result share its key; `item_start` comes before the
`tool_use`, and there is no `item_stop` for a tool. A subagent's items
(`parent_tool_use_id` on their `item_start`, their own keys) are in the tail
like the main agent's, so a viewer switching back mid-turn keeps delegate
activity; the main store never holds them, so they end `unsaved` (§3).

**Tail bookkeeping frames.**

| kind | data | meaning |
|---|---|---|
| `settled` | `{keys:[…]}` | the store now has these keys: render them from `/transcript`, drop the live copies. Never in a replay of the tail again. |
| `unsaved` | `{keys:[…]}` | the agent will never store these: keep showing the live copy (the agent's own UI loses them on a restart too). Sent ~1 s after a turn ends for its unstored items (not user messages, which wait for their row) — codex's interrupted partial, a subagent's items, thinking from a provider whose `stored_thinking` is unsupported (or a thinking item a native one didn't store) — for every in-flight item when the child's provider process exits mid-turn, and for a send that failed. |
| `tail_truncated` | `{dropped, keys}` | in a replay only: the cap evicted these items (whole); fetch the transcript for them. An item still streaming is evicted only when nothing else can go; its later frames then replay behind a stand-in `item_start` with no `id:` |
| `resync` | `{reason:"epoch"\|"gap"\|"unkeyed"}` | refetch the last `/transcript` page and replace items by key; no `id:` |
| `replay_end` | `{}` | the replay is over; everything after is live (pace only from here) |

**Status frames** keep today's data (the `provider.Event` JSON) and are
numbered like the rest: `turn_end, error, permission_request, task,
task_roster, background, compact, account, subagent_usage`, plus the
thinking-token tick (`thinking` with `thinking_tokens` and no text) and any
other kind the legacy stream carries, except `pty_output` and `settle`.

**The keyed start option.** Sessions start with `StartOptions.Keyed` (settle
hints) where the provider's `stable_keys` works: claude, codex, goose and the
fake (`native`), and hermes (`emulated`; since C4a). Under Keyed hermes
streams tool names and results as it stores them, so the live and stored
items are identical; the legacy stream shows that too.

### 3. The unsettled tail (session service, memory only)

The hub keeps, per session child, an ordered buffer of chat frames **whose key
the agent's store doesn't have yet**.

- **Settling is driven by the adapter.** Each adapter marks the events after
  which its store has likely caught up (claude: assistant `message_stop`,
  tool result, `result`; codex: `item/completed`; goose: tool completion and
  the prompt response; hermes: the tool-call frame and the prompt response).
  ~250 ms after a settle hint, after a `user_message`, at turn end, and
  whenever it serves `/transcript`, the child reads the store **incrementally**
  through a new optional reader method, `TranscriptSince(ref, cursor)`, with an
  opaque adapter cursor (file offset for JSONL; rowid for goose; for hermes,
  the rows after the last settled `message_uid`, since its row ids aren't
  stable). Frames whose key is now stored are dropped.
- **Ordering.** Every surveyed store writes in conversation order, so the tail
  always follows the stored items; the conformance probe checks it.
- **Duration and size.** The tail lasts as long as the agent holds an item
  back: under a second for codex and hermes, the length of the running tool
  for goose (tool call and preceding text) and for claude's mid-turn sends.
  A multi-hour tool can keep a small tail unsettled for hours; its *size*
  stays the in-flight items. A hard cap (4 MiB, like the PTY ring) bounds the
  pathological case: the oldest frames drop and a `tail_truncated` marker
  takes their place (the viewer then knows to fetch the transcript).
- **Never-stored items** (codex's interrupted partial, a subagent's items,
  thinking a provider doesn't store) stay in the tail, marked `unsaved`, but
  not for long: once a later turn has ended, an earlier turn's unsaved items
  leave the tail (into the retained journal, so a resume still has them). A
  fresh viewer gets the latest finished turn's unsaved items and the current
  turn's, never a pile-up. They're absent after a restart, as in the agent's
  own UI.
- **Thinking** settles by key like any item, against the keyed store read
  (`provider.KeyedTranscriptReader`: Role `thinking` rows, where the
  provider's `stored_thinking` is native). `session.thinking` events stay as
  today, as the record only where `stored_thinking` is unsupported; nothing
  settles against them, and they carry no keys — mads's event log is not a
  second copy of the conversation.
- **Tool-result bodies** are kept as a 2 KB preview in the tail; the live frame
  carries the full text as today, and the stored row has it once settled.
- **ACP.** acprpc's in-memory buffer stops being the transcript; it supplies
  the tail. `/transcript` for goose and hermes reads the agent's store (goose
  SQLite or `session export`; hermes `state.db`), whether or not a child runs.
  Where that means reading a private schema, the preflight version window is
  the guard (it already caught goose's silent update to 1.50.1).

### 4. `/transcript` carries keys

- Every row carries `key`. Thinking comes from the store where the agent keeps
  it (claude JSONL with summaries on, codex reasoning summaries, goose,
  hermes) and is placed by key; `session.thinking` events stay only as the
  fallback for agents that don't store it.
- Stored-only rows (goose's hidden context row, codex's injected context) are
  filtered by the adapter; stored-only rows the agent shows its own user
  (hermes `failed_turn`) come through as `notice` rows.
- **Pagination**, which `?page=` pretended to do: `GET /transcript?before=<key>&limit=<n>`
  returns the `n` rows before `key` (default: the last `n`). The adapter maps
  it (codex: native `thread/items/list` paging; others: their reader).
  *Built (C3)*: paged in the serving layer over the rendered rows
  (`pkg/transcriptpage`), not yet by the adapter. `before` names the first
  row with that key; a page grows back so a call and its result never split,
  so `rows[0].key` is the next cursor. Answers `{rows, has_more}` only when
  paging; split user rows get `<key>#<i>` sub-keys; 410 for an unknown key,
  400 for `before` over unkeyed rows (contract: `contracts.TranscriptPage`).
- The Router's stored-ref fallback (no live child) reads the same keyed rows;
  there's no tail to merge then.
- *Built (C2)*: the keyed form — `?v=2`, or any paging parameter
  (`transcriptpage.Keyed`) — reads the agent's own store, through
  `KeyedTranscriptReader` where the provider has it (stored thinking as
  keyed rows), in the child and in the Router's fallback alike. Plain
  `/transcript` is unchanged and never carries thinking rows.

### 5. Connecting, reconnecting, switching

*Built (C2), the replay rules:*

- **Fresh** (no `Last-Event-ID`, no `?after`): `hello{resume:"fresh"}`; the
  whole unsettled tail, oldest first, with each run of one item's deltas
  coalesced into one frame (it carries the last delta's id) and a
  `tail_truncated` where evicted items were; an `unsaved{keys}` naming tail
  items already declared so; then state snapshots — open permission requests,
  background processes, the task roster, each its latest status frame;
  `replay_end`; live. Snapshots, the `unsaved` and `replay_end` carry the head
  id (`<epoch>:<hello.seq>`).
- **Resumed** (`Last-Event-ID`, or `?after=<epoch>:<seq>` for a client that
  can't set the header, in this epoch and still in the journal):
  `hello{resume:"resumed"}`, exactly the frames after that seq as journaled
  (tool results as previews), `replay_end`, live.
- **Resync** (another epoch, or a seq the journal no longer covers):
  `hello{resume:"resync"}`, `resync{reason:"epoch"|"gap"}`, then as fresh.
- The tail a fresh replay sends includes subagent items, and unsaved items
  only from the latest finished turn and the current one (§3).
- A subscriber too slow for the live stream (1024 frames behind) is
  disconnected rather than skipped; it reconnects with its `Last-Event-ID`.

**The journal** (memory only, per child): unsettled frames stay until their
key is stored, capped at 4 MiB (evicting whole items, oldest first, an item
still streaming last); every
other frame (settled, status) is retained for 60 s or a 4 MiB ring, so a
reconnect within that resumes without a gap.

**Settling** (`runner/settle.go`): ~250 ms after a settle hint (one read per
burst), after each `user_message`, at turn end (and again ~1 s later, before
declaring `unsaved`), and whenever `/transcript` is served, the child reads
the store with `TranscriptSince` (a stale cursor re-reads the whole store;
an adapter that can't read incrementally is read whole, at turn end and on
`/transcript` only). An item settles when its row is stored (`agent` for
text, `thinking`, `tool` for a call and `tool_result` for its result — or
no result before the turn ended — `user`, `inbound`). Settle reads use the
keyed form (thinking rows included). A `/transcript` read settles the
tail *before* it is answered, so no row it serves shares a key with a frame
still in the tail. `GET /transcript?v=2` (or paged) is the read a keyed
client makes: always the agent's store, keyed form (for goose, never
acprpc's live buffer, whose rows mostly have no keys); plain `/transcript`
keeps today's source.

**The child's exit.** When the provider process dies mid-turn the child
exits at once; first it waits (at most 250 ms) for every connected `/output`
writer, legacy and v2, to put its last frames — `unsaved`, the error — on
the wire.

**User-row labels** (no `KeyedSender`: goose, hermes): on each settle read,
the next unlabelled stored user row after the previous labelled one takes the
oldest unmatched send's key; text equality is only a sanity check (logged on
mismatch). `/transcript` serves labelled rows under the `user_message` key
(segments get sub-keys of it). Labels live as long as the child; rows from
before it, or from a session resumed outside mads, keep native keys.

The client side, for C4:

- **Mount** (first open, or switching to a session): fetch the last page of
  `/transcript?v=2`; open `/output?v=2` without a cursor and apply the replay.
- **Reconnect after a drop:** EventSource resends `Last-Event-ID` itself;
  follow `hello.resume`. On `resync`, refetch the last page and replace items
  by key before applying the tail that follows.
- **The UI merges by key.** A frame whose key is already rendered from the
  transcript is skipped (or, for an in-progress item, its deltas continue the
  same item); a transcript row whose key is rendered live replaces it in place,
  same React key, same text. Order is the transcript's for settled items, then
  the tail's. `settled` says which live items to hand over; `unsaved` which to
  keep.
- Replayed frames render instantly; the pacer paces only frames after
  `replay_end`.

Accepted as they stand (C2 review, 2026-10-21), for C4 to build around:

- `notice` frames have no live producer yet; stored notices come as
  `/transcript` rows with `notice:true`.
- Queue changes still arrive as `session.queue_changed` events;
  `hello.queued` is only the snapshot at connect.
- A send that failed (the HTTP caller got the error) is reported as
  `unsaved` for its `user_message` keys: drop the bubble.
- hermes ran without `StartOptions.Keyed` until C4a, which turned it on.

Consequences:

- Several turns in a row: each turn's items settle as the agent stores them; a
  send during the previous turn's settle lag is just more tail.
- Two sessions with live turns: each child keeps its own tail whether watched
  or not. Switching to either replays its unsettled items; the rest is already
  in its transcript page. Cost is flat in turn length.
- **All mid-turn transcript refetches go away**: stream `open` (replaced by
  replay), refocus (the stream is still connected, or reconnects with a
  cursor), `link.message_injected` (now a `user_message` frame), and the
  optimistic-bubble juggling in `refetchAfterTurn`. At `turn_end` the UI may
  refetch the last page once for timestamps; with keyed merge, that's a no-op
  visually.

### 6. Rendering: one keyed list, one join rule

- The pane holds one ordered list of items keyed by `key`, from transcript
  pages and stream frames alike; React keys are the item keys (no positional
  `m-${i}` keys, so nothing remounts when items arrive or settle).
- **Join rule** (mockup): adjacent *visible* text items render as one bubble,
  **regardless of message or item id**. They are joined as-is if the earlier
  item ends with a newline, otherwise with a blank line. Any visible item
  between them (tool call, thinking with text, notice, user message) ends the
  bubble; hidden items (empty or signature-only thinking, filtered context
  rows) don't. The same function runs on live and stored items. In practice it
  fires rarely: claude never emits adjacent text blocks, and codex's
  commentary and final answer usually have a command between them.
- The live activity group (tools/thinking) keeps today's behaviour: open while
  the turn runs, collapsing at turn end. That collapse is the one intentional
  change at turn end.

### 7. Long turns

- The tail's size is bounded by the in-flight items, its duration by the
  running tool (§3); neither grows with turn length.
- The transcript is paged from the end (§4), so mounting a session deep in a
  multi-hour supervisor turn costs one page plus the tail.
- Background-process notifications, mid-turn sends and injects are
  `user_message` frames in sequence: no refetch, no vanishing.

### 8. Transports

One contract (keyed live items + keyed stored rows); the transports differ only
in how the live half gets its keys:

| | Structured JSON (claude stream-json, codex app-server) | ACP (goose, hermes, kiro) | PTY (terminal mode, any agent) |
|---|---|---|---|
| Live chat frames | the adapter maps native events; ids already on the wire | acprpc maps them; per-agent knobs supply the key derivation | **none**: the live view is the terminal |
| Stored rows | the adapter's reader over the agent's store | the adapter's reader over the agent's store (not acprpc's buffer) | same reader as chat mode |
| Unsettled tail | yes | yes (acprpc's buffer) | empty |

Chat and Terminal are two transports over one conversation, switched by
`POST /transport` (`ConversationTab.tsx:143`). In terminal mode there are no
chat frames; the chat view shows the stored transcript, with the same keys, so
switching back merges cleanly. A turn run in terminal mode reaches the chat
view only as the agent stores it, as today.

## Test plan

Repro recipes from 2026-10-20 become regression tests (fake provider with
`blocks:` and `user_lag_ms:`, plus a `hold_until_tool_done:` option that
models goose's store lag):

- **Text join (R4A–E):** the live and settled DOM are identical for: sentence
  then heading; a list split across items; two sentences; sentence then list;
  item 2 starting with a blank line. Join-rule unit tests, including hidden
  items between text and visible items breaking the bubble.
- **User message lag (bug 2):** with a 2.5 s user write lag, the bubble is
  visible continuously through the turn after (a) a send that starts the
  session, (b) a mid-turn visibility change, (c) a mid-turn stream reconnect.
- **No duplicates (bug 1):** with an item settled mid-turn, a reconnect,
  refocus and inject each leave exactly one copy.
- **Switching (bug 3):** two sessions with live turns; switch away and back
  mid-item; the whole turn so far shows instantly and the item continues.
- **Held-back store:** with the fake holding a tool call until the tool ends
  (goose), a mount mid-tool shows the pre-tool text and the running tool once.
- **Cursor:** reconnect with `Last-Event-ID` gets only the missed frames; a new
  epoch gets the whole tail.
- **Long turn:** a scripted turn with thousands of frames keeps the tail under
  the cap, and mounting mid-turn fetches one page + the tail.
- **Key conformance (every adapter):** the probe in §1 against each adapter's
  scripted stand-in in CI, and against the real binary in its `MADS_*_SMOKE`
  run. The survey captures become stand-in fixtures.

## Work breakdown

| # | Part | Depends on |
|---|------|-----------|
| P | Provider prerequisites (separate branches, under way): goose 1.50 compat window + captures; claude `queued_command` decode fix; hermes adapter (full trust only); codex app-server adapter | — |
| C1 | Provider keys: `Key` on `provider.Event` and transcript `Message`; per-adapter derivation (claude, codex, goose, hermes, kiro-unverified, fake); live-text normalisation; settle hints; `TranscriptSince`; ACP readers over the agent's store; key conformance probe; fake `blocks:` / `user_lag_ms:` / `hold_until_tool_done:` | P |
| C2 | Session service: `user_message` frames from the runner; structured chat frames; `epoch:seq` ids; unsettled tail with settling, cap, previews, `unsaved`; replay + `Last-Event-ID` + `replay_end`; user-row labelling | C1 |
| C3 | `/transcript` pagination (`before`, `limit`) in the child and the Router's stored-ref path; README/contract updates | C1 |
| C4 | UI: keyed conversation model; merge rules; join rule; notices; thinking by key; remove mid-turn refetches and optimistic-bubble paths; pacer paces post-replay only; paging on scroll | C2, C3 |
| C5 | e2e: the regression recipes above in the hermetic suite; spec text (REBUILD-SPEC "Two Communication Planes", session README) updated | C4 |

## Decided

- Keys are opaque and supplied by the provider layer, derived from native ids
  (2026-10-20, Drew).
- Join rule is turn-level: adjacent visible text items, regardless of id
  (2026-10-20, Drew).
- acprpc's buffer is the ACP live tail only; history comes from the agent's
  store (2026-10-20, Drew).
- Hermes is offered at full trust only (its permission requests can't enforce
  less); recorded in its adapter design (2026-10-20, Drew).

## Open questions

1. **Settling cadence.** Re-reading after adapter settle hints is cheap with
   `TranscriptSince`; a filesystem watch would settle exactly when the agent
   writes but needs a per-store mechanism (JSONL vs SQLite WAL).
   Recommendation: hints only.
2. **The cap.** 4 MiB per session child, like the PTY ring. Recommendation:
   fixed, not configurable.
