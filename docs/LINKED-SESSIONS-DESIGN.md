# Linked Sessions — agreed design (2026-07-16)

Drew + Claude design session. Extends REBUILD-SPEC §1.12; where this document and
§1.12 disagree, this document wins (notably: pipeline monitorability).

## Model: a link is a set of grants

A link is directed (granting session ⟶ subject session) and carries a **grant
set** — the schema stores grants, the UI names presets:

| Grant | Meaning |
|---|---|
| `read` | receive digests of the subject's activity; read transcript + task state |
| `inject` | deliver a message into the subject's conversation (queued, attributed) |
| `orchestrate` | agent-held tool to message/direct the subject's agent |

**User-selectable presets are exactly two**: **Monitor** = `{read}` and
**Leader** = `{read, inject, orchestrate}`. **Hand-raiser** = `{read, inject}`
exists only as a SYSTEM-GRANTED state (see decision 6) — it is never offered in
the link chooser. Contracts see grants, so new combinations need no schema
change.

The meaning of `inject` depends on the subject:

- subject is an interactive session → deliver a message into its conversation
  (in practice Leader-only, since hand-raiser is never user-granted);
- subject is a pipeline run → system-facing actions on the run (e.g. posting a
  task note, flagging a blocker) — a hand-raiser interacts with **systems,
  never directly with other agents** (Drew, 2026-07-16). Agent-to-agent
  messaging is exclusively `orchestrate` territory.

## Multiplicity

Links are directed edges; nothing is 1:1 by design:

- A subject may have **any number of monitors** (`read` fans out; L2b digests
  deliver to every reading link).
- A leader may hold links over **any number of subjects** (the L2c tool lists
  them all).
- A subject may have **at most one `orchestrate` holder** — two bosses invite
  conflicting direction; validated at L1. Revisit if a real use case appears.
  The refusal names the existing holder (session name and id).
- A directed pair holds **at most one link** (2026-09-30). A create for a pair
  that's already linked merges into that link instead of adding a parallel
  one (two rows carrying `read` would double every digest and signal). The
  same grant set is an idempotent success with no event. A different grant
  set updates the link in place (same id, `link.updated`); a cadence left
  unset keeps the stored one. Adding `orchestrate` this way runs the same
  checks as a fresh leader link.
- **No loop of leaders** (2026-10-18). A write that adds `orchestrate` is
  refused if the subject already leads the granting session, directly or
  through others; the refusal spells out the loop by session name. Monitor
  edges don't count, and a loop stored before this rule can still have its
  cadence changed or a link downgraded.

## Decisions

1. **Async, queued, never interleaved.** All cross-session message triggers are
   asynchronous. An injected/orchestrated message queues behind the subject's
   in-flight turn — it never interrupts. The sender's tool returns "delivered"
   immediately; replies arrive as events (optional bounded wait for
   quick-question ergonomics).
2. **Attribution is explicit.** Injected messages render in the subject's stream
   with source attribution ("⇄ from your leader · <session name>") — never
   impersonating the subject's own user. Since 2026-10-20 the attribution is
   written into the message itself (see "Message wrapper" below). Actor stamping via the existing
   X-Mads-Actor discipline.
3. **Leader authority is capped by the subject's trust level.** A link grants
   reach, never permissions: the subject's agent acts on leader requests within
   its OWN trust setting. Hard prerequisite: trust must be actually enforced by
   providers (today it is decorative — see trust-enforcement task).
