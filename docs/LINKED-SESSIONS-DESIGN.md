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

## Decisions

1. **Async, queued, never interleaved.** All cross-session message triggers are
   asynchronous. An injected/orchestrated message queues behind the subject's
   in-flight turn — it never interrupts. The sender's tool returns "delivered"
   immediately; replies arrive as events (optional bounded wait for
   quick-question ergonomics).
2. **Attribution is explicit.** Injected messages render in the subject's stream
   with source attribution ("⇄ from Leader · <session name>") — never
   impersonating the subject's own user. Actor stamping via the existing
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
     parking a tool call, a session crash, a task reaching done or blocked.
     Signals are composed deterministically (no fast-model call) and
     delivered to every non-manual explicit read link, per_turn included: a
     permission pause parks the subject mid-turn, so the turn digest that
     would report it may never fire. Turn digests still accumulate on the
     link's capped mailbox (oldest dropped first) and flush as context in
     the next wake's forced turn, or on demand via the agent-facing
     check_mailbox tool. The fast model triages while it compresses: a
     digest it marks actionable (blocked, failing, waiting on input,
     drifting) wakes the monitor immediately instead of queueing, so
     on_signal isn't limited to the deterministic terminal events.
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
   plainly what they're for, and nothing else changed about the routes.
   The provider sees one server, `mads-links` (tools appear as
   `mcp__mads-links__message_session`), with fixed verbs:
   `list_linked_sessions`, `message_session(session, text, wait_ms?)`,
   `get_session_summary(session)`,
   `get_session_turns(session, from_turn, to_turn)`,
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
