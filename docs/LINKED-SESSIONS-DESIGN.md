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

Presets: **Monitor** = `{read}` · **Hand-raiser** = `{read, inject}` ·
**Leader** = `{read, inject, orchestrate}`. Users pick presets; contracts see
grants, so new combinations need no schema change.

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
   boundary): it receives run digests and may surface findings to its user or
   inject guidance, exactly the shepherding pattern proven manually.

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