4. **Monitor cadence: three-valued, defaulting to signals-only.** The cadence
   knob on a `read` link picks how the push half behaves; on-demand "catch me
   up" reads stay available under all three and cost nothing until asked.
   - `per_turn`: when a watched agent's turn completes, a marginally
     compressed digest of that turn (fast-model compression — the
     synthesis/fastmodel runner is the natural engine) is pushed (queued) to
     the monitor. No continuous token streaming: LLMs consume messages, not
     streams. Iterate toward finer granularity only if digests prove
     insufficient.
   - `on_signal` (default): nothing pushed per turn. The monitor is woken
     only by attention signals from the subject — a permission request
     parking a tool call, a session crash, a task reaching done, blocked, wont_do or needs_review.
     Signals are composed deterministically (no fast-model call) and
     delivered to every non-manual explicit read link, per_turn included: a
     permission pause parks the subject mid-turn, so the turn digest that
     would report it may never fire. Turn digests still accumulate on the
     link's capped mailbox (oldest dropped first) and flush as context in
     the next wake's forced turn, or on demand via the agent-facing
     check_mailbox tool. Digest content never wakes the
     monitor: a Fast Model "actionable" verdict used to, but false-alarmed
     ("blocked waiting on its own subagents/runs") and was turned off
     (2026-10-19). Waking on a turn's content is meant to return as an
     intent-based classifier, developed separately (not a frontier/fast LLM
     call). The noop line is offered on done-task, permission and crash
     wakes only; blocked, wont_do and needs_review notices ask for a
     decision instead.
     Rationale: a monitor is an LLM — every pushed
     digest forces a full-inference turn over its whole context, and on a
     healthy subject almost every such turn concludes "nothing to do". The
     wake filter belongs in deterministic code, not in the most expensive
     component of the system.
   - `manual_only`: nothing pushed at all; pull-only.

   Every monitor-facing push closes with a one-line noop protocol ("reply
   with exactly NOOP" when nothing needs doing): the monitor's cheapest
   possible answer, which the conversation UI collapses — consecutive
   digest→NOOP pairs render as one aggregated quiet-check chip. Note the
   collapse trims MADS transcript/UI weight only; a stateful provider keeps
   its own NOOP turns in context, which is why not waking at all (the two
   cadences above) carries most of the value.

   **Amendment (2026-10-19): NOOP is offered only where "nothing to do" is
   plausible.** On a leader's or monitor's link, a task signal offers NOOP
   only for done; blocked, wont_do and needs_review ask for a decision.
   On an implicit hand-raiser (decision 6) every task signal offers NOOP
   except blocked, the one status that stalls the session's own run.
   Permission and crash signals keep it (`job.signalClosing`).
5. **Pipeline runs are monitorable, not leadable.** §1.12's interactive-only
   restriction is relaxed for `read` (+`inject` where the subject is the
   pipeline's session — see 6). Pipeline runs keep their single orchestrator:
   no `orchestrate` grant on a running pipeline.
6. **Implicit hand-raiser on own pipeline (Drew).** If a pipeline is launched
   for a session whose interactive agent is live — or the session becomes
   interactive while the pipeline runs — that agent acquires hand-raiser
   grants `{read, inject}` over the run immediately (or on its next turn
   boundary): it receives run digests, may surface findings to its user, and
   may act on the RUN as a system (task notes, blocker flags) — never message
   other agents. Exactly the shepherding pattern proven manually.

   **Implementation note (L3, 2026-07-17).** Because a pipeline run and the
   session it's "for" share one session ID (`Supervisor.SpawnPipeline(ctx,
   runID, sessionID)` scopes the run to the same `sessionID` an interactive
   `SpawnSession` would use), "that agent acquires hand-raiser grants over the
   run" is realized as a **self-referential link**: `GrantingSessionID ==
   SubjectSessionID == sessionID`. This required a dedicated system-only store
   path (`Links.CreateImplicitHandRaiser`/`RemoveImplicitHandRaiser`) rather
   than reusing `Links.Create`, since `Create` rejects a session linking to
   itself by design (a rule correct for ordinary distinct-session links). The
   link is persisted with a new `Implicit bool` field
   (`pkg/contracts/link.go`) so existing L1/L2 machinery (digest fan-out,
   event emission, UI reads) treats it like any other link record without
   special-casing. **Auto-remove is final, not "or kept as history"**: the
   Router's Supervisor grants on both edges (pipeline start; child
   activation with a run in flight) and revokes the moment the
   run's process exits (normal completion, cancellation, or crash alike) —
   the link never outlives the run it was granted over. The grant call
   itself is detached from the spawn that triggers it (a background
   goroutine making its own HTTP round trips), so a run that exits quickly
   can have its (synchronous) revoke run *before* the grant's Create lands —
   `grantImplicitHandRaiser` re-checks the run's liveness immediately after
   its own write completes and self-revokes if the run is already gone,
   closing that window regardless of which call actually lands last.

   **Amendment (2026-09-28): Edge A is level-triggered.** Pipeline start
   grants the link unconditionally, not only when the session's interactive
   child is live. The live-child check read the Supervisor's in-memory map,
   which a Router restart empties, so a 10-task run launched three minutes
   after a restart never got its hand-raiser. When a task went blocked, the
   run digest found no reader and nobody heard about it.

   **Amendment (2026-09-28): task terminals reach the hand-raiser as
   signals.** `job.DeliverSignal` now targets the implicit link for task
   terminals (never for permission or crash signals, which a session can't
   usefully be told about itself). If the session's child isn't running, it
   is woken through `Deps.Waker`, the same wake a composer send uses. The
   notice is deterministic (title, status, latest blocker note) and stays
   digest-attributed with the NOOP instruction. The Fast-Model run digest
   (`GenerateRunDigest` / `RunWatcher`) is retired: its only targets were
   implicit links, so keeping it would have pushed two forced turns per
   task terminal. It also dropped the notice silently when the child was
   dormant or no Fast Model backend was configured.

   **Scope note (L3, 2026-07-17).** The 2026-07-16 amendment above (agent may
   "act on the RUN as a system: task notes, blocker flags") is implemented
   only on its *read* half in this pass — the hand-raiser receives run
   digests (task status changes, blocker notes folded in), matching the
   acceptance criteria. Wiring an agent-invoked task-note/blocker-flag tool
   is descoped, not overlooked: `contracts.Task` has no `RunID` (tasks are
   scoped only by `SessionID`, shared between a run and its interactive
   session), so there's no schema-level way to distinguish "the run's tasks"
   from the session's own; and the interactive agent has no task-listing
   tool at all today (the only existing note-creation path is the
   background fastmodel synthesizer in
   `services/session/internal/synth/engine.go`, not an agent-invoked call).
   A bare post-a-note tool without a way to discover which task ID it
   applies to would be unusable. Real fast-follow, gated on giving tasks a
   `RunID` (or equivalent scoping) and a task-listing capability first.

## Inbound messages (2026-10-19)

Agents can receive messages from OUTSIDE mads — today Claude Code's
cross-session peer messages (one claude process's native `SendMessage` to
another over a local socket). Before this, a leader's peer message to its
follower never appeared in the follower's conversation. "A message arrived
from outside" is now a first-class, provider-neutral conversation item, the
same kind of thing as a background task's completion notice: a timeline entry
that may open an unprompted turn. Three layers:

1. **Provider (neutral contract, `providers/provider`).** `provider.Inbound`
   {channel, from, from_name, state, hold_reason}; `EventInbound` live and
   transcript Role `"inbound"`, Text = the bare body; optional
   `PeerAddresser` = the session's own address in the same opaque format its
   senders' `From` uses. A held message (`state: "held"`) reached the
   harness but not the agent and opens nothing.
2. **Session layer (`services/session/internal/runner`, `pkg/inbound`).**
   Records `session.peer_address` after each provider (re)start (on change
   only). Resolves `From` to the session whose most recent address at or
   before the message equals it (addresses are reused after a process
   exits), then the link between the two (either direction) and the sender's
   current name → `contracts.InboundSource`, on the transcript
   (`TranscriptMessage.inbound`, live and dormant alike) and the live
   stream's `"inbound"` event. A delivered message that lands with no turn in
   flight is a turn the runner didn't start: busy (so Send/Inject queue
   behind it) and agent working until its turn end, its digest opening with
   the message. It writes no `link.message_injected` — that event means
   mads delivered it.
3. **Claude Code adapter (`providers/claudecode`).** The only place the
   specifics live: `--replay-user-messages` on the stream-json transport
   (a peer message is echoed as an `isReplay` user line with an `origin`
   of kind `"peer"`; mads's own echoed sends produce nothing), the
   `peer_message_hold` system line, `messaging_socket_path` on init, and the
   two transcript shapes (idle arrival: an `isMeta` user line with the
   origin; mid-turn: a `queued_command` attachment).

**Neutrality rule:** nothing above the provider layer knows about sockets,
`isMeta`, `command_lifecycle`, or Claude Code; an address is only ever
compared, never parsed. A harness with no such channel implements nothing
and nothing changes.

## Message wrapper (2026-10-20)

Every message MADS inserts into a conversation as a user turn carries its
label in the text, the way Claude Code wraps its own peer messages:

```
<mads-message kind="digest" from="Pipeline Study" relation="a session you lead" turn="42">
…the body, exactly as delivered before (including any NOOP line)…
</mads-message>
```

- **What's wrapped:** a linked session's message (`message_session`, a link
  inject) as `kind="message"`; a per-turn digest as `kind="digest"`; a
  signal wake (permission, crash, task terminal, with any mailbox backlog)
  as `kind="signal"`; MADS's own grant notice and a pipeline's
  folder-changed notice as `kind="notice"`. The human's own sends, queued or
  not, are never wrapped.
- **Attributes:** `kind` always; `from` (the sender's name, `mads` for
  MADS's notices); `relation` when there is one, always from the
  RECIPIENT's side — `your leader`, `a session monitoring you`, `a session
  you lead`, `a session you monitor`, `your pipeline run`; `turn` on a
  digest, the subject's transcript turn it covers in `get_session_turns`
  numbering. Values are XML-escaped. No ids.
- **Who wraps:** the session runner (`pkg/linkwrap.Wrap`), at the one point
  it hands an injected message to the provider — sent at once or drained
  from the queue. A coalesced run of queued digests is one turn of several
  blocks, each wrapped on its own. A re-root note owed to the agent stays
  outside, ahead of the wrapper.
- **Who reads:** one parser (`pkg/linkwrap.Expand`) on both the live
  child's transcript and the Router's durable one, so a sleeping session's
  history is labelled exactly like a running one's. A wrapped user message
  becomes one transcript message per block, labelled from its attributes;
  text ahead of the blocks (the re-root note) becomes a notice from mads.
  A block ends at the outermost close tag, so a body that quotes the tags
  doesn't break it; a malformed or unclosed wrapper leaves the message
  plain. `get_session_turns` counts a send as one turn however many blocks
  it holds.
- **Not retroactive:** messages from before 2026-10-20 have no wrapper and
  read as the human's. The runner's in-memory, send-ordinal attribution
  this replaces misaligned after any restart and is gone.
- **The UI decides by kind,** not by looking the link up: digest and signal
  render as digest cards (only the watching side ever gets one), notice as
  a notice row, message as the "⇄ from <relation> · <name>" chip. A digest
  card's cadence line appears when its sender's name matches one of the
  viewer's links. A NOOP answering a digest or signal folds into a quiet
  check; one answering a notice marks the notice "acknowledged".
- **Spoofing:** anyone can type the tag. A human who types it in their own
  session only changes how their own UI draws their own message; an agent
  reading a wrapper it didn't get from MADS gains nothing a plain message
  couldn't say. Wrap escapes the tags inside a body (`&lt;mads-message`,
  reversed by Parse), so a linked session that writes a close tag and a
  fresh block into its own message can't forge a second block (say, a
  "notice from mads") in its subject's conversation. The label is a rendering and context aid, never an
  authority check: delivery-time checks (grants, `DigestAttributed` for the
  self-amplification guard, the `link.message_injected` audit event) still
  run on the Attribution the Router sends, not on parsed text.

- **Several updates in one turn:** a wake's flush of deferred pushes from
  more than one entry sends `InjectRequest.Parts`, one part per entry with
  its own stored Attribution; the runner wraps each part on its own and
  delivers them as one turn, so every update keeps saying who sent it.

## Unchanged from §1.12

- Link creation UX, two paths:
  - Multi-select sessions in the rail → Link → choose preset. Sessions must
    already exist. (UI mockup pass before the UI task.)
  - Launchpad split "Start session" button → "Link to session(s)…" stages
    leader/monitor links against the `"$new"` placeholder, submitted via
    POST /sessions `links` so the session arrives already linked (no
    post-create race with spawn). New session as holder only in v1; the
    dialog keeps its holder-picker shape so new-session-as-subject is a
    later unlock, not a redesign.
- Constraint: linked sessions must NOT operate in different worktrees of the
  same repo (parallel-universe problem). That is the only placement rule:
  links may cross workspaces and repos (a web-app leader directing an IaC
  follower), since a link conveys messages and digests, never file access.
- Leader→follower orientation only; no peer/bidirectional links (deadlock
  complexity, no proven use case).
- Session deletion breaks links, never cascades.
- Everything ships behind the `linked-sessions` flag (toggle already in
  Settings → Advanced).

## Existing plumbing this builds on

- `POST /sessions/{id}/messages` + `/interrupt` (router→child proxy) — the
  delivery primitive for `inject`/`orchestrate`.
- Live transcript proxy (`GET /sessions/{id}/transcript`) — `read`'s
  transcript half.
- W5.4 summary job — abridged follower context for the leader.
- W5.1 synthesis + providers/fastmodel — digest compression.
- W5.x capability runtime — how the leader's agent receives its
  cross-session tool.
- W1.7 link schema hooks; `linked-sessions` feature flag (W6.10).

## Decomposition (dependency order)

1. **Trust enforcement** (prerequisite, independently valuable — seeded).
2. **L1 Data Service**: link records with grant sets, preset vocabulary,
   constraint validation, CRUD + events.
3. **L2a Inject path**: queued, attributed delivery into a subject session;
   attribution rendering in the conversation UI.
4. **L2b Digest path**: turn-completion → fast-model digest → queued delivery
   to monitor sessions; cadence knob on the link (`per_turn` / `on_signal` /
   `manual_only`, decision 4). Shipped: `on_signal`'s push half is
   digestwatch.SignalWatcher → job.DeliverSignal.
5. **L2c Leader capability**: agent tool (message subject / fetch summary /
   list links), async replies as events. Shipped: Router endpoints
   (`GET /sessions/{id}/orchestrate/subjects`, `GET .../orchestrate/summary`,
   `POST /links/{id}/orchestrate/message` with an optional `wait_ms` bounded
   wait, per decision 1's async-with-quick-question-ergonomics carve-out) plus
   an MCP-over-stdio server (`pkg/orchestratemcp`, hand-rolled JSON-RPC 2.0
   framing — no SDK dependency) exposing the link tools (named below),
   materialized only when a session holds an `orchestrate`
   grant and the `linked-sessions` flag is on (`Runner.setupOrchestrate`),
   via a new `StartOptions.MCPServers` descriptor on `provider.Provider`.
   `providers/claudecode` now threads `StartOptions.MCPServers` into a real
   `--mcp-config`/`--strict-mcp-config` launch on both transports
   (`providers/claudecode/mcp.go`), so a live claude-code agent holding an
   `orchestrate` grant can reach them.
   `providers/kirocli` declares `BehaviorMCPServers` unsupported
   (`providers/kirocli/README.md`) pending a confirmed live mechanism — the
   capability simply doesn't materialize for kiro-cli sessions, and the
   session child degrades gracefully rather than erroring.

   **Activation (2026-09-30).** A new grant starts `pending_restart`. The
   runner mounts it only at a quiet turn boundary (turn over, nothing
   queued). Where the live session can change its MCP servers in place
   (`provider.MCPHotSetter`: claude-code's stream transport, via the
   `mcp_set_servers` control request, see
   docs/research/claude-mcp-hot-mount.md), there is no restart at all.
   Otherwise the runner cycles the provider process on the same
   conversation (`RefreshOrchestrate` → `swapTransport` with resume). That happens
   when the Router notifies it and, level-triggered, at every quiet boundary
   while any of its grants are still pending. So a lost notification or a
   refused cycle (`ErrSwitchBusy`, no resumable ref yet) retries at the next
   idle moment rather than waiting for a restart. One cycle mounts every
   pending link. A pipeline run doesn't block it; the run is separate
   processes. The orchestrate server runs `mads` by absolute path
   (`pkg/madsbin`); a bare `mads` missing from PATH is what left the first
   live grant pending indefinitely. Until the tools mount, the Router's
   grant notice gives the agent `mads orchestrate subjects|summary|send`
   commands that do the same thing from a shell.

   **Link tools (2026-10-19).** A leader ignored the tools and reached its
   subject another way, so its messages bypassed MADS. The tools now say
   plainly what they're for; the routes are unchanged apart from
   `get_session_turns`'s rework below.
   The provider sees one server, `mads-links` (tools appear as
   `mcp__mads-links__message_session`), with fixed verbs:
   `list_linked_sessions`, `message_session(session, text, wait_ms?)`,
   `get_session_summary(session)`,
   `get_session_turns(session, count?, from_turn?)`,
   `check_mailbox(session)`, `set_attention(session, cadence)`. A read-grant-
   only server omits `message_session`. `session` is the linked session's
   name (its ID also works). The server resolves it on every call against
   the subjects route: exact name, then exact ID, then case-insensitive
   name. Unknown or ambiguous names come back as tool errors listing the
   valid names, with IDs when two share a name. It's never cached, because
   sessions get renamed. The Router routes and the `mads orchestrate` CLI
   still take link and session IDs. The server's `initialize` carries
   instructions saying the user linked these sessions and suggests these
   tools, and that messages arrive attributed to the sender with replies
   returning as digests. They name no sessions, since links change under a
   running server. The server is marked `alwaysLoad`
   (`provider.MCPServerRef.AlwaysLoad`; claude-code 2.1.285+) so its tools
   sit in the prompt instead of behind tool search. The local capability
   registry still keys the process `orchestrate-<session id>`: its pid
   files live in the shared `$MADS_HOME/mcp`.

   `get_session_turns` reads the subject's transcript (2026-10-19), the
   same read `GET /sessions/{id}/transcript` serves: the live child's when
   it's running, the provider's `TranscriptReader` otherwise. It used to
   rebuild turns from `session.turn_ended` digest deltas, which gave
   assistant text in streaming fragments, tool calls as a bare name, no
   tool output, no opening message, and event-log seqs as turn numbers. A
   turn now starts at each `user` message (the human's or a link-injected
   one); anything before the first folds into turn 1, and turns number
   1..N. `count` alone is the last `count` turns (default 1); `from_turn`
   plus `count` reads forward from there. A slice past either end is
   clamped and the header says so; `from_turn` past N is a tool error
   naming N. The result is plain text: a `<name> — turns 41–42 of 42`
   header, then per turn `## Turn N · user` (or `· from your leader ·
   <name>`, `· digest from a session you lead · <name>`, `· notice from
   mads`, read from the message wrapper), the
   message, `agent:` lines, `tool <Name> <compact JSON input>` with its
   output on `  → ` lines, `notification:` lines, and a one-line
   `[context compacted]`. No thinking, statuses, ids, or locations. Tool
   input is cut at 1,000 characters and output at 2,000, each with
   `…(truncated, N chars)`; past 40,000 for the whole response the oldest
   turns in the slice drop first and the header says which. Raw tool
   content does leave the subject this way, deliberately: the user granted
   the link. Digests are unchanged and still carry tool calls as name and
   path only. `list_linked_sessions` reports each session's `turns` (the
   subjects route counts them only when asked with `?turns=1`, since every
   other tool resolves its `session` through that route), and
   `get_session_summary` reports `turns` in place of the seq anchors it
   used to return.
6. **L3 Implicit hand-raiser**: pipeline start/session-activation hooks
   auto-granting `{read, inject}` over own-session runs.
7. **L4 Link UI**: rail multi-select + Link flow + grouping/badges +
   digest rendering (mockup pass first; flag-gated).
8. **Staged links at create**: `POST /sessions` accepts an optional `links`
   list of `LinkRequest`s with `"$new"` (`contracts.NewSessionPlaceholder`)
   on either endpoint; the Router resolves the placeholder and creates the
   links after the session record exists but before the child spawns, so a
   staged leader's orchestrate MCP is live on the child's first turn (no
   client-side create-then-link race against `setupOrchestrate`). The 201
   body gains a `links` key (created links, IDs resolved) only when staged
   links were sent. Any invalid staged entry fails the whole create —
   never an orphan unlinked session. Flag-off, the `links` field itself is
   a 400 (plain creates keep working; only /links routes 404 wholesale).

## Delivery to sleeping sessions (2026-10-20)

A push to a session whose child isn't running used to vanish: `message_session`
and link inject answered "subject session not running", per-turn digests were
"best-effort skipped", signals woke only the implicit hand-raiser, and the
grant notice was dropped. Undelivered digests and signals mean the channel is
quietly not doing its job, so every push now either wakes its recipient or
waits for it. Drew's call (option b):

- **Wake for anything that needs action.** `message_session`, link inject, and
  the signals permission-requested, crashed, and task blocked / won't-do /
  needs-review. The wake is the Router's `ensureSession` (per-session lock),
  the same wake a composer send gets, and it does **not** clear the session's
  "set aside" tag: a link message isn't the user changing their mind. A
  just-woken child answers `/inject` with a retryable 503 while its runner
  starts, so delivery retries with backoff for about 15s (`job.PostInject`)
  instead of failing. A wake that can't happen (unknown provider, spawn
  failure) falls back to deferring the signal; `message_session` and inject
  return the structured wake error.
- **Defer, then flush on start** for everything informational: per-turn
  digests, done signals, and the orchestrate grant notice. They are stored in
  the link's mailbox (`link_digests`) flagged `deferred`, with the push's text
  and its full `Attribution` (migration 0032 adds `deferred`, `noop`,
  `attribution`), and replay unchanged later. A digest never wakes anyone, so
  there is no wake chain (a woken monitor's own forced turn is still
  digest-attributed and doesn't re-trigger digestwatch). The implicit
  hand-raiser still wakes for every task terminal, as before (decision 6).
- **on_signal backlog is untouched.** Deferred entries are a separate class
  from the on_signal accumulation in the same table. `DrainLinkDigests` (the
  signal wake and `check_mailbox`) drains only the backlog; the start-time flush
  drains only deferred entries. The 32-entry cap applies per class.
- **Flush on start.** `Supervisor.SpawnSession` calls a start hook once the
  child is tracked, whatever started it (composer send, link wake, manual
  resume). The hook (`job.FlushDeferred`) gathers the deferred entries of every
  link where the session is the granting side and delivers them as ONE forced
  turn, oldest first: a single entry replays verbatim; several are joined under
  a short header, prefixed with their source when they come from different
  links. The NOOP line closes the turn only if every entry is NOOP-able. The
  turn carries the oldest entry's Attribution whole (`DigestAttributed` only if
  all are). It is idempotent because the drain is one Data Service transaction:
  racing flushes can't both receive an entry. A failed replay re-queues the
  entries for the next start.
- **Visible while asleep.** `Session.waiting_updates` (derived by the Router
  from `GET /links/deferred-counts` on the Data Service, like `effective_trust`)
  is the count of deferred entries where the session is the recipient. The rail
  card shows "N updates waiting" until the flush clears it; deferring and
  flushing emit `session.updated` so every open browser refetches.
