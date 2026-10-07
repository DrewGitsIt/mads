# mads UX draft

> Companion to REBUILD-SPEC.md. Establishes the common language for surfaces and user flows, and maps every surface to its data contract (REST reads, event subscriptions, Router actions). Derived from tao's UI (screenshots 1–27, reviewed 2026-07-08) adapted to the mads spec and Electron.
>
> tao screenshot numbers are referenced as (#N). Spec sections as (§N).

---

## 1. Information Architecture

Five top-level areas in a left icon rail (replacing tao's top-right button row). The rail is collapsible — collapsed, it becomes a thin edge strip that flies out on hover:

| Area | Contains | tao lineage |
|---|---|---|
| **Sessions** | Session list + Session Workspace. Pipeline execution lives *inside* the session. | Chat (#1–10) + Build (#11–13), merged |
| **Retro** | Retro composer, results, history | Retro (#14–16), kept nearly as-is |
| **Library** | Agents, Capabilities, Workspaces, Pipeline templates; remote registry browsing | Toolbox (#17–23), simplified + new surfaces |
| **Dashboard** | System-level observability: cost, usage, outcomes (§8) | New — tao had none |
| **Settings** | Config, profiles, credentials, providers, updates, About | Settings (#25–26) + Secrets (#24) + About (#27) |

**Structural changes from tao:**
- **Build page is eliminated** (your note #11). Autonomous execution is a tab within the Session Workspace — same session, same tasks, no context switch. The good parts of Build (per-task files, live agent terminal) move into the Pipeline tab.
- **Secrets move into Settings** (§13.4 mandates credentials live in settings). The manager UI itself is kept (#24 — "this is cool").
- **DAG and task detail stop being modals.** DAG is a view within the Tasks tab; task detail is a side sheet with room to breathe (notes #8, #9, #12).
- **Knowledge docs are gone as a concept** (note #18, finalized in mockup review). Reference material lives inside skill directories per the standard format (SKILL.md + nested content) or in workspace steering files — no attachment slots anywhere.
- **CLI capability type is gone** (note #23): Skills (optionally bundling an executable, industry-standard format) + MCP servers only (§12).

---

## 2. Design System Rules

The contract for "more beautiful." Every mockup and eventually every component obeys these; they fix the systemic issues in the screenshots.

1. **Two type families, strict roles.** Sans (Inter-class) for all UI chrome — nav, cards, labels, buttons, task titles. Mono only for: code, terminal, diffs, file paths, IDs, log excerpts. tao's biggest visual problem is monospace-everywhere in some views (#1 sidebar, #14 retro) vs sans in others (#12) — one rule ends it.
2. **One density scale.** 4/8px spacing grid; consistent row heights per component class (list row, card, table row). No per-view invented densities.
3. **Semantic color tokens.** Neutral dark base (tao's Nord-ish palette is a fine start), plus tokens: `status-active` (green), `status-attention` (amber), `status-blocked` (red), `draft` (yellow — synthesis cards §4), `diff-add/mod/del`, one accent for primary actions. All text meets WCAG AA — the low-contrast file paths (#13) and muted-on-dark labels are defects, not style.
4. **Elevation model: panels over modals.** Desktop app affordance — split panes, side sheets, and docked panels are the default. Modals only for confirmations and small single-purpose forms (publish, add credential). tao's DAG (#8), task edit (#9), and compose (#7) all graduate from modal to panel.
5. **Every empty state teaches.** One line of guidance + one primary action, centered. No dead panes (#1's uninviting center, #2's empty summary sections — sections with no content simply don't render).
6. **Button hierarchy.** Primary (accent, one per view), secondary (outline), ghost (icon/text). Fixed sizing scale — no full-width buttons unless the layout column is narrow (kills the overly wide +New, #1).
7. **One icon set** (Lucide-class), one custom scrollbar style app-wide (#10).
8. **Provenance is visible.** Anything an LLM wrote and a human hasn't confirmed carries the `draft` treatment (yellow border/badge) — synthesis cards, generated agents, DPA suggestions. Confirmed/human content never does. (Fixes #2's unlabeled provenance complaint at the system level.)
9. **Inviting by default.** The default identity is gender-neutral, pleasant, and a bit earthy: warm neutral grounds, a soft clay accent, gentle contrast — mads should encourage a 65-year-old and a 15-year-old equally on first open. Aggressive/hacker aesthetics are optional themes, never the default. Action labels are descriptive words, not jargon ("Restart Agent", not "Bounce"). Approved palette direction (mockup 1 rev 2): warm charcoal grounds `#1A1713`–`#2F2A23`, clay accent `#C08552`, sage/sand/soft-brick semantics.

**Electron-native expectations:** real PTY via node-pty (fixes the vim/`git commit --amend` lockups, #6 — that's a wiring bug class, not an xterm.js limit; VS Code proves it), resizable docked shell (#5), global hotkeys, ⌘K command palette, OS drag-and-drop, dock/tray badge for needs-attention sessions, optional pop-out session windows.

---

## 3. Surface Inventory & Data Contracts

Contract notation:
- **Reads** — REST via Router (source: Data Service, or provider transcripts §11)
- **Subscribes** — control-plane event log filters (§15), plus data-plane streams where noted
- **Actions** — Router endpoints the surface can invoke

### 3.1 Session List (left rail of Sessions area)

Persistent sidebar. Groups: **Active** (live dot) and **Recent** — sessions with open tasks stand out inside Recent via the amber dot + open-task count, not a separate "needs attention" bucket (§1's derived signal, minus the scolding). Each row: name, workspace badge, relative time, open-task count. Search + ⌘K. `+ New` is a normal-width primary button. No "All completed" bulk affordances (#14).

- **Reads:** `GET /sessions` → id, name, activity, workspace, repo, last_activity, open_task_count, summary (first line)
- **Subscribes:** `session.*`, `task.created`, `task.status_changed` (all sessions — cheap, low rate)
- **Actions:** `PATCH /sessions/:id` (rename), `DELETE /sessions/:id` (confirm dialog), select → loads Workspace

### 3.2 New Session (Launchpad)

Replaces #1's empty state. Centered hero composer — greeting copy (approved): "What can I help you with?" / "Describe your goal — I'll help you get there." Large input, inviting (the Claude/Gemini pattern you cited). Below it, one compact row of **pills**: Workspace · Agent · Model · Trust · Isolation — each a popover, defaults pre-filled from workspace/profile precedence (§10), never full-width dropdowns. Below the pills, suggestion chips fill the composer. While on the Launchpad, the Context Rail shows its teaching empty state (design rule 5). First message → session persists implicitly (§1), composer docks to bottom, Workspace layout appears.

**Suggestion chips are queries, not canned copy**: sessions with open tasks ("Resume: …"), the oldest ready task, recent retro DPAs — plain Data Service reads, no LLM call. Only a first-run user with no history sees teaching examples.

**Model × provider**: the model popover groups models under provider headers (§11: the provider is the harness — `claude-opus-4.6 · claude-code` and `claude-opus-4.6 · kiro-cli` are distinct entries because tools, resume, and transcripts differ). Until the first message the session isn't bound, so choosing a model under another provider switches the provider too; after creation the popover lists only the bound provider's models (§11.5).

**Multi-repo isolation is feature-flagged** (§10, §17 — spec §1.13 now reconciled): with the flag on, the isolation popover's third option deepens into a checklist of the *workspace's declared repos*, primary pinned; each checked repo gets its own tracked worktree and commit hook (§5). There is deliberately no free-form repo picker at session start — declaring the repo set is what workspaces are for.

**What backs @ and /**: both are mads-level and provider-independent — they resolve at send time into injected content (§2.4) before anything reaches the harness, so they never depend on the model below. `@` searches the mentionables index: skill reference docs (inside skill directories, §12), session summaries (§16), workspace steering files. (There is no separate knowledge-document entity — reference material lives inside skills, per the standard format.) `/` lists mads commands (`/task`, `/model`, `/agent`) plus registry skills (§12), badged by capability health and filtered by the selected *agent's* capability bindings — never by model. Provider-native slash commands (e.g., kiro's `/rewind`) exist only inside terminal-mode passthrough.

**Between Start and the live session** (~1–3s, fully observable): the pane morphs optimistically — the typed message becomes the first user bubble, the composer docks, and the sidebar gains a row with a provisional name (first line of the message; the Fast Model refines it asynchronously). Above the message, a **setup strip** streams provisioning steps as their control-plane events land: worktree created → post-commit hook installed (§5) → agent process starting (provider) → MCP servers up (§12.5) → message delivered — then tokens stream and the strip collapses. A failed step surfaces inline with a retry action. No spinner-only limbo, no black box (§18). Emits `session.provisioning` events.

- **Reads:** `GET /workspaces`, `GET /agents` (picker + quadrant suggestions §9), `GET /providers` + `GET /providers/:id/models` (grouped model popover), `GET /sessions?attention=true` + `GET /tasks?status=ready` (suggestion chips), `GET /mentionables` (@ index), `GET /capabilities?type=skill` (/ menu)
- **Actions:** `POST /sessions` (first message creates), pill edits are staged client-side until creation

### 3.3 Session Workspace

The heart. There is no header bar or subheader above the conversation (the consolidated header this section first proposed was built, then retired on 2026-10-07). The session's name and status are on its rail card; everything about the session itself is in the **session dock** at the bottom edge (below). The shell panel toggles with ^`. "Continue" is renamed **Resume** (#2).

Center pane tabs: **Conversation · Tasks · Files · Pipeline**. Right side: **Context Rail** (§4's constant pane). Bottom: the session dock. Optional docked, **resizable** shell panel (#5).

#### Conversation tab
Chat renderer (markdown + collapsible tool cards) or terminal renderer (xterm.js on real PTY) per global display mode (§2). The stream is full-width — no centered reading column; agent prose keeps a readable measure (~90ch) while tool cards and code blocks use the available room. Inactive sessions show the **session summary card** at top (only non-empty sections — §16 structure: accomplished / decisions / remaining) above the transcript. Injected content preview before send (§2.4).

**The session dock** (`ui/src/conversation/Dock/`) is one edge-to-edge band under the conversation, in both views. No inner boxes, no textarea border; focus tints the whole band. Top to bottom:

1. **Rim.** The dock's 2px top hairline *is* the context meter. Its fill is occupancy: quiet below 70%, amber from 70%, red from 90%. Hovering it shows `106.5k / 128k · 83% context`. From 70% the label stays up, and from 80% it carries **Compact** (chat only). With no usage recorded, the rim is a plain hairline with no label, never a stale or invented number.
2. **Attachment chips** (chat).
3. **Input row** (chat only): **+** · an auto-growing textarea · Stop while a turn runs · **Send**, or **Queue** while busy.
4. **Expanded editor** (⌘E): grows in place, no modal (#7). Monaco with a line-number gutter and word wrap, then an action bar with **+**, the send/collapse hint, and Stop / Send / Queue.
5. **Status line** (one 30px line): persona ▾, model ▾ and trust ▾ (dot coloured by level) on the left; on the right `⎇ workspace / branch → base` and the **Commit / Land** control. Each segment opens its picker upward. The ⎇ popover says where the session works (folder, worktree, branch, status) and offers **Commit…** and **Land into base**. Land outcomes and refusals answer in that popover, at the button, as well as on the card in the conversation (SESSION-WORKTREES-DESIGN.md D4–D6).

The **+ menu** holds everything about input and display: Attach files…, Mention, Commands, Expand editor, Keyboard shortcuts; **View: Chat | Terminal**; a context row with a bar, and Compact now.

**Narrow panes** shed text, never controls. Below ~700px persona and model show short names, the branch a short id, and "Trust: " drops to the bare value. Below ~520px persona and model go icon-only, the branch hides (`⎇ → main`), and the rim label is the bare %. The trust value and the Commit/Land control never collapse.

**Terminal view** keeps the same dock under the xterm, so switching views moves nothing but the input. The xterm *is* the input: no input row, chips, queue or working line. A **💬 Chat** segment leads the status line (the way back) and a **⌘E compose** segment sits before ⎇. There is no + menu and no Compact (type `/compact` in the TUI); mads `@` `/` `#` commands are chat only, and PTY input is never scanned. ⌘E compose opens the same line-numbered editor as a **sheet rising from the dock over the xterm**. It never resizes the terminal, since a PTY resize makes full-screen TUIs redraw. **Paste into terminal** sends it as a bracketed paste. "Ask the agent…" actions (the ⎇ popover's fixes) open this sheet prefilled, for the user to review and paste. The rim shows the session's recorded usage if there is any; in this view context depends on the TUI.

**Hotkeys** come from one table, `ui/src/platform/hotkeys.ts` (`DEFAULT_BINDINGS`, with `setHotkeyOverrides()` as the hook for a future Settings override). Modifiers match exactly, and on Linux/Windows no plain Ctrl+letter is bound, because readline owns those in a terminal.

| Action | Mac | Linux/Windows |
|---|---|---|
| Expand editor / compose | ⌘E | Ctrl+Shift+E |
| Send from the editor | ⌘⏎ | Ctrl+⏎ |
| Collapse the editor | Esc | Esc |
| Chat ⇄ Terminal | ⌘⇧T | Ctrl+Shift+T |
| Attach files | ⌘⇧A | Ctrl+Shift+A |

- **Reads:** `GET /sessions/:id/transcript?page=` (normalized, §11), `GET /sessions/:id` (summary field)
- **Subscribes:** **data-plane** `GET /sessions/:id/stream` (SSE: tokens, tool events, live status); control: `session.activity_changed`
- **Actions:** `POST /sessions/:id/messages` (text + attachments + @-mentions + /skill injections), `/interrupt`, `/bounce`, `/shell`, `/fork` (rewind §2.11), `PATCH /sessions/:id` (model/agent/trust)

#### Tasks tab
Full task management. **List view** (default) and **Graph view** toggle — same data (#8's Graph/Table, inverted priority). Graph: proper layout engine, crisp edge routing, **minimap kept** (#8 — you love it), status-colored nodes, cycle highlighting (§3.6 — cycles allowed in editor, block launch). Interactions: drag to pan; zoom via mouse wheel and on-canvas controls. **Display rule (approved)**: dependency-waiting tasks render as calm "Queued" (dashed ring, `← dep` chips) even though their schema status is `blocked` (§3) — red is reserved for tasks that need a human. Task click → **detail side sheet** (not dialog): full-width fields, markdown rendering for description/criteria/notes, collapsible notes grouped by type (decision/finding/dead_end/test_result/observation §4) instead of #12's wall of text. All fields editable (§3.7). Bulk import lives here (§4).

- **Reads:** `GET /tasks?session=:id` (core + orchestrator fields), `GET /tasks/:id/notes` (on demand, §4)
- **Subscribes:** `task.*`, `note.*` filtered to session
- **Actions:** `POST /tasks`, `PATCH /tasks/:id` (incl. dependencies), `POST /tasks/:id/confirm|discard` (drafts), `POST /tasks/import`, `POST /sessions/:id/pipeline/start` (runs cycle check, launches)

#### Files tab
Session-level and per-task file changes (§5 dual granularity). File list with A/M/D badges + attribution (task chip, actor). Monaco diff: side-by-side/inline toggle (#10), **consistent scrollbars**, AA-contrast paths (#13). "Open in editor" always visible (§5). **Merge back** button here and in header: pick target branch → auto-attempt → conflict resolution in embedded merge editor or external editor (§17). *Superseded: an isolated session lands through **Land** in the session dock's status line (SESSION-WORKTREES-DESIGN.md D6); the merge-back flow is retired.*

- **Reads:** `GET /sessions/:id/changes?task=` (diffs, attribution)
- **Subscribes:** `file.changed`, `commit.created`, `commit.linked` (session filter)
- **Actions:** `POST /system/open-editor {path}`, `POST /sessions/:id/merge {target_branch}`

#### Pipeline tab
Replaces the Build page (#11–13). Appears when a run exists. Layout: run header (status, started, concurrency, cost-so-far is **not** shown live — §8 after-the-fact; show elapsed + counts) with **Start/Pause/Resume/Cancel**. Task board grouped by pipeline stage (columns = stages from the template §6; tasks move across as `stage.started/completed` events arrive). Click a task → split drill-in: stage progression timeline (attempts, loop-backs, feedback §6) + **live agent output terminal** (kept from #11) + per-task files (kept from #13, with touched-file count on the tab). The drill-in panel — like the shell dock — is drag-resizable from its top edge. Blocked tasks surface prominently with accumulated feedback. Orphan/unmerged state visible per task.

- **Reads:** `GET /pipeline-runs?session=:id`, `GET /pipeline-runs/:id` (per-task stage, attempts, timings, agent assignments)
- **Subscribes:** `pipeline.*`, `stage.*`, `task.status_changed` (session filter); **data-plane** per-stage agent stream on drill-in
- **Actions:** `POST .../pipeline/start|pause|resume|cancel`, `POST /tasks/:id/retry`, `POST /tasks/:id/reassign {agent}`

#### Context Rail (right)
Constant across display modes (§4). Two tabs: **Tasks** (compact list; synthesis draft cards appear at top with pulse + yellow border → Confirm / Edit / Discard §4) and **Changes** (compact file list; click → Files tab). Status quick-filter chips (All / In progress / Ready / Review / Done) cut the task list to a single status; grouping returns on All. Collapsible.

- **Reads:** `GET /tasks?session=:id&fields=summary`, `GET /sessions/:id/changes?fields=summary`
- **Subscribes:** `task.created`, `note.created` (drafts), `task.status_changed`, `file.changed`, `commit.linked`
- **Actions:** `POST /tasks/:id/confirm|discard`, `POST /notes/:id/confirm|discard`, edit-then-confirm

### 3.4 Retro

tao's flow kept (#14–16 — it works). Fixes: session picker **collapses to a summary chip strip once a retro is generated** (#16); Observations input moves into the main column above Generate (#14); descriptive CTA: **"Create follow-up session (N accepted)"** replaces "Plan from Retro" (#16). DPA cards carry spec fields (problem / evidence / root_cause / remediation_layer / suggested_action §7), default-accepted; the **status chip is the toggle** (click flips Accepted ⇄ Rejected — one element shows state and changes it), plus Edit. The picker has a Clear-selection action and kind filters (All / Work / Retro-generated): selecting retro-generated sessions *is* retro-of-retros (§7) — no separate mode; the retro agent sees the tag and analyzes how DPA tasks fared. The results column centers at a readable measure. Retro history list kept. Cancellable mid-generation (§7).

- **Reads:** `GET /sessions` (picker w/ stats), `GET /retros`, `GET /retros/:id`
- **Subscribes:** `retro.started/progress/completed/failed` (transient job §15)
- **Actions:** `POST /retros {session_ids, observations}`, `POST /retros/:id/cancel`, `PATCH /retros/:id/dpas/:n {status, edits}`, `POST /retros/:id/create-session`

### 3.5 Library

Left sub-nav: **Agents · Capabilities · Workspaces**. Within each: Local list is primary; **"Browse registry"** button opens the remote catalog as a distinct install-flow surface (kills the two-tier pill navigation, #17).

**Agents:** cards show name, description, **quadrant label** (§9), ≤3 tags + `+N` overflow (fixes #17/#20 tag flood; no redundant AGENT chip). Detail/editor: NL-generation box at top ("Describe the agent you need…" → generated definition, draft-styled §9), form fields (name/prompt/capabilities/permissions/model/quadrant), **prompt scaffold guidance** (role/process/output-format template + hints, fixes #18), enrichment actions (§9.6), similar-agent warning (§9.7). Model preference is a **validated dropdown** (inherit workspace default + registry-known models — never free text). Quadrant carries an **explainer tooltip** — it's a mads-specific concept. An **Import from provider** action lists harnesses detected on the machine with agent counts (e.g., "claude-code — 12 agents · ~/.claude/agents") and maps their definitions into the mads format for preview before anything is kept.

**Capabilities:** rows with health dot, name, description, type chip (Skill/MCP), degraded → **"Diagnose"** (guided §12.7, replaces "Fix Me"). Editor per industry Skill format + MCP config; credentials referenced by env-var name (§13); knowledge docs attach here.

**Workspaces:** new surface (§17) — list + editor: repos, default agent/model/trust, env/credential references, pipeline template, isolation default. Shows which sessions used it.

**Pipeline templates:** the fourth shelf (gap caught in mockup review — templates were defined in §6 and referenced by the workspace editor, but had no create/edit surface). The list shows each template's stage sequence, which workspaces use it, and run count; the system default ships editable, like system agents. The editor is **dual-mode**: a visual stage list (add/reorder; each stage card carries the *implemented* §6 grammar — type agent|command, agent binding, per-stage `model:`, `when:` condition, `on_fail` policy with loop-back target, max-loop cap, `then: block`; see §6 "Implemented Surface" for what §6 defers) and a YAML source view (Monaco) of the same artifact, always in sync. **Validate** runs the same checks as pipeline launch — unknown agents, missing loop targets, stage cycles — and reports all errors at once (§10.3). Saving never disturbs in-flight runs; they keep their frozen copy (§6).
- **Reads/Actions (templates):** `GET /templates`, `GET /templates/:id` (+used-by), `PUT /templates/:id`, `POST /templates/validate`

**Registry (remote):** install/update/version-pick flows (§12.9); Publish dialog uses explicit checkboxes (#22). The entire registry surface ships behind the `remote-registry` feature flag (§10) — its nav entry is hidden until enabled.

- **Reads:** `GET /agents|/capabilities|/workspaces`, `GET /registry/...` (remote), `GET /capabilities/:id/health`
- **Subscribes:** `capability.health_changed`, `agent.*`, registry job events
- **Actions:** CRUD on all three; `POST /agents/generate`; `POST /capabilities/:id/check`, `/diagnose`; `POST /registry/install|publish`

### 3.6 Dashboard

New (§8). System-level only, data-not-interpretation. Widgets: active sessions + running pipelines (live), cost over time by model/session/pipeline, agent leaderboard (usage, success rate, retries), capability usage incl. never-used, task outcomes, pipeline run history table → run drill-in links back to the session's Pipeline tab. All charts are queries over the event log (§15).

- **Reads:** `GET /metrics/*` (views over `GET /events`), `GET /pipeline-runs`
- **Subscribes:** `session.*`, `pipeline.*` for the live widget only; charts refresh on view
- **Actions:** none (navigation only)

### 3.7 Settings

Left-nav sections: **Appearance · General · Profiles · Providers · Credentials · Pipeline · Advanced · Updates/About**. Adds over tao: Profiles (§10.5), synthesis mode Off/Light/Full with cost hints (§10), Fast Model, trust defaults, provider capability matrix status (per provider: native/emulated/unsupported behaviors §11), feature flags, credentials manager (moved from Toolbox, UI kept from #24, plus env-var import + vault sources §13). Validation returns all errors at once, inline (§10.3); restart-required badges kept (#25). Updates: version card + release notes in a **two-column layout** (fixes #25's dead space). About page content (#27's comparison table + loop diagram + tabbed walkthrough) lives here as its own entry, updated to Planning → Execution → Retro with Library.

- **Reads:** `GET /config`, `GET /profiles`, `GET /providers` (+capability matrix), `GET /credentials` (masked), `GET /version`
- **Actions:** `PUT /config` (validate-all), profile CRUD/switch, credential CRUD + import + vault link, `POST /system/update`

### 3.8 Command Palette (⌘K)

Global. Sessions (jump), tasks (open sheet), actions (new session, start pipeline, run retro, toggle shell, open settings). The GUI twin of slash commands (§2.5).

- **Reads:** local caches of sessions/tasks/actions (already subscribed)
- **Actions:** dispatches to the surfaces above

### 3.9 First-run

Wizard over empty app (§14.2): provider detection → credential setup → default workspace → lands on Launchpad with a sample suggestion. Preflight check surface reusable from Settings (§14.8).

---

## 4. Event Vocabulary (starter set implied by the surfaces)

`session.created|provisioning|renamed|activity_changed|updated|deleted` · `task.created|updated|status_changed|confirmed|discarded` · `note.created|confirmed|discarded` · `file.changed` · `commit.created|linked` · `pipeline.started|paused|resumed|cancelled|completed` · `stage.started|completed|failed|skipped` · `retro.started|progress|completed|failed` · `capability.created|updated|deleted|health_changed` · `agent.created|updated|deleted` · `workspace.created|updated|deleted` · `link.created|updated|deleted` · `attachment.added` · `merge.attempted|conflicted|completed`

This list feeds §15's event log contract; expect growth during mockups.

---

## 5. Open UX Questions

1. **Session header density** — how much metadata is visible vs. tucked into popovers? *Settled (2026-10-07): no header; the session dock's status line (§3.3) carries persona, model, trust, branch and Commit/Land, each with its own popover.*
2. **Pipeline board orientation** — columns-by-stage vs rows-by-task with stage progress bars. Prototype both cheaply.
3. **Linked sessions UI** (§1.12) — leader/monitor affordances deferred until core workspace lands; the `Link` entity (docs/LINKED-SESSIONS-DESIGN.md) and its Data Service CRUD exist behind the `linked-sessions` flag.
4. **Terminal-mode Context Rail interactions** — rail is identical, but confirm hotkey-only workflows feel right for terminal purists.
5. **Notification surface** — passive model (§8.5); does needs-attention get a dock badge only, or also an in-app inbox?

## 6. Mockup Plan

One surface per iteration, HTML artifacts (Electron's real medium). **Sources are self-contained HTML files in `~/Downloads/mads-ux/`** (e.g., `mockup-01-session-workspace.html`) — the canonical, agent-reviewable copies; downstream agents read these directly. The claude.ai artifact URL is the human viewing copy, republished from the same source. Order:

1. **Session Workspace — Conversation** (the heart: header, chat, Context Rail, composer, shell)
2. **Session Workspace — Tasks** (list + graph + detail sheet + synthesis drafts)
3. **Session Workspace — Pipeline** (board + drill-in)
4. **Launchpad + Session List** (first impressions)
5. **Retro**
6. **Library** (agents editor is the deep one)
7. **Dashboard**
8. **Settings + first-run**
