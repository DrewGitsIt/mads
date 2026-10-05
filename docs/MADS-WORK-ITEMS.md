# mads Work-Item Decomposition

> Companion to REBUILD-SPEC.md and MADS-UX.md. Breaks the spec into build-ready work items,
> ordered by **dependency waves** — never priority labels. Every item is part of the launch
> design; feature-flagged functionality gets its schema hooks and flag plumbing in early waves
> even when its surface ships later. (§N) = spec section; (UX §N) = MADS-UX.md section;
> (M#) = mockup number in the prototype.

## How to read this

- **A wave is a dependency frontier, not a priority tier.** Items within a wave are
  parallelizable; an item may only depend on items in earlier waves (or explicitly named
  same-wave items). Nothing here is "phase 2" — agents building any item should design
  for the whole spec.
- **Each item is sized for one agent session** (§15 design principle 4): one service
  concern, one bounded deliverable, a verifiable "Done when."
- **Every item ships its tests.** Test obligations come from the Quality & Testing
  Strategy (§15) — an item is not done until `make test` covers its listed obligations
  and CI is green with `-race` and lint.
- **Done when = acceptance criteria.** When these items become mads tasks (post-dogfood),
  the item body maps directly: title → task title, Goal/Includes → description,
  Done when → completion signal, Depends → dependency edges.

## Wave map

| Wave | Theme | Items | Built by |
|---|---|---|---|
| 0 | Foundations | 2 | hand / Claude Code directly |
| 1 | Data Service | 7 | hand / Claude Code directly |
| 2 | Provider layer | 4 | hand / Claude Code directly |
| 3 | Minimal Pipeline Engine → **🥣 dogfood milestone** | 4 | hand / Claude Code directly |
| 4 | Router & Session Service | 6 | **mads pipeline** |
| 5 | Intelligence & capability runtime | 7 | mads pipeline |
| 6 | UI (Electron) | 11 | mads pipeline |
| 7 | Distribution & breadth | 7 | mads pipeline |

Critical path to dogfood: `W0.1 → W0.2 → W1.1 → W1.2 → W1.3 → W1.4 → W1.5 → W2.1 → W2.2 → W2.3 → W3.1 → W3.2 → W3.4`.

---

## Wave 0 — Foundations

### W0.1 Monorepo scaffold & CI
**Goal:** the repo every later item lands in, with the quality gates on from day one.
**Includes:** Go workspace layout (`services/data`, `services/router`, `services/session`,
`services/pipeline`, `pkg/…`, `providers/…`, `ui/`); per-service `make test` / `make run`;
golangci-lint config; CI running lint + `go test -race ./...` on every merge; the
service-README template implementing the agent-maintainability contract (§15: how to run
alone, how to test, API contract, critical paths).
**Done when:** a hello-world handler in each service directory builds, tests, lints green in CI.
**Depends:** —

### W0.2 Shared contracts package
**Goal:** one source of truth both sides of every boundary consume (§15 contract tests).
**Includes:** `pkg/contracts` — event envelope struct (seq/timestamp/type/session_id/task_id/actor/payload §15)
and the typed event-name registry seeded from the UX vocabulary (UX §4); task schema types (§3);
API error conventions (validate-all-at-once shape §10.3); golden-file helpers so producer and
consumer suites pin the same fixtures.
**Done when:** a sample producer test and consumer test consume the same golden and a contract
change fails both until updated together.
**Depends:** W0.1

---

## Wave 1 — Data Service

The single owner of persistent state (§15). SQLite WAL, exclusive access, HTTP API.

### W1.1 Data Service skeleton
**Goal:** a runnable, independently testable service shell.
**Includes:** HTTP server on fixed localhost port; SQLite WAL open/close; migration framework
(versioned, up/down); `/health`; config wiring (port, data directory §10); README per W0.1 template.
**Done when:** `make run` starts it standalone, curl works, migration up/down tests pass.
**Depends:** W0.2

### W1.2 Event log & transactional outbox
**Goal:** the control plane (§15). This is the system's spine — build it early, test it hardest.
**Includes:** append-only `events` table with indexes on `seq` and `(session_id, seq)`;
outbox helper so any state mutation + event append commit in one transaction; `POST /events`;
`GET /events?since=&session_id=&types=` serving indexed history then live pushes over SSE;
per-subscriber cursor resume.
**Test obligations (§15):** outbox atomicity (mutation and event commit or roll back together);
cursor-resume property tests — no gaps, no duplicates across reconnects; concurrent writers under `-race`.
**Done when:** a subscriber can disconnect mid-stream and resume from its cursor with provably
exact-once delivery of matching events.
**Depends:** W1.1

### W1.3 Task store
**Goal:** the task schema and lifecycle (§3), all mutations announced via the outbox.
**Includes:** core + orchestrator fields; CRUD + query API (`GET /tasks?session=&status=…`);
draft → confirm/discard lifecycle (§4); dependency edges with cycle detection exposed as a
launch-time check (cycles allowed at rest, block launch §3.6); status transitions emitting
`task.*` events; bulk import (§4).
**Done when:** every mutation produces its event atomically; cycle check flags a seeded cycle;
status-transition table matches §3.
**Depends:** W1.2

### W1.4 Write leases
**Goal:** contention-free shared task mutation (§15 lease model).
**Includes:** `POST /tasks/:id/claim` / release; only-holder-may-write enforcement; TTL with
implicit renewal on every write + cheap idle renewal call; expiry sweep → release, mark task
blocked, emit event; Router-initiated bulk release endpoint (for supervision, W4.1).
**Test obligations:** TTL expiry and renewal-on-write; contention under `-race` (two writers,
one lease, no lost updates).
**Done when:** a killed holder's lease expires like a parking meter and the task lands blocked
with an event — no polling anywhere.
**Depends:** W1.3

### W1.5 Pipeline template store & validation
**Goal:** templates as first-class stored artifacts (§6, M9).
**Includes:** template CRUD (`GET/PUT /templates`, used-by from workspace references);
`POST /templates/validate` running the same checks as pipeline launch — unknown agents, missing
loop-back targets, stage cycles — reported all at once (§10.3); validation logic in a shared
package the Pipeline Engine (W3.1) reuses verbatim; the default-implementation template seeded
editable (M9); saves version-stamped so in-flight runs keep their frozen copy (§6).
**Done when:** the seeded default validates; each seeded defect class reports together in one response.
**Depends:** W1.2

### W1.6 Registry stores: agents, capabilities, workspaces
**Goal:** the Library's data (§9, §12, §17).
**Includes:** agent definitions (prompt/capabilities/permissions/preferences/quadrant + tags §9);
capability records (Skill dir refs + MCP configs, credential env-var names §12–13); workspace
bundles (repos with primary pinned, defaults, credential references, template binding §17);
validate-all-at-once on writes; system agents + built-in skills seeded (§12).
**Done when:** CRUD round-trips all three; the six system agents exist after first migration.
**Depends:** W1.2

### W1.7 Session, file-tracking & commit-linkage records
**Goal:** the remaining persistent state (§1, §5, §16).
**Includes:** session metadata (name, activity, workspace, provider binding, summary field §16);
file-change records with **attribution per §5 (decided):** isolated worktree → everything
attributed to the session; non-isolated → agent tool-use + session-context commits only, other
observed changes stored as unattributed observations; commit records + the linkage chain
resolution order (trailer → session context → file heuristics → orphan flag §3/§5); schema
hooks for linked sessions (§1.12) and multi-repo tracking (§17) behind their flags.
**Done when:** attribution rules are table-tested for both isolation modes; an unlinkable
commit round-trips as orphan-flagged.
**Depends:** W1.2

---

## Wave 2 — Provider layer

### W2.1 Provider abstraction & capability-matrix registry
**Goal:** the uniform agent interface and the strategy registry mirroring §11's matrix.
**Includes:** provider interface (start / send / stream / interrupt / resume / transcript /
model discovery / cost report); both transports defined — ACP/stdio structured events and PTY;
per-provider strategy registry with each behavior declared native / emulated / unsupported;
the **conformance-suite harness** — one shared test suite that runs against any registered
provider and asserts its declared matrix is true (§15).
**Done when:** the harness runs against a stub provider and fails on any matrix/behavior mismatch.
**Depends:** W0.2

### W2.2 provider-fake
**Goal:** the keystone test double (§15) — a first-class deliverable, not an afterthought.
**Includes:** full provider implementation with scripted, deterministic behavior driven by
scenario files: respond with fixed text/streams, edit files, emit tool events, create tasks,
crash on cue, delay on cue; both ACP and PTY modes; declared 100%-native matrix.
**Done when:** conformance suite green; a scripted crash-on-cue scenario is consumable by a
downstream supervision test.
**Depends:** W2.1

### W2.3 claude-code adapter
**Goal:** the reference provider — every required behavior works here first (§11).
**Includes:** interactive sessions (spawn, stream, interrupt), one-shot non-interactive
invocation (`claude -p`), resume, transcript access + normalization (golden-pinned),
model discovery, token/cost reporting; matrix declared honestly (emulated/unsupported rows
explicit).
**Test obligations:** conformance suite green against the declared matrix; recorded-fixture
smoke tests opt-in, never CI-default (§15).
**Done when:** a scripted end-to-end exchange (start → message → stream → interrupt → resume)
passes on a machine with claude-code installed, and CI passes without it.
**Depends:** W2.1

### W2.4 Fast Model job runner
**Goal:** the one-shot LLM utility every transient job uses (§11.1).
**Includes:** baseline = one-shot harness invocation via the provider layer; optimization path =
user-configured OpenAI-compatible endpoint (§10 settings key, direct call when present);
prompt-in/structured-out helper with schema validation + retry; rate-limit surfacing
(inform-and-wait, never silent retry loops §11.9).
**Done when:** golden prompt-in/structured-out tests pass against provider-fake; endpoint
config flips the path without caller changes.
**Depends:** W2.2, W2.3

---

## Wave 3 — Minimal Pipeline Engine → 🥣 dogfood milestone

### W3.1 Engine skeleton & stage machine
**Goal:** template execution (§6): the state machine that takes ready tasks through stages.
**Includes:** load a frozen template copy (W1.5 shared validation on launch); stage progression
for `agent` and `command` stages; `when:` condition evaluation; `on_fail` loop-backs with
max-loop caps → blocked; dependency-ordered task scheduling with concurrency limit;
task claims via lease (W1.4); `pipeline.*`/`stage.*` events via outbox.
**Test obligations (§15):** stage-machine tests — conditions, loop-backs, max-loop → blocked;
concurrency-limit enforcement; all against provider-fake.
**Done when:** the default template runs a seeded 3-task graph on provider-fake with a scripted
review failure looping back exactly max_loops times, then blocking.
**Depends:** W1.4, W1.5, W2.2

### W3.2 Worktree lifecycle, merge & verify stages
**Goal:** isolation and return-to-trunk (§6, §17).
**Includes:** worktree-per-task creation before agent launch; post-commit hook installation
reporting task context (§5); merge as a command stage in dependency (topological) order;
loud conflicts → task blocked with explanation; verify stage on the merged tree catching quiet
conflicts; worktree cleanup after merge.
**Test obligations:** worktree lifecycle against real git in temp directories; a seeded
same-lines conflict blocks with the right feedback.
**Done when:** two independent tasks merge cleanly in order; a conflicting pair blocks the
second with accumulated feedback on the task.
**Depends:** W3.1

### W3.3 Dynamic work queue
**Goal:** mid-run task pickup (§6): the live queue, not a frozen snapshot.
**Includes:** engine subscribes to its session's `task.*` events (W1.2 filtered SSE);
same-session tasks created mid-run join the queue — cycle check re-runs at insert, task is
immediately confirmed; other-session tasks are untouched (they land in that session's backlog).
**Done when:** a task created (via API) while a run is active is picked up, scheduled after its
dependencies, and a cycle-introducing insert is rejected with a clear event.
**Depends:** W3.1 *(can land just after the milestone — the bootstrap doesn't need it day one)*

### W3.4 Bootstrap runner (`mads run`)
**Goal:** the dogfood entry point — drive a pipeline with no Router and no UI.
**Includes:** a thin CLI that seeds/reads tasks in the Data Service, launches a Pipeline Engine
run against a workspace + template, tails `pipeline.*`/`stage.*` events to the terminal, and
reports the outcome; documented in the repo as *the* way mads builds mads.
**Done when:** `mads run` executes a real work item from this document end-to-end with
claude-code agents in worktrees, producing linked commits.
**Depends:** W3.2, W2.3

> **🥣 Dogfood milestone.** From here on, work items are entered as mads tasks
> (dependency-ordered, per this document's Depends edges) and executed by the bootstrap
> pipeline. Every subsequent wave both builds the product and battle-tests the engine.

---

## Wave 4 — Router & Session Service (interactive path)

### W4.1 Router core: supervision & lifecycle
**Goal:** the always-running, user-facing coordinator (§15).
**Includes:** HTTP server for the UI (default port 17373, configurable); spawn/kill of Session
and Pipeline Engine child processes with ephemeral port/unix-socket discovery; **supervision as
primary liveness** — on child exit, release its leases immediately via W1.4's bulk endpoint and
emit the failure event (TTL remains the backstop only); session and pipeline lifecycle endpoints
(create, terminate, bounce, start/pause/resume/cancel).
**Test obligations (§15):** child crash → immediate lease release; provisioning-sequence event ordering.
**Done when:** killing a child process externally lands its tasks in blocked-with-event within
one supervision tick, verified against provider-fake sessions.
**Depends:** W1.4, W3.1

### W4.2 Router streams: SSE fan-out & event forwarding
**Goal:** the UI's single subscription point (§15 two planes).
**Includes:** control-plane: per-view filtered subscriptions proxied to the Data Service event
API with cursor pass-through; data-plane: forwarding live agent-output streams from
Session/Pipeline processes to UI SSE, never persisted; reconnect-with-cursor semantics.
**Test obligations:** fan-out and server-side filter correctness; reconnect resumes without gaps.
**Done when:** two concurrent UI subscribers with different filters each receive exactly their
events across a forced reconnect.
**Depends:** W4.1

### W4.3 Session Service core
**Goal:** one process per active session owning the agent (§1, §15).
**Includes:** agent process ownership via the provider layer (ACP and PTY); message send,
streaming to Router, interrupt, restart-agent ("Bounce"), model switch within provider (§11.5),
resume via provider strategy; session provisioning sequence emitting `session.provisioning`
events (worktree → hook → agent starting → MCP up → delivered, UX §3.2); implicit creation on
first message (§1).
**Test obligations (§15):** ACP and PTY adapters against provider-fake; resume, interrupt,
restart, model-switch flows; transcript normalization pinned by goldens.
**Done when:** a full scripted conversation round-trips through provider-fake in both display
modes with the provisioning strip events in order.
**Depends:** W4.1, W2.2, W2.3

### W4.4 File observation & commit linkage
**Goal:** the tracking layer (§5) with the decided attribution rules.
**Includes:** FSEvents/inotify watcher per worktree (lazy expansion / `git status` fallback for
giant repos §17); agent tool-use event capture; post-commit hook handling; the linkage chain
(trailer → session context → heuristics → orphan); attribution per isolation mode (W1.7 rules)
enforced at the recording site; multi-repo *tracking* when the workspace declares repos
(isolation stays primary-repo-only §17).
**Done when:** table-driven scenarios — agent edit, user edit in isolated worktree, user edit
non-isolated, script edit, trailered commit, untrailered commit — each land with the §5-correct
attribution.
**Depends:** W4.3, W1.7

### W4.5 Interactive worktrees & merge-back
**Goal:** session isolation and user-triggered return (§17).
**Includes:** worktree-by-default in git repos with opt-out; unmerged-worktree flagging on
inactive sessions; `POST /sessions/:id/merge {target_branch}` — auto-attempt, clean merge
completes, dirty merge returns structured conflict data for the UI's resolution flow;
`merge.attempted|conflicted|completed` events.
**Done when:** clean and conflicted merges both round-trip against real git in temp dirs with
the right events and session flags.
**Depends:** W4.3, W3.2

### W4.6 Transient-job runner
**Goal:** the spawn-run-exit pattern for Fast Model jobs (§15).
**Includes:** Router-side job spawning + completion reporting to the UI; the job contract
(reads/writes via Data Service only, Fast Model via W2.4); job status events; used by summaries,
retros, agent generation, and selection scoring (wave 5).
**Done when:** a no-op sample job runs through spawn → work → exit with status events observable
over W4.2.
**Depends:** W4.1, W2.4

---

## Wave 5 — Intelligence & capability runtime

### W5.1 Synthesis engine
**Goal:** the note-taker (§4) — optional by construction.
**Includes:** Off/Light/Full trigger modes (§10 settings); transcript-delta analysis via Fast
Model; draft tasks and typed notes written via lease with `draft` provenance; confirm/discard
transitions; debounce config.
**Test obligations (§15):** prompt-in/structured-out goldens against provider-fake;
draft → confirm/discard state transitions.
**Done when:** a scripted conversation yields the golden draft set in Full, the high-confidence
subset in Light, nothing in Off.
**Depends:** W4.3, W4.6

### W5.2 Agent selection & ranking
**Goal:** auto-select (§9): capability filter → quadrant filter → deterministic ranking.
**Includes:** capability set-intersection filter; signal-word quadrant classification; ranking
(failure exclusion → label affinity → track record from the event log → recency → alphabetical);
every selection logged with scores for retro evaluation; scorer behind a swappable interface.
**Done when:** table-driven selection cases are reproducible (same inputs → same agent) and each
selection's score breakdown lands in the event log.
**Depends:** W1.6, W4.6

### W5.3 Agent generation & builder assistance
**Goal:** agents on demand (§9.5–9.7).
**Includes:** generation job (task context → full draft definition, ephemeral unless saved);
NL → agent for the Library editor; enrichment endpoints (describe from prompt, suggest
capabilities, refine wording); similar-agent detection warning.
**Done when:** golden generation tests pass on provider-fake; a near-duplicate request returns
the similarity warning with the matched agent.
**Depends:** W5.2

### W5.4 Session summary job
**Goal:** the compressed narrative (§16).
**Includes:** trigger on active → inactive; derived from task data, not transcripts; three
sections, empty sections omitted; written to `session.summary`; regenerates on each deactivation.
**Done when:** golden summaries for seeded sessions; re-deactivation overwrites.
**Depends:** W4.6, W1.7

### W5.5 Retro engine
**Goal:** the improvement loop (§7).
**Includes:** retro job over selected sessions (tasks, file changes, metadata + user
observations); DPA generation with full field set (problem/evidence/root_cause/
remediation_layer/suggested_action), default-accepted; cancellable mid-generation with progress
events; accept/reject/edit persistence; create-follow-up-session from accepted DPAs;
retro-of-retros = selecting retro-generated sessions (tag-aware analysis, no separate mode).
**Done when:** a scripted retro over provider-fake sessions yields golden DPAs; cancel mid-run
leaves no partial state.
**Depends:** W4.6, W5.4

### W5.6 Capability runtime: MCP lifecycle, health & skills
**Goal:** capabilities as living things (§12).
**Includes:** MCP server lifecycle — system-wide, start-if-not-up at session start, explicit
stop; health checks (server reachable, skill dir exists) + `capability.health_changed` events;
guided-diagnostics backend (§12.7); skill availability (provider config setup at agent create)
and the three injection paths (user-directed, system-directed, /command §12.4); **basic
credential storage + scoped injection** (§13.1–13.6: restricted-permission local store, agents
receive only declared capabilities' env vars, graceful degradation when missing) — env-var
import and vault integration follow in W7.4.
**Done when:** an agent with a declared MCP capability gets the server started and its env vars
injected; an undeclared capability's token is provably absent; a missing credential degrades
without crash.
**Depends:** W1.6, W4.3

### W5.7 Metrics query views
**Goal:** the dashboard's data (§8) — queries over the event log, no separate store (§15).
**Includes:** `GET /metrics/*` views: cost over time by model/session/pipeline, agent
leaderboard (usage, success rate, retries), task outcomes, capability usage incl. never-used,
pipeline run history; computed from event fixtures so it tests without live providers.
**Done when:** each view returns golden aggregates over a seeded event log.
**Depends:** W1.2

---

## Wave 6 — UI (Electron)

Every item implements its mockup's data contract (UX §3) against the real Router + provider-fake.
The design system source of truth is MADS-UX §2 + the approved palette; the prototype at
`~/Downloads/mads-ux/mockup-01-session-workspace.html` is the visual reference (M1–M9).

### W6.1 App shell & design system
**Goal:** the foundation every surface composes from.
**Includes:** Electron app scaffold (TS, packaging per platform); token system from the approved
palette (warm charcoal grounds, clay accent, semantic ok/warn-draft/bad, chart categorical set —
all validated values); type rules (sans chrome / mono for code-IDs-paths); component kit
(buttons per hierarchy, chips, cards, panels/side-sheets, toasts, resizable panel primitive);
collapsible icon rail with hover-peek; provenance `draft` treatment as a shared component.
**Test obligations (§15):** component tests for tokens + core components.
**Done when:** a storybook-style gallery renders every component in both themes and CI runs its tests.
**Depends:** W0.1 *(UI work can start pre-wave-4 against mocked contracts, but E2E sign-off needs W4.2)*

### W6.2 Session list & Launchpad (M4, UX §3.1–3.2)
**Includes:** Active/Recent rail with amber-dot attention signal; hero composer with approved
greeting; metadata pills with popovers (workspace/agent/model-grouped-by-provider/trust/isolation,
multi-repo checklist behind flag); suggestion chips as Data Service queries; implicit creation +
observable setup strip from `session.provisioning`; teaching empty states.
**Done when:** Playwright: launch → type → session appears with provisioning steps streaming →
composer docked, against provider-fake.
**Depends:** W6.1, W4.3

### W6.3 Conversation tab (M1, UX §3.3)
**Includes:** consolidated header (name/status/pills/context meter/actions, Shell + Restart Agent
in kebab); chat renderer (markdown + collapsible tool cards) and terminal renderer (xterm.js on
real node-pty); full-width stream with ~92ch prose measure; summary card on inactive sessions;
composer with ⌘E expansion, @ mentionables and / commands resolved via `GET /mentionables` +
capability-filtered skills; injected-content preview; docked resizable shell.
**Done when:** Playwright happy path in both display modes; vim runs in the shell without lockup.
**Depends:** W6.1, W4.3, W4.2

### W6.4 Tasks tab & Context Rail (M2, UX §3.3)
**Includes:** list/graph toggle; graph with layout engine, minimap, wheel + control zoom, cycle
highlighting; **Queued display rule** (schema `blocked` renders calm with dep chips; red = needs
human); detail side sheet with grouped typed notes, all fields editable; draft cards with
confirm/edit/discard; bulk import; Context Rail (Tasks/Changes tabs, status quick-filters).
**Done when:** Playwright: create → link deps → see Queued → confirm a draft → graph updates live
from `task.*` events.
**Depends:** W6.1, W1.3, W5.1

### W6.5 Files tab & merge-back flow (UX §3.3, §5, §17)
**Includes:** session and per-task change lists with A/M/D + attribution chips (incl.
unattributed observations); Monaco diff side-by-side/inline; open-in-editor; merge-back:
target-branch pick → auto-attempt → embedded merge editor or external-editor handoff on conflict.
**Done when:** Playwright: seeded conflict surfaces the resolution flow; clean merge completes
with `merge.completed` reflected in UI.
**Depends:** W6.1, W4.4, W4.5

### W6.6 Pipeline tab (M3, UX §3.3)
**Includes:** run header (Start/Pause/Resume/Cancel, elapsed + counts, no live cost §8);
stage-column board moving tasks on `stage.*` events; drill-in split panel (stage timeline with
attempts/loop-backs/feedback + live agent terminal + per-task files with touched count),
drag-resizable; blocked tasks prominent with accumulated feedback; retry/reassign actions.
**Done when:** Playwright: full provider-fake run watched live — loop-back visible, block
surfaces feedback, drill-in streams.
**Depends:** W6.1, W3.3, W4.2

### W6.7 Retro surface (M5, UX §3.4)
**Includes:** session picker (checkboxes, kind filters All/Work/Retro-generated, clear-selection,
collapses to chip strip post-generation); observations input; cancellable 3-step generation on
`retro.*` events; DPA cards where the status chip is the toggle; "Create follow-up session
(N accepted)" CTA; history list.
**Done when:** Playwright retro round-trip on provider-fake incl. cancel and chip-toggle.
**Depends:** W6.1, W5.5

### W6.8 Library (M6 + M9, UX §3.5)
**Includes:** four shelves — Agents (cards + editor with NL-generate draft styling, prompt
scaffold, enrichment, similar-agent warning, validated model dropdown, quadrant tooltip,
import-from-provider mapping preview); Capabilities (health dots, Diagnose panel, Skill/MCP
editor with env-var credential refs); Workspaces (editor incl. repos, defaults, template
binding, used-by); **Pipeline templates** (dual-mode visual/YAML editor always in sync, full §6
grammar on stage cards, Validate all-at-once, save-never-disturbs-in-flight); Browse Registry
behind the `remote-registry` flag.
**Done when:** Playwright: create an agent via NL flow; edit + validate a template with a seeded
error set reported together.
**Depends:** W6.1, W1.5, W1.6, W5.3, W5.6

### W6.9 Dashboard (M7, UX §3.6)
**Includes:** date presets, stat tiles, stacked cost-by-model chart using the validated
categorical palette (per-column tooltips + table toggle), agent leaderboard, task outcomes,
capability usage with never-used flags, run history with drill-back to the session's Pipeline
tab; live widget on `session.*`/`pipeline.*`, charts refresh on view.
**Done when:** renders golden aggregates from W5.7 fixtures; chart passes the palette validator
in both themes.
**Depends:** W6.1, W5.7

### W6.10 Settings & first-run (M8, UX §3.7, §3.9)
**Includes:** all panels — General (restart badges), Profiles (§10.5), Providers with the
capability-matrix status table (§11), Credentials manager (masked CRUD + env import + vault
link), Pipeline & synthesis with cost hints, Advanced (feature flags incl. linked-sessions,
multi-repo, remote-registry), Updates/About two-column; validate-all inline errors; first-run
wizard (welcome → provider preflight → credential import → first workspace → Launchpad).
**Done when:** Playwright: change a restart-required setting and see the badge; wizard completes
on a clean data dir.
**Depends:** W6.1, W5.6, W7.3 *(preflight backend — wizard can stub until it lands)*

### W6.11 Command palette & notifications (UX §3.8)
**Includes:** global ⌘K over sessions/tasks/actions from already-subscribed caches; dock/tray
badge for needs-attention; the passive notification model (§8.5 — resolve UX open question 5:
badge-only at launch, in-app inbox deferred with schema hooks).
**Done when:** ⌘K jump-to-session and start-pipeline dispatch correctly; badge tracks
attention count.
**Depends:** W6.2

---

## Wave 7 — Distribution & breadth

### W7.1 kiro-cli adapter
**Goal:** the second provider that keeps the abstraction honest (§11).
**Includes:** full strategy set with an honest matrix (emulated/unsupported rows explicit, UI
degradation strings); conformance suite green; recorded-fixture smokes opt-in.
**Done when:** the same conformance suite that passes claude-code passes kiro-cli's declared
matrix without core changes — proving §11.8.
**Depends:** W2.1, W2.3

### W7.2 Installer & uninstall
**Includes:** curl-to-shell installer (platform detect, binary placement, PATH); data-dir
creation with default config on first run; `mads uninstall` with explicit confirmation /
`--keep-data` (§14.7); homebrew tap as stretch.
**Done when:** install → first-run → uninstall smoke green on macOS and Linux runners.
**Depends:** W0.1 (builds), W6.10 (first-run)
**Note (Drew, 2026-10-05): offer mergiraf after install.** Add a post-install setup step
that checks whether [mergiraf](https://mergiraf.org) is on the system (the same lookup
`pkg/mergedriver` uses: PATH, Homebrew's prefixes, `~/.cargo/bin`). If it isn't, ask the
user whether to install it, saying what it's for: it lets Land and pipelines merge code
edits that touch the same file automatically. On yes, install with `brew install
mergiraf` where Homebrew is available, else download the release binary for the platform
from <https://codeberg.org/mergiraf/mergiraf/releases>. Never install it silently, and
never make it a build dependency: mads works without it. W7.3's preflight already reports
it as an optional row (version and path, or this install guidance); this step is the
installer-time counterpart. Not built yet.

### W7.3 Preflight & provider compatibility
**Includes:** on-demand health check of binaries, dependencies, services with fix guidance
(§14.8); mads-version ↔ provider-version compatibility matrix with pre-use warnings (§14.9);
surfaced in Settings and the first-run wizard.
**Done when:** a seeded broken dependency reports what's broken and how to fix it.
**Depends:** W4.1
**Note (2026-10-05):** preflight has an optional `mergiraf` row, a warning when absent
(never a blocker) with `brew install mergiraf` and the release-binary link. The installer
prompt to install it is recorded under W7.2.

### W7.4 Credentials: env import & vault integration
**Includes:** environment-variable scan + import flow (§13.7); vault sources (1Password CLI,
AWS Secrets Manager) resolving referenced credentials at runtime (§13.8); builds on W5.6's
storage + scoped injection.
**Done when:** an env-imported and a vault-resolved credential both inject into a declared
capability; neither ever appears in another workspace's sessions.
**Depends:** W5.6

### W7.5 Update lifecycle
**Includes:** version check + release notes in Settings (user-initiated only §14.3); graceful
agent stop → checksum-verified download → apply → restart → sessions resume; automatic rollback
on failure; backwards-compatible data migrations exercised (§14.4–5).
**Done when:** update smoke passes incl. a forced mid-update failure rolling back cleanly with
no session data lost.
**Depends:** W7.2

### W7.6 Remote registry (feature-flagged)
**Includes:** registry client (browse, install, user-initiated update, version pick §12.9);
publish flow with explicit checkboxes; nav hidden until `remote-registry` flag enabled.
**Done when:** install/update/publish round-trip against a local test registry server.
**Depends:** W1.6, W6.8

### W7.7 Full E2E smoke & release gate
**Goal:** the two standing gates from §15, wired as CI jobs.
**Includes:** the full-topology E2E — spawn everything, scripted session → tasks → pipeline →
retro on provider-fake, on every merge, kept to minutes (seeded headless at wave 4, extended
with Playwright at wave 6 — this item completes and enforces it); the release gate —
cross-platform build matrix + install/first-run/update/uninstall smoke on macOS and Linux.
**Done when:** both gates block a deliberately broken candidate build.
**Depends:** W6.x (all), W7.2, W7.5

### W7.8 goose adapter (via the generic ACP engine)
**Goal:** third provider, and the first instantiation of the declarative direction — all ACP
wire behavior lives once in `providers/acprpc` (proposed W7.9); goose contributes only
capture-verified knob values, the trust table, and its non-ACP CLI surfaces (transcript export,
session locator, PTY).
**Includes:** honest matrix (OneShot/OneShotStream Emulated over ACP); conformance green
against a scripted stand-in; opt-in real-binary smoke (`MADS_GOOSE_SMOKE=1`, free local
ollama); registration across router/session/pipeline services + `mads run`; preflight +
compat rows.
**Design:** docs/GOOSE-ADAPTER-DESIGN.md (goose wire facts, capture provenance) +
docs/DECLARATIVE-PROVIDERS-DESIGN.md (engine architecture).
**Done when:** the claude-code conformance suite passes goose's declared matrix without core
changes, and the real-binary smoke is green on a machine with goose + ollama installed.
**Depends:** W2.1, W2.3, W7.1 (adapter conventions)

---

## Flagged functionality ledger

No item above is "phase 2," but three features ship dark behind flags (§10.6) with their hooks
built in-wave:

| Flag | Hooks built | Surface when enabled |
|---|---|---|
| `remote-registry` | W1.6 registry schema | W6.8 nav entry + W7.6 flows |
| `multi-repo` | W1.7 tracking schema, W4.4 multi-repo tracking | Launchpad isolation checklist (W6.2) |
| `linked-sessions` | W1.7 schema hooks, W6.10 flag toggle | Leader/monitor UI — design deferred (UX open Q3), post-launch item |

## Running the decomposition

1. Waves 0–3 are executed directly (Claude Code sessions against this document), in critical-path
   order, parallelizing within waves where the Depends edges allow.
2. At the 🥣 milestone, remaining items are imported as mads tasks — one task per item, Depends
   edges as dependency links, "Done when" as the completion signal — and run through the
   bootstrap pipeline (architect → executor → build → code_review → merge → verify).
3. Retro after each wave (§7) — the build itself is the first real workload for the improvement loop.
