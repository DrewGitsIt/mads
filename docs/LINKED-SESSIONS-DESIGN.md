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
4. **Monitor cadence: per-turn compressed digest.** When a watched agent's turn
   completes, a marginally compressed digest of that turn (fast-model
   compression — the synthesis/fastmodel runner is the natural engine) is pushed
   (queued) to the monitor. No continuous token streaming: LLMs consume
   messages, not streams. Iterate toward finer granularity only if digests prove
   insufficient. On-demand "catch me up" reads stay available and cost nothing
   until asked.
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

## Unchanged from §1.12

- Link creation UX: multi-select sessions in the rail → Link → choose preset.
  Sessions must already exist. (UI mockup pass before the UI task.)
- Constraint: linked sessions must NOT operate in different worktrees of the
  same repo (parallel-universe problem).
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
   to monitor sessions; cadence knob on the link.
5. **L2c Leader capability**: agent tool (message subject / fetch summary /
   list links), async replies as events.
6. **L3 Implicit hand-raiser**: pipeline start/session-activation hooks
   auto-granting `{read, inject}` over own-session runs.
7. **L4 Link UI**: rail multi-select + Link flow + grouping/badges +
   digest rendering (mockup pass first; flag-gated).
