# mads build spec

> Requirements for a ground-up build of the mads (making agents do stuff) orchestration system.
> Captures what the system must do, not how the current implementation works.
> Any team should be able to implement this in any language, with any storage, using any agent runtime.

> The core goal of mads is to make using agents easy for users who are experienced or novices. It abstracts away finnicky concepts (i.e. how to configure agents or MCP servers) and helps the user get work done, no matter what agent provider(s) they are subscribed to.

> **Lineage**: mads is a ground-up rebuild informed by tao, its predecessor (source at `~/repo/tao`). Where this document references tao by name, it refers to the old system.

---

## 1. Session Management

**Problem**: The user needs a container to scope their work, track what's happening, and resume where they left off — across multiple simultaneous workstreams.

### Requirements

1. **Sessions are goal-scoped** — One session represents one coherent objective (e.g., "build the auth system", "fix the deploy pipeline").

2. **Session creation is implicit** — No "create session" step. The user sends a first message; the system persists a session at that moment, derives a name from the initial message, and assigns a unique ID.

3. **Multiple simultaneous sessions** — The system must support dozens of concurrent sessions, each potentially with its own active agent.

4. **Metadata is mutable** — Agent, model, trust mode, and working directory can be changed mid-session. The system transitions gracefully between values when the provider's runtime supports it. If a provider cannot support a particular transition (e.g., re-rooting the working directory), the system surfaces that constraint clearly rather than silently failing.

5. **Working directory is preserved** — The system records the working directory at session init time (the cwd when the user starts the session). This is important because agentic harnesses bind to a cwd at launch. The initial directory is the default; changing it mid-session is supported if the provider allows.

6. **Session activity is event-driven** — Status changes (active → inactive, disconnected) come from explicit triggers pushed by the process managing the agent. The system never guesses or polls for liveness.

7. **Derived completion** — A session with no open tasks is considered complete. A session that becomes inactive while tasks remain needs attention. The user explicitly deactivates the agent; the system derives the rest.

8. **Resume: provider-native first** — The primary resume path is whatever the provider supports natively (--resume flag, conversation ID, session continuation). This preserves full conversational context. Fallback: reconstruct from task state + handoffs into a fresh session. The fallback is explicitly degraded — it works, but loses conversational nuance.

9. **Session deletion** — Hard delete. Kills any active agent, removes all orchestration artifacts (tasks, file tracking records, execution state, session metadata). Provider transcripts are retained (they live in the provider's storage, not mads's). Deletion is irreversible and does not cascade to linked sessions — links are simply broken.

10. **Observability at a glance** — The session list lets the user instantly distinguish: which sessions have active agents, which are done, and which need attention.

11. **Live agent status is a UI-only concern** — The real-time signal (thinking, tool-calling, waiting) is streamed for display but not persisted as session state.

12. **Linked sessions** — The user can link sessions together, designating one as the "Leader" or "Monitor". Linking enables cross-session orchestration.

    **Creating a link**: Shift+click sessions in the sidebar to select multiple, then click a "Link" button. The user is prompted to choose which session becomes Leader or Monitor. Sessions can only be linked after both already exist.

    **Leader session behavior**: When a session is designated Leader, it immediately receives:
    - Permission to orchestrate linked sessions' agents
    - An abridged summary of what each linked session's agent has done (context injection)
    - A tool for sending messages to any linked session's agent

    From the Leader user's perspective: the Leader agent makes tool calls to communicate with followers — looks like any other tool use.

    From the Follower user's perspective: messages from the Leader appear as user messages in the follower's conversation stream.

    **Monitor session behavior**: A Monitor can read transcripts and task state of linked sessions and answer questions about them, but cannot send messages or orchestrate.

    Constraints:
    - Linked sessions must NOT operate in different worktrees of the same repo (parallel universe problem — conflicting file state).
    - Linking only applies to human-interactive sessions, not pipeline execution runs.
    - Leader→follower orientation only (leader has authority, followers are consultants). Bidirectional peer linking deferred — adds deadlock/coordination complexity without a proven use case.

13. **Workspace spec (optional)** — A session may optionally declare its isolation level: run in the user's current directory (default), run in a new worktree, or run in new worktrees for multiple related repos. Isolation is declared at session init time, either (a) inherited from the workspace config if the user starts a session in a workspace that specifies isolation, or (b) selected by the user at session start in the default workspace. Cannot be changed mid-session. Multi-repo worktree isolation is deferred behind a feature flag (§10, §17); when enabled, the repo set comes from the workspace's declared repos (primary pinned) — never a free-form picker at session start.

### Session State

Session state serves one purpose: let the user glance at a list of sessions and instantly understand what's happening in each one.

**Layer 1: Activity** (binary, event-driven)
- **Active** — an agent process is running in this session right now
- **Inactive** — no agent process is running (user turned it off, or the agent process pushed a "disconnected" event)

**Layer 2: Derived status** (computed at display time from activity + task state)
- Inactive + has open tasks → needs attention (visual flag reminding the user they may want to resume or split tasks into a new session — not a blocking state)
- Inactive + no open tasks → completed
- Active → show live agent status (thinking, tool calling, waiting — ephemeral, not stored)

### Session Identity

- **ID**: system-generated, unique, stable. Used in all internal references and cross-session links.
- **Name**: derived from the user's first message. User can rename at any time.
- **Metadata**: working directory, provider, model, agent, trust mode, creation time, last activity time. All mutable except ID and creation time.

### Multi-Repo Discovery

The user must define a primary repo before the session starts (via workspace config or working directory). Additional repos touched during the session are noted by the system for tracking. This serves two purposes:
- **File tracking attribution** — which commits belong to this session
- **Conflict safety** — warn the user at session start when another session is already operating in the same repo/worktree, so they know a second agent may be active in that space

**Note**: Multi-repo discovery and worktree isolation are independent features that don't compose cleanly. Isolation requires knowing the repo at session init time (to create the worktree); discovery learns additional repos after the agent starts working. If a session uses isolation, the primary repo is declared upfront in the workspace config. Discovery is useful for tracking work that spills into additional repos.

### Open Questions

- ~~Exact lifecycle transitions~~ — Resolved: two-layer model (Active/Inactive + derived status) is the design. Pipeline execution stages directly define task status without needing additional session-level states.
- ~~Whether workspace spec should support full container/sandbox isolation or just worktrees~~ — Deferred. Worktrees are the isolation mechanism for now.

---

## 2. Human-Agent Interfaces

**Problem**: The user needs to communicate with AI agents — giving direction, providing context, reading responses, and intervening when needed.

### Requirements

1. **Structured chat is the primary interface** — Messages in, rich responses out. Agent responses display as a structured stream: text renders as markdown, tool calls appear as discrete inspectable events (collapsible cards), not raw terminal output.

2. **Terminal mode as an alternative display** — Some users prefer the terminal aesthetic. The system supports embedding a raw terminal as a display option. **Global preference** — set in settings, cannot switch mid-session. Both chat and terminal are 100% first-class rendering modes for the same underlying conversation.

3. **Rich compose** — Multi-line editing with markdown support, configurable keybindings (vim/emacs/default), and the ability to attach context alongside the message. Character warning at ~100K characters (unreasonably large input). The user can preview what injected content will be sent with their message before submitting.

4. **Context injection at send-time** — The user can reference external context that resolves into structured content delivered with the prompt. Three distinct mechanisms:
   - **Drag-and-drop zone** — a drop target over the app's GUI that accepts files the user wants to send to the LLM. The drag-and-drop semantics from tao, the predecessor (complex multi-file handling), should be retained. In terminal mode, the terminal handles file input formatting.
   - **@-mentions** — directs the agent to a particular steering file, knowledge document, or session summary. Resolves to the document's content. Works in both display modes (text-based).
   - **/commands** — references skills from mads's capability registry. Injects the skill definition/instructions. Works in both display modes.

   **Display of injected content**: In chat mode, injected content is appended visually below the user's message with clear labels:
   - `══ Attached files: ══ <file/paths>`
   - `══ Capability Injection ══: user strongly suggests <skill> at <file/path> to complete your task`
   
   In terminal mode, formatting is left to the terminal's native rendering.

5. **Slash commands** — `/model`, `/agent`, `/skill` (inject capability) with autocomplete. Also exposed via a discoverable UI button/flyout menu for users who don't memorize commands.

6. **Mid-session model switching** — The user can switch between models the current provider supports. The system handles the transition transparently.

7. **Interrupt** — The user can stop the agent mid-response and regain control promptly.

8. **Reconnection resilience** — If the agent process dies, the system attempts recovery. The user sees a clear disconnection indicator. Conversation history is not lost on reconnect.

9. **Streaming** — Responses stream in real-time (token-by-token for text, event-by-event for tool calls). The user never stares at a blank screen wondering if the agent is working.

10. **Attachments are core** — Users can upload files and images to a session. Permissive on types — anything that isn't an executable. Attachments are:
    - Delivered to the agent as part of the next message
    - Primarily useful at the moment they're sent (once the agent reads them, they're in conversation history)
    - Compressed after 7 days of inactivity
    - Deleted after 30 days
    - Accessible for download while they exist (compressed or not)

11. **Conversation branching (Rewind)** — The user can fork a conversation at any prior turn, creating a new session that starts from that point. The original session is never modified. This is provider-native behavior (kiro's `/rewind` creates a new session ID). mads's responsibilities:
    - Track the parent-child relationship between the original and forked sessions
    - Allow the user to navigate between related sessions (see both paths they tried)
    - Task attribution follows the active session — tasks synthesized before the fork point belong to the original

12. **Non-text agent output** — Agents may produce images, diagrams, file diffs, or other rich content. The system renders these appropriately within the display mode (inline images in chat mode, file paths or ASCII fallback in terminal mode).

13. **Conversation history** — Transcripts are stored by the provider. mads provides:
    - A searchable index across session transcripts
    - The ability to view historical conversations

14. **Bounce (process restart)** — The user can completely restart the process handling the session and resume the agent from where it left off. Use cases: agent gets stuck, user wants to include new tools or change configuration. The session state and conversation history survive the bounce — only the harness process is killed and restarted.

15. **Shell (adjacent terminal)** — A hotkeyed action that opens a terminal window with identical working directory and environment to the agent session. Use cases: agent asks the user to run commands, user wants to do git work themselves. The shell's lifecycle is linked to the agent session — when the session ends, the shell closes.

### Display Normalization

The provider abstraction (Section 11) normalizes provider-specific output formats into a common event stream. This section assumes the interface receives normalized events. How raw provider output becomes structured events is not the UI's concern.

---

## 3. Task Management

**Problem**: The user's goal must be decomposed into discrete, independently-executable units of work that both humans and AI agents can understand, prioritize, and act on. Tasks are often created by agents, executed by agents, and reviewed by agents — the schema must serve all three roles clearly.

### Task Schema

**Core fields** (static, set at creation or early in lifecycle):

| Field | Purpose | Authoring guidance |
|-------|---------|-------------------|
| title | One-line summary. A human scanning a list should understand the task from this alone. | |
| description | The problem and desired outcome. Written as if the reader has never seen this codebase. | |
| acceptance_criteria | Numbered, checkable conditions. Each must be independently verifiable. Used by both executor (to know when to stop) and reviewer (to judge completeness). | |
| scope | What's IN and what's explicitly OUT. Prevents agents from "helpfully" fixing adjacent things. | |

**Handoffs** (accumulated as the task moves through agents):

Each entry represents a structured artifact left by one agent for the next:

| Field | Purpose |
|-------|---------|
| from | Who left this (e.g., "planner", "architect", "executor", "reviewer") |
| content | The artifact — design notes, implementation notes, review feedback, revision notes |
| timestamp | When it was left |

The handoffs array is the living record of how work flowed. Each agent reads what's relevant, does its job, appends its output. This decouples the task schema from any specific pipeline design — phases can be added, removed, or reordered without schema changes.

**Orchestrator fields** (for scheduling and coordination):

| Field | Purpose |
|-------|---------|
| id | Identity |
| status | Lifecycle state (see below) |
| dependencies | Hard ordering — this task cannot start until these are done |
| labels | Routing hints for agent selection (e.g., "bug", "ui", "backend") |
| session | Scoping — which session/project this belongs to |
| retry_count | How many times execution has been attempted |
| created_at | Timestamp for staleness detection and metrics |

### Task Lifecycle

**Default statuses**: ready → in_progress → blocked → needs_review → done / won't_do

- **ready** — all dependencies are met, task is available for execution
- **in_progress** — an agent or human has claimed this task
- **blocked** — cannot proceed (dependency unmet, external blocker, or waiting on user input)
- **needs_review** — execution complete, awaiting review verdict
- **done** — accepted and complete
- **won't_do** — explicitly abandoned (user decided it's not needed, or superseded)

Pipeline templates may define additional intermediate statuses (e.g., multiple distinct review stages). The task system's only concern is: "is this task available for the next stage?" — it doesn't interpret what the stages mean.

### Requirements

1. **Two audiences** — The schema explicitly separates fields for the executing agent (core + handoffs) from fields for the orchestrator (scheduling metadata). Agents never need to understand orchestrator fields.

2. **Acceptance criteria are first-class** — They're the contract between creation and verification. Both executor and reviewer consume them.

3. **Scope boundaries prevent waste** — Explicitly stating what's NOT in scope is as important as stating what is, especially for autonomous agents.

4. **Handoffs are pipeline-independent** — The task doesn't hardcode which phases exist. Any agent can read a task, leave structured data, and pass it on. The handoffs array accumulates over time.

5. **Dependencies are hard ordering** — A task is only "available" when all its dependencies are complete. Priority is not a separate concept; ordering comes from dependencies and FIFO.

6. **Visual dependency management (DAG editor)** — The UI provides an interactive DAG editor for task dependencies. Users can review the execution order and modify dependencies (add, remove, reorder) before launching a pipeline run. This is a critical part of the planning-to-execution handoff — the user verifies the plan makes sense before committing to autonomous execution.
   - Dependencies are session-scoped — no cross-session dependencies, even between linked sessions
   - The system performs a cycle detection check before pipeline launch and blocks execution if cycles exist
   - Users can create cycles (the editor doesn't prevent it) but the pipeline refuses to run until they're resolved

7. **Fully mutable** — All task fields can be modified by the user or an agent at any time. Users can edit synthesized tasks before execution, split tasks, merge tasks, or rewrite any field.

8. **Agent-accessible interface** — Agents must be able to programmatically query, claim, update, and close tasks through a controlled internal API. Access is scoped by caller context:
   - Pipeline agents receive explicit endpoints for their stage (e.g., executor can write handoffs and mark complete; reviewer can write feedback and pass/fail)
   - Planning session agents can read task state but notes/handoffs are managed via synthesis, not direct API writes
   - Implementation: internal MCP server or tool definitions with guards based on the calling agent's role and session context

9. **Persistence and scoping** — Tasks survive restarts and are scoped to a session.

10. **Human review gate** — Automatically-created tasks may be flagged for user approval before execution begins. Low-confidence tasks are surfaceable without blocking the rest.

11. **Clear field semantics for authoring agents** — Since tasks are often written by agents (the synthesizer), each field must have unambiguous guidance on what belongs there. Ambiguity in field purpose leads to agents putting the wrong content in the wrong place.

### Commit Linkage

**Every commit made by an agent must link to the task it resolves or contributes to.** Linkage is attempted through a chain of mechanisms, strongest first:

1. **Commit-message trailer (deterministic)** — agents working a task are instructed (via system prompt or pipeline stage prompt) to end every commit message with a `Task: <task-id>` trailer. The post-commit hook parses the trailer and links directly. Pipeline agents always receive their task ID, so this is the primary mechanism during execution.
2. **Session context (strong)** — the post-commit hook fires with the session's context; if exactly one task is in_progress for that session, the commit links to it.
3. **File-scope heuristics (best-effort)** — if multiple tasks are in_progress (parallel execution), the changed files are matched against each task's scope and tracked file changes to disambiguate.
4. **Orphan flag (fallback)** — if no linkage can be established, the commit is recorded as an orphan and flagged for user attention (§5); the user can link it manually from the UI.

Tasks without commits are normal — not every task produces code (research, decisions, configuration).

### Task Size & Granularity

A good task for autonomous execution is a **cohesive unit of work** that:
- Fits within effective context (<64K tokens of relevant code)
- Has minimal external dependencies
- Includes a verifiable completion signal (testable acceptance criteria)

In practice this typically means 5-50 lines of code changed, 1-3 files touched, with clear testable outcomes. But these are heuristics, not hard rules — the synthesizer or the human uses judgment. The system does not enforce task size; it trusts the author (human or synthesizer) to decompose appropriately.

### Open Questions

- ~~Granularity guidance for synthesis~~ — Resolved: cohesive unit heuristic, not enforced by system.
- ~~Custom status definitions in pipeline templates~~ — Resolved: not needed. The six default statuses (ready, in_progress, blocked, needs_review, done, won't_do) cover all cases. Pipeline stages track progression independently — they don't need additional task statuses.

## 4. Automatic Task Synthesis

**Problem**: Tasks are the core contract for work in mads. But manually creating and maintaining them is tedious. The system should observe the planning conversation and automatically capture substantive findings — taking notes with care and accuracy so the user only needs to review and adjust. Task Synthesis does 90% of the job; the user's touch makes it theirs.

### Mental Model: Lab Notebook

Task Synthesis is an attentive assistant maintaining a lab notebook while the user works. It watches the conversation, records decisions, findings, test results, and dead ends as structured notes on the active task. Occasionally it detects genuinely new work and creates a new task. The user sees what was captured and can accept, edit, or discard.

The **active task** is the task the system is actively working on in the interactive session (in_progress). If multiple tasks are in_progress, the synthesizer targets the most recently touched one.

The goal: write notes so good the user just clicks "yes" without editing. The hit rate on "accepted without changes" is the quality metric.

### The Classification

When the synthesizer detects something substantive, it makes two decisions:

1. **Task or Note?** — Could a different person/agent pick this up and work on it independently, without the full context of the current task? If yes → new Task. If no → it's a Note on the current task.

2. **If Note, what type?**
   - **decision** — we chose X over Y (and why)
   - **finding** — we discovered X is true (root cause, constraint, capability)
   - **test_result** — test passed or failed (with relevant output)
   - **dead_end** — we tried X and it didn't work (prevents repeating mistakes)
   - **observation** — general context worth recording

### Notes Schema

Notes are a separate array on the task, distinct from handoffs (which are pipeline boundary artifacts between agent stages).

| Field | Purpose |
|-------|---------|
| type | decision, finding, test_result, dead_end, observation |
| content | The note itself |
| timestamp | When it was recorded |
| source | Who generated it (synthesizer, user, agent-name) |
| status | draft (pending review) or confirmed |

Notes are append-only. They load on demand (not included in task list queries). Consumers filter by type for progressive disclosure — an executing agent reads `decision` and `finding` notes; it skips `dead_end` and `test_result` unless it hits trouble.

### Task Sources

Tasks can be created by:

1. **Automatic synthesis** — the system observes the conversation and extracts new work items or captures completed work
2. **Human-created** — the user manually creates a task in the UI
3. **Agent-created** — an agent programmatically creates a task during execution (e.g., discovers a sub-problem)

All three produce the same task structure. The difference is authorship and draft status.

### Human Task Creation

The system must treat human-created tasks as a first-class path — not a fallback when synthesis is off. Users who disable synthesis (or who prefer manual control) need a fast, low-friction way to:

1. **`/task` command** — create a task inline from the conversation. Minimum viable: `/task Fix the login redirect bug`. Creates with title only; description, acceptance criteria, and scope can be filled in later or left empty.

2. **New task button in the UI** — a dedicated creation flow in the task panel with a form for all fields. Supports markdown in description and acceptance criteria.

3. **Edit flow for existing tasks** — all fields are always editable. No "locked after creation" semantics. The user can iteratively refine tasks, add acceptance criteria later, adjust scope as understanding deepens.

4. **Bulk import** — import tasks from CSV or other delimited formats. The system maps columns to task fields (title, description, labels, dependencies). Useful for migrating from external trackers or loading an LLM-generated plan.

Human-created tasks skip the draft/review step entirely — they're immediately `confirmed` since the user authored them.

### What the Synthesizer Does

1. **Appends notes to the active task** — decisions, findings, test results, dead ends. This is the primary action and the most frequent.

2. **Updates task metadata** — scope narrows, acceptance criteria refine, description gets more specific as understanding deepens.

3. **Creates new tasks** — when genuinely independent work is discovered. Not sparingly for its own sake, but only when the work passes the "independently actionable" test.

4. **Splits tasks** — when the conversation reveals that a task is actually two distinct problems, the synthesizer scopes down the original and creates the second.

5. **Supersedes tasks** — if a task becomes irrelevant (different approach chosen, problem disappeared), marks it won't_do with a note about why.

### Triggers

Event-driven — structural signals from the conversation that indicate something substantive happened:

**High-value triggers** (always fire synthesis):
- Agent completed a write action (file write, file edit, file create, file delete)
- Agent committed code
- User confirmed a decision ("yes, let's go with approach B")

**Medium-value triggers** (fire based on mode):
- Clear topic/problem shift in the conversation
- Agent produced a significant finding (root cause, error diagnosis)
- Agent completed a multi-step action sequence

**Low-value / never triggers**:
- File reads (not impactful)
- Search/grep operations (information gathering, not action)
- Arbitrary turn count (use debounce instead)

The system maintains a trigger map classifying tool actions as impactful (write-like) vs non-impactful (read-like). This map is configurable but ships with sensible defaults.

**Mode differences**:
- **Full**: fires on all high and medium triggers. Short debounce (30s / 3 turns).
- **Light**: fires on high triggers only. Longer debounce (60s / 5 turns).

Triggers are cheap to detect (structural, not LLM-based). The LLM call happens only after a trigger fires.

### Input Context

When triggered, the synthesizer receives a structured prompt with enforced size limits:

| Section | Content | Budget |
|---------|---------|--------|
| System instructions | Classification rules, note types, task schema | ~300-500 tokens |
| Compressed history | Summary of earlier conversation (not raw turns) | ~500-1000 tokens |
| Recent window | Last N turns at full fidelity — text + tool names (not full tool output) | ~2000-3000 tokens |
| Current tasks | Active task(s) for this session (to detect updates vs new tasks) | ~500-1000 tokens |
| Extraction instructions | What to produce (task/note classification, fields to fill) | ~200-300 tokens |

**Total per synthesis pass**: ~3500-5800 tokens input.

The system constructs this context from raw conversation data, compressing older history and stripping tool output content from recent turns (keeping tool names for context). This keeps synthesis cost predictable regardless of how long the conversation runs.

### Draft Review

**Default behavior**: all synthesized actions (new tasks, new notes, updates) appear as **cards in the right-hand pane** (the same pane that shows tasks and touched files). The pane is constant regardless of display mode (chat or terminal), making synthesis UX display-agnostic.

**Visual treatment**:
- New synthesis cards pulse their border 1-2 times to attract attention
- Unconfirmed cards have a yellow border until the user either edits or confirms
- Cards that have been confirmed show normal styling

**User setting**: power users can change this to auto-approve all synthesis in live sessions. This is a per-user preference, not a system-wide toggle.

Draft actions are reversible. The user can:
- Confirm (accept as-is — yellow border disappears)
- Edit and confirm (modify then accept — now it's human-authored)
- Discard (reject entirely — card removed)

### Synthesis Model

Synthesis uses the **Fast Model** — a globally-configured cheaper model (e.g., Haiku) used for all non-hot-path LLM calls (synthesis, agent selection, retros, session summaries). This keeps synthesis cost low and predictable. The fast model is configurable in settings independently of the session's working model.

### Dual Mandate

Task Synthesis serves two purposes simultaneously:

1. **Historical record** — captures what WAS done (decisions made, findings discovered, approaches tried and rejected). Completed tasks + their notes answer "what happened in this session?"

2. **Downstream agent input** — captures what NEEDS doing. Incomplete tasks carry enough context (notes, scope, acceptance criteria) for autonomous execution.

### Requirements

1. **Non-blocking** — Synthesis runs in the background. It never interrupts the user's conversation or blocks the agent.

2. **Don't duplicate** — If a task already exists covering the same work, don't create another. If a note duplicates an existing note, skip it.

3. **Resilient** — If synthesis fails (LLM error, parse failure), retry with backoff. Never corrupt existing tasks. Circuit breaker on repeated failures.

4. **Low-latency classification** — The Task-or-Note decision + Note-type classification should be fast (small prompt, structured output). The content generation can take longer.

5. **Deduplication against existing state** — The synthesizer always reads current tasks + notes before writing, to avoid redundancy.

6. **Fully optional** — The system must be 100% functional with synthesis disabled. All task management features (creation, editing, dependencies, pipeline execution) work identically whether tasks were synthesized or manually created. Synthesis is a convenience layer, not a load-bearing dependency.

### Open Questions

- ~~Optimal conversation window size~~ — Resolved: structured budget (~3500-5800 tokens per pass) with compressed history + recent window.
- Whether note types should be extensible (user-defined types) or fixed at the current five

## 5. File Tracking

**Problem**: During a session, agents and humans modify files. The system needs to know what changed, who changed it, and which task it relates to — for attribution, review, and historical record.

### What Gets Tracked

Every file modification by an agent, regardless of commit state:
- **Full path** — absolute path to the file
- **Full diff** — the complete change (additions, modifications, deletions) as captured at observation time
- **Committed or uncommitted** — whether the change has been committed yet
- **Task attribution** — which task this change relates to
- **Session attribution** — which session produced this change
- **Timestamp** — when the change was observed

The goal is to track literally any file updated by the agent — not just committed work.

### Granularity

File changes are tracked at **two levels**:
- **Per-task** — granular view for auditing individual work items. "What did the agent change to complete this task?"
- **Per-session** — aggregated view for historical reference. "What files were touched in this session overall?"

Both are derived from the same underlying change records (attributed by task and session).

### Detection Layers

Changes are detected from multiple sources:
1. **Agent tool-use events** — file write/edit/create/delete tool calls in the conversation stream. Captures uncommitted work in real-time. This is the primary source and also triggers synthesis.
2. **Git commits** — post-commit hook fires, captures the committed diff and links to the active task.
3. **Working tree observation** — triggered on synthesis events (not periodic polling). The system runs `git diff` when a write-tool fires, capturing the delta. In non-git environments, FSEvents/inotify surfaces file-changed events.

All sources contribute to the same tracked file set. Deduplication ensures the same change isn't recorded multiple times from different sources.

**User and script changes are tracked too** — if the user edits a file in their editor or a script modifies files, the working tree observation layer catches it (via FSEvents/inotify detecting the change, surfaced on the next synthesis trigger).

**Attribution scope for ambient changes (decided)**: attribution follows isolation mode. In **isolated worktrees** (the default), everything in the worktree is attributed to the session — the worktree exists only for this session, so any change there is session work, regardless of who typed it. In **non-isolated sessions**, only agent tool-use events and session-context commits are attributed; other observed changes (user edits, other agents, scripts outside the session) are recorded as **unattributed observations** — visible in the file tracking pane and available to synthesis, but never counted as this session's work.

### Commit Linkage (mechanism)

The post-commit hook is the bridge between git and mads's task system:
- Installed automatically when a session is initiated in a repo (if not already present)
- On commit, reports to mads: the commit hash, commit message, changed files, diff, and the session context
- mads resolves the task linkage using the mechanism chain in §3 Commit Linkage (trailer → session context → file heuristics → orphan flag)

The hook communicates via HTTP to the service that owns file tracking state. The specific endpoint and protocol are determined by the system architecture (§15).

### Multi-Repo Support

File tracking works across multiple repositories if a session spans them. Each repo has its own hook installation. Changes are attributed with both the repo identifier and the file path.

### Conflict Detection & Isolation

The default for git repos is worktree isolation (§1, §17), which eliminates file conflicts by construction. If the user opts out of isolation and runs multiple sessions in the same working directory, they accept the risk of conflicting edits. The system does not actively monitor for cross-session conflicts in the non-isolated case — that's the user's domain.

### Interface to Synthesis

File changes are a primary trigger for task synthesis (Section 4). When a file is modified or committed, the synthesizer evaluates whether to append a note or create a new task. File tracking explicitly provides this event stream to the synthesis system.

### Requirements

1. **Track all agent file modifications** — committed and uncommitted, additions, modifications, and deletions.

2. **Full diff capture** — store the complete diff at observation time. An engineer reviewing later should see exactly what was done.

3. **Dual attribution** — every change links to both a task and a session.

4. **Multiple detection sources** — tool-use events, git commits, and working tree observation all feed into the same tracked set.

5. **Persist across session lifetime** — file tracking state survives restarts.

6. **Open in external editor** — the user must be able to open any tracked file in their configured editor directly from the UI. The editor is configured in settings as a dropdown populated with popular editors (JetBrains, VS Code, Sublime, etc.), filtered to only those detected on the user's machine. Works identically in both display modes (the file tracking pane is display-mode agnostic).

7. **Inline diff display** — the UI shows file changes using an embedded diff viewer (Monaco-based or equivalent). Users can toggle between inline diff and before/after (side-by-side) views. Diffs are viewable at both the task level (what changed for this task) and session level (all changes across the session). An "open in editor" button is always available for users who prefer their own tools.

8. **Orphan detection** — commits that can't be linked to a task are flagged for user attention and can be manually linked from the UI (§3 Commit Linkage).

9. **Synthesis integration** — file change events are exposed to the synthesis system as triggers.

### Open Questions

- ~~Agent sandbox/environment design~~ — Resolved: isolation is declared at session init, the default for git repos is worktree isolation. Non-isolated sessions accept conflict risk.
- ~~Storage strategy for diffs at scale~~ — Deferred: store raw unified diffs for now, revisit compression/expiry if storage becomes a problem in practice.
- ~~Attribution scope for ambient changes~~ — Resolved: attribution follows isolation mode. Isolated worktrees attribute everything to the session; non-isolated sessions attribute only agent tool-use events and session-context commits, with other observed changes recorded as unattributed observations. See "Attribution scope for ambient changes" above.

## 6. Execution Pipeline

**Problem**: You have a set of tasks ready for work. The system must autonomously produce completed work — selecting appropriate agents, executing the tasks, and verifying quality — with configurable parallelism and resilience to failures.

### Requirements

1. **Tasks in, completed work out** — the pipeline's input is available tasks; its output is commits, closed tasks, and handoff notes recorded in the task's handoffs array.

2. **Templated pipelines** — the pipeline is not hardcoded. It is defined as a YAML template (analogous to CI pipeline configuration but far more user-friendly). The system ships a sensible default. Users can modify it or create new templates. A GUI editor takes this from "usable" to "good" — visual stage ordering, drag-and-drop, inline configuration.

3. **One template per session** — all tasks in a session go through the same pipeline template. If the user wants different pipelines, they use different sessions.

4. **Configurable parallelism** — run multiple agents simultaneously up to a concurrency limit. Only pick up tasks whose dependencies are satisfied.

   **The work queue is live.** Tasks created mid-run in the same session (e.g., by an executing agent that discovers a sub-problem) join the run's queue dynamically once their dependencies are met — the Pipeline Engine learns about them from the event log (§15). Dependency validation, including the cycle check, re-runs at insert time; a task that would create a cycle is rejected back to its creator with an error. Agent-created tasks during autonomous execution are immediately `confirmed` — there is no draft-review step mid-run. Tasks created for a *different* session land in that session's backlog; they never join this run.

5. **Agent selection per task** — the system chooses an appropriate agent for each task based on its labels, description, and capabilities required. If a specific agent fails on a task, retry with a different one.

6. **Fully autonomous** — the pipeline runs without human intervention. The user can interrupt but the system does not ask questions or wait for input during execution. All agent invocations are one-shot, non-interactive (provider's `--no-interactive` equivalent).

7. **Observable** — the user can see what's executing: which tasks are in progress, what pipeline stage each is in, live agent status. The user can intervene (stop, retry, reassign) from the UI.

8. **Graceful shutdown** — when the user stops execution or all tasks are done, clean up agent processes. Don't leave orphans.

### Pipeline Lifecycle

- **Start** — begin picking up ready tasks and advancing them through stages
- **Pause** — stop picking up new work. In-flight tasks run to natural completion of their current stage. No state recovery problem on resume.
- **Resume** — start picking up work again after pause
- **Cancel** — immediately halt all agents, stop all work, kill processes

### Stage Types

Not all stages are LLM calls. A stage has a `type` that determines its behavior:

**`agent`** — invoke an LLM/agent to do work
- Inputs: task fields, prior handoffs, repo state
- Outputs: a handoff entry appended to the task
- Failure: timeout, crash, or explicit failure signal

**`command`** — run a script or shell command (tests, build, lint, deploy)
- Inputs: the working directory, environment variables, task context available as env vars or stdin
- Outputs: exit code + stdout/stderr, captured and attached to the task
- Failure: non-zero exit code

**`gate`** — evaluate a condition to decide whether to proceed, skip, or loop
- Inputs: prior stage outputs, task state, any evaluable expression
- Outputs: pass (advance), fail (block or loop), skip (jump ahead)
- Use case: "only proceed to UX review if the task has label 'ui'"

### Stage Definition (YAML)

> **Aspirational — not the implemented grammar.** The example below predates
> the build and uses seven key spellings that `pkg/template` does not accept
> (`pipeline:` wrapper, `id`, `type`, `run`, `on_failure`, `reads`,
> `produces`). Do not copy it into a real template. The grammar that actually
> validates is in [Implemented Surface (v1 conformance)](#implemented-surface-v1-conformance)
> at the end of this section.

Each stage in a template specifies:

```yaml
pipeline:
  name: "default-implementation"
  version: 1

  defaults:
    timeout: 15m
    model: claude-sonnet
    retry: {max: 2, when: error}

  stages:
    - id: architect
      type: agent
      agent: architect                 # hard lookup in agent registry
      reads: [task.description, task.acceptance_criteria, task.scope]
      produces: [design_notes]
      when: "!contains(task.labels, 'trivial')"

    - id: executor
      type: agent
      agent: executor
      reads: [task, stages.architect.design_notes]
      produces: [implementation_notes]

    - id: build
      type: command
      run: "make build && make test"
      on_failure: {goto: executor, max_loops: 2}

    - id: code_review
      type: agent
      agent: reviewer
      reads: [task.acceptance_criteria, stages.executor.implementation_notes, file_changes]
      produces: [review_verdict]
      on_failure: {goto: executor, max_loops: 2}

    - id: ux_review
      type: agent
      agent: ux_reviewer
      reads: [task, file_changes]
      produces: [ux_verdict]
      when: "contains(task.labels, 'ui')"
      on_failure: {goto: executor, max_loops: 1}
```

**Stage fields:**
- `id` (required) — unique identifier, used in `stages.<id>` references
- `type` (required) — `agent`, `command`, or `gate`
- `agent` (agent stages) — hard lookup in agent registry, or `auto` for auto-selection
- `run` (command stages) — shell command to execute
- `when` (optional) — expression that must be true for this stage to run; if false, stage is skipped
- `reads` (optional) — data contract references this stage receives as context
- `produces` (optional) — named outputs this stage is expected to generate
- `on_failure` (optional) — `{goto: <stage_id>, max_loops: N}` for feedback loops
- `timeout` (optional) — overrides pipeline default
- `retry` (optional) — overrides pipeline default retry policy

### Feedback Loops

When a review stage fails (reviewer rejects the work), it loops back to a prior stage:
- The reviewer's feedback is appended as a handoff (the executor reads it on retry)
- **Circuit breaker**: configurable max loop count per stage. Default: 2 loops. After max loops, the task is marked blocked with accumulated feedback.
- Each loop iteration is a fresh agent invocation with the accumulated handoffs (original task + all prior attempts + all feedback)

### Failure Handling & Retry

- **Agent timeout** — if no progress for a configurable duration, interrupt the agent
- **Agent crash** — retry with the same or a different agent (configurable)
- **Stage failure** — determined by stage type: non-zero exit (command), explicit fail signal (agent), fail verdict (gate)
- **Max retries per stage** — configurable, default 2. After exhaustion, task is blocked.
- **Different agent on retry** — if the first agent failed, the system can select a different one. Configurable per stage.

### Pipeline Stage Observability

The task's "current stage" is tracked by the pipeline engine and exposed for display. The UI shows which stage each task is in using the stage's `name` as the label. The pipeline engine owns progression; the task's `status` field reflects only the six standard statuses.

### Pipeline Data Contract

All data accessible within a pipeline is exposed through a typed namespace hierarchy. These variables are available in `reads` declarations, `when` conditions, and `{{}}` template interpolation. The contract is documented, stable, and versioned.

```
session
├── id                      # session UUID
├── name                    # human-readable session name
├── workspace               # workspace name (if any)
└── repo                    # primary repo path

task
├── id                      # task ID
├── title                   # one-line summary
├── description             # full problem statement
├── acceptance_criteria     # numbered checklist (array)
├── scope                   # in-scope / out-of-scope text
├── labels                  # routing labels (array of strings)
├── status                  # current status (ready, in_progress, etc.)
├── dependencies            # array of task IDs this depends on
├── notes[]                 # notes array (typed: decision, finding, etc.)
│   ├── type
│   ├── content
│   └── timestamp
└── handoffs[]              # pipeline handoff artifacts
    ├── from
    ├── content
    └── timestamp

stages
├── <stage_id>              # access any prior stage's output by ID
│   ├── <output_key>        # named output from that stage's `produces`
│   ├── exit_code           # (command stages only)
│   ├── stdout              # (command stages only)
│   ├── stderr              # (command stages only)
│   └── duration            # how long the stage took
└── current                 # the currently executing stage
    ├── id
    ├── attempt             # which retry attempt (1-based)
    └── feedback            # feedback from prior failure (if looping)

file_changes
├── files[]                 # array of changed files in this task
│   ├── path
│   ├── action              # created | modified | deleted
│   └── diff                # unified diff content
├── count                   # total files changed
└── summary                 # one-line summary of changes

input
├── task                    # alias for `task` (convenience)
└── context                 # any additional context injected at pipeline launch

pipeline
├── name                    # pipeline template name
├── run_id                  # unique ID for this pipeline execution
└── started_at              # timestamp
```

**Rules:**
- Stages can only read outputs from **prior** stages (no forward references)
- A stage's `produces` keys become accessible as `stages.<id>.<key>`
- `task` is always available (it's the work being done)
- `session` is always available (global context)
- `file_changes` reflects the current state of changes made during this task's execution
- `stages.current.feedback` is populated when a loop-back occurs (the failing stage's output becomes feedback)

**Expression examples:**
```yaml
when: "contains(task.labels, 'ui')"
when: "stages.build.exit_code == 0"
when: "file_changes.count > 0"
when: "stages.current.attempt <= 2"
reads: [task.description, task.acceptance_criteria, stages.architect.design_notes]
produces: [design_notes, file_list]
```

### No Synthesis During Pipeline Execution

Synthesis (Section 4) only runs in human-agent interactive sessions. Pipeline agents are one-shot and non-interactive. Their work is captured in structured handoffs per stage — this IS the record of what happened, no synthesis needed.

### Isolation

Each agent works in its own environment. Parallel tasks must not interfere with each other's file state. The isolation mechanism is part of the sandbox/environment design (cross-cutting concern with Sections 1, 5, and 15).

### Implemented Surface (v1 conformance)

This section is normative for what exists today; everything else in §6 is the
roadmap it was scoped down from. The narrowing was deliberate (MADS-UX §3.5,
MADS-WORK-ITEMS W1.5/W3.1/W3.2) but was never recorded here, so §6 read as a
contract it did not keep. Validation lives in `pkg/template` and is shared
verbatim by the editor's Validate, the Data Service's create/update, and the
Pipeline Engine at launch — if it isn't in this grammar, it isn't enforced
anywhere.

```yaml
name: default-implementation          # required
stages:                               # required, >= 1, linear
  - name: executor                    # required, unique
    agent: executor                   # agent XOR command; "auto" selects (§9)
    command: make build && make test  # shell, via sh -c
    model: claude-sonnet-5            # agent stages only; freeform, unvalidated
    when: '!contains(labels, "trivial")'
    on_fail:
      loop_back: architect            # must name an EARLIER stage
      max_loops: 2                    # >= 1 when loop_back is set
      then: block                     # "block" is the only accepted value
```

`when:` accepts exactly four forms: `contains(labels, "x")`, `on_loop`, and
the `!` negation of either. `on_loop` is true only on a `loop_back` re-entry.

| §6 feature | Status |
|---|---|
| `agent` / `command` stages, `agent: auto` | Shipped |
| `when:` guards | Shipped, but only the four forms above — not "any evaluable expression" |
| `on_failure: {goto, max_loops}` feedback loops | Shipped, renamed `on_fail: {loop_back, max_loops}` |
| Circuit breaker → blocked with accumulated feedback | Shipped |
| Worktree isolation, merge/verify, land queue | Shipped (W3.2), plus a post-rebase gate §6 does not describe |
| Live work queue, concurrency limit, lifecycle | Shipped |
| Per-stage `model:` | Shipped — an addition beyond §6, not in it |
| `id:`, `type:`, `run:` | Renamed / dropped: `name:`, and agent-vs-command is inferred from which field is set |
| `gate` stage type | **Deferred.** Gate-like behavior exists only as the hardcoded post-rebase `verify` stage |
| `retry: {max, when}`, per-stage `timeout:` | **Shipped.** `retry: {max (0-10), when: error\|always\|never, backoff}`; `when: error` (the default) retries infrastructure failures but not a considered verdict. Rejected on the merge stage — the land queue owns that retry. Per-stage `timeout:` resolves stage → `defaults:` → global config → 10m |
| `defaults:` block | **Shipped** — `timeout`, `model`, `retry` |
| `reads:` / `produces:` | **Deferred.** Stages exchange context only via the task's handoffs and the previous failure's output |
| Pipeline Data Contract namespace, `{{}}` interpolation | **Deferred.** No `stages.<id>.<key>`, no `file_changes`, no interpolation |
| `version: 1` / version-stamped saves | **Shipped** (closes W1.5's unbuilt promise). `contracts.Template.Version` bumps per save; `pipeline.started` carries `template_version` so a run is attributable to the revision it ran. In-flight runs were already safe via the engine's frozen in-memory copy — this adds the audit trail, not the protection |
| "Retry with a different agent" (Requirements #5) | **Deferred.** Deliberately absent from the grammar rather than accepted-and-ignored: writing `different_agent` is a validation error, not a silent no-op |
| "Agent timeout — interrupt if no progress" | Partial: the stage timeout is total elapsed, not idle-progress |

Unknown keys are **rejected**, with the line number and a did-you-mean
suggestion seeded from the aspirational spellings above (`on_failure` →
`on_fail`, `run` → `command`, `id` → `name`, `goto` → `loop_back`, ...), all
reported together. Templates saved under the older lax grammar are surfaced
by `POST /templates/validate-all` (badged in the Library shelf), by
`mads template validate`, and by a log-only sweep at Data Service startup.
Design notes: `docs/PIPELINE-TEMPLATE-CONFIG-DESIGN.md`.

### Open Questions

- ~~Exact YAML schema for templates~~ — Resolved: see Stage Definition and Pipeline Data Contract above.
- ~~GUI editor design~~ — Resolved: linear stage list with config panels, drag-to-reorder, YAML always viewable as alternative.
- ~~How agent selection interacts with stage `agent`~~ — Resolved: `agent: <name>` is a hard lookup of an agent by name in the registry; `agent: auto` triggers auto-selection.
- ~~Isolation mechanism for parallel execution~~ — Resolved: pipeline engine creates worktrees before launching agents, cleans up after merge.
- ~~How concurrent stages should be expressed~~ — Deferred: v1 pipelines are a linear stage list per task; parallelism happens across tasks, not within a single task's pipeline. A stages+jobs structure (GitLab-style) can be added later if a real need emerges.

## 7. Retrospective Analysis

**Problem**: After a session (or batch of sessions) completes, the user wants to understand what went well, what failed, and what to change — and have the system produce actionable follow-up work.

### Mental Model

A retro is an **informed one-shot analysis**. The user selects sessions, optionally provides feedback ("the executor kept rewriting the same file", "agent selection was terrible for UI tasks"), and a purpose-built retro agent hunts for impactful patterns backed by evidence. The output is a set of Durable Preventative Actions (DPAs) — structured findings that become tasks in a new session.

### What Triggers a Retro

User-initiated only. The system may suggest ("this pipeline had 4 blocked tasks — want to run a retro?") but never auto-generates one.

### Scope Selection

The user selects what the retro covers:
- **Single session** — one planning session and its pipeline run (if any)
- **Multi-session** — multiple sessions selected together (e.g., "retro on this week's work")
- **Retro-of-retros** — select past retro sessions instead of work sessions, looking for patterns in how retro-generated tasks were addressed

### Input Data

The retro agent receives:
- **Tasks** — full task content including notes (decisions, findings, dead_ends, test_results), handoffs, acceptance criteria, status, retry count
- **Files changed** — per-task and per-session diffs
- **Task notes highlighted** — dead_end and test_result notes are surfaced prominently (richest signal for "what went wrong")
- **Transcript paths** — the retro agent is told where transcripts live on disk and can read them if it needs to verify a specific decision or tool-use pattern
- **User-provided feedback** — steers the investigation toward root causes the user cares about

### The Retro Flow

1. User selects sessions, optionally provides observations
2. Retro agent (purpose-built system agent, uses Fast Model) analyzes all input data
3. Retro agent produces a list of DPAs — structured findings with evidence and suggested actions
4. DPAs are presented as **cards in a list**, defaulting to "accepted" state
5. User reviews: keeps accepted ones, rejects or edits others
6. Accepted DPAs are passed to a **new session as tasks**, where the user (with an agent) figures out how to address them

### Output: DPAs (Durable Preventative Actions)

Each DPA is a structured finding:

| Field | Purpose |
|-------|---------|
| problem | What went wrong or could be improved |
| evidence | Specific data backing this finding (task IDs, stage outputs, transcript excerpts) |
| root_cause | Why it happened |
| remediation_layer | What to fix: agent config, tool config, prompt engineering, task decomposition, dependency structure, pipeline template, human process |
| suggested_action | Concrete change to make |
| status | accepted (default) / rejected / edited |

### DPA → Task Flow

Accepted DPAs become tasks in a new session. This means:
- DPAs are traceable (you can see which retro produced which tasks)
- The user addresses DPAs with agent assistance (the new session has normal planning capabilities)
- Retro-generated sessions are tagged/identifiable in the session list (enabling retro-of-retros)
- The retro itself is stored as a persistent artifact (the analysis, the DPAs, the user's accept/reject decisions)

### Requirements

1. **User-initiated** — never auto-generated, but the system may suggest when data indicates issues.

2. **One-shot analysis** — the retro agent runs once, produces DPAs, done. Not an interactive conversation.

3. **Multi-session** — the user can select any combination of sessions for combined analysis.

4. **Structured output** — findings are DPAs with evidence and remediation layers, not unstructured prose.

5. **DPA → task pipeline** — accepted DPAs flow into a new session as actionable tasks.

6. **Persistent and identifiable** — retro artifacts are stored, retro-generated sessions are tagged, enabling trend analysis and retro-of-retros.

7. **Transcript access** — the retro agent can read full transcripts from disk when it needs to verify specifics.

8. **Cancellable** — generation can be cancelled mid-flight.

### Open Questions

- ~~How to present DPAs in the UI~~ — Resolved: cards in a list, default accepted, user rejects/edits.
- ~~Whether DPAs should auto-expire~~ — N/A: they become tasks immediately.
- ~~How deep the transcript access goes~~ — Resolved: agent is told file paths, reads on demand.

## 8. Observability

**Problem**: The user needs to understand what the system is doing, what it did, and where things went wrong — across sessions, agents, and pipeline runs. Observability is the raw data layer; analysis lives in retros (Section 7).

### Requirements

1. **Structured logging** — every component (core daemon, session controllers, pipeline agents) produces structured logs with session/task context. Logs are the debugging backbone when something goes wrong.

2. **Event-based metrics** — the system records key events with timing: session starts/ends, task lifecycle transitions, agent spawns, pipeline stage completions, failures. These are not a separate store: metrics are queries/views over the control-plane event log (§15). Dashboards, cost aggregation, and retro inputs all read the same log.

3. **Real-time visibility** — the UI surfaces what's happening now: which sessions are active, what agents are doing, pipeline progress. One place to see everything.

4. **Historical queryability** — the user can ask "what happened in session X?" and get: tasks created/closed, agents used, time spent, files changed, failures encountered.

5. **Passive notification model** — the system does not alert or interrupt the user. Data is available when the user looks. (Active notifications may be added later.)

6. **Log rotation and retention** — logs don't grow unbounded. Configurable retention policy with automatic rotation.

7. **Export-ready metrics format** — metrics stored in a format that could be exposed to external tools (Prometheus/Grafana) in the future. Not required for launch but the storage model should not preclude it.

### Key Metrics

**Agent usage**:
- Which agents are used most frequently
- Success/failure rate per agent
- Average task completion time per agent
- Which agents get retried or swapped out

**Repo / Workspace usage**:
- Sessions per workspace
- Tasks per workspace over time
- Which repos see the most agent activity

**Model / Provider usage**:
- Which models are used (and how often)
- Token consumption per model
- Cost per model / per session / per task (see Cost Tracking below)

**Productivity signals**:
- Commits per day, per session
- Average tasks per session
- Task completion rate (done vs blocked vs won't_do)
- Pipeline run outcomes (pass rate, average time, blocked count)

**Capability usage**:
- Which skills/MCP servers are invoked most frequently
- Which capabilities are available but never used (candidates for removal)

### Cost Tracking

Every LLM call costs money. The system tracks:
- Token usage (input + output) per call
- Cost per call (based on model pricing)
- Aggregated cost per: session, task, pipeline run, agent, model, time period
- Displayed in the UI so the user understands the economic cost of their workflows

Cost data feeds into retros ("this pipeline run cost $4.20, up from $2.80 last week — the executor is using 3x more tokens").

### Pipeline Run History

Past pipeline runs are stored as queryable artifacts:
- Which tasks were included
- Outcome per task (passed, failed, blocked)
- Total time, per-stage time
- Cost per run
- Agent assignments and retry history
- Feeds directly into retro analysis (Section 7)

### Scope Levels

Observability operates at two levels:

**Session-level** — timeline of one session: what happened, in what order, what the agent did, which tasks moved, what files changed. The "debug view" for a single workstream.

**System-level** — the whole system's health and activity: all active sessions, pipeline status, aggregate metrics, trends over time. The "dashboard view" for understanding overall usage and patterns.

### Open Questions

- ~~Specific chart/visualization designs~~ — Deferred: the dashboard page will be designed during UI mockups. What matters now is the metrics framework that captures this data cleanly from day one.
- ~~Whether to show cost estimates in real-time~~ — Resolved: cost is after-the-fact only (in the metrics dashboard). Context usage is already visible in chat/terminal display modes during live work.
- ~~Retention policy defaults~~ — Resolved: forever with compression. Detailed metrics are compressed after a configurable period but never deleted.

### Design Principles

1. **Framework first, visualizations second** — the priority is a clean metrics capture framework that records events with session/task context from the start. The dashboard UI that renders these metrics can evolve without data loss or refactoring.

2. **Separate dashboard page** — observability lives on its own dedicated page, not embedded in session views. It's a system-wide health/activity view.

3. **Data only, no interpretation** — charts, graphs, and KPIs. No AI-generated insights or anomaly detection. Interpretation is the retro's job (§7).

4. **After-the-fact cost** — cost tracking is displayed in the dashboard after sessions complete. No running cost counter during active work (context window usage percentage serves that role in real-time).

## 9. Agent Selection & Creation

**Problem**: Not every task should be handled by the same agent. The system needs to match tasks to the right agent — and users need to be able to create, configure, and manage their agent roster.

### What Is an Agent?

An agent is a **prompt + capability list + optional constraints**. The minimum viable agent is a name and a prompt. Everything else is optional and inherits from workspace/session defaults when not specified.

```yaml
name: backend-architect
description: "Senior backend engineer specializing in Go systems design"
prompt: |
  You are a senior backend engineer...
  # (or file:// reference to external markdown)

# What the agent can access (optional, inherits workspace defaults)
capabilities:
  tools: [read, write, shell, "@git"]
  mcp_servers: [github, jira]
  skills: [architecture, beads]

# Security boundary (optional, inherits workspace defaults)
permissions:
  allowed_paths: ["src/**", "pkg/**", "cmd/**"]
  denied_commands: ["rm -rf *", "git push --force *"]

# Preferences (optional, inherits session/workspace defaults)
preferences:
  model: claude-sonnet-4
```

**Design decisions:**
- **Prompt is the core** — agent identity lives in the prompt. Everything else constrains or supports it.
- **Capabilities declare what the agent CAN do** — tools, MCP servers, skills.
- **Permissions declare what the agent CANNOT do** — file paths, commands. Security boundary.
- **Model is a preference, not a hard requirement** — the system uses it if available, falls back gracefully.
- **No hooks in the agent definition** — hooks are environment concerns (workspace/session level), not agent identity.

This design aligns with industry patterns: Kiro and Claude Code both converge on prompt + tool access + permission constraints as the core agent definition. Cursor and Gemini use prompt-only with implicit tool access. mads sits in the Kiro/Claude tier (explicit capabilities + permissions) because pipeline execution demands clear security boundaries.

### Agent Selection

**Pipeline stages specify who does the work:**

- **Explicit assignment**: the pipeline template names a specific agent (e.g., `agent: backend-architect`). That agent is used. Predictable, debuggable, the user knows exactly who's doing what.
- **Auto-select**: the template says `agent: auto`. The system selects an agent based on the task's content, labels, and required capabilities.

Explicit assignment is the default. Auto-select is the escape hatch for when the user doesn't care or wants to experiment.

**Auto-selection mechanism (two-stage filter):**

1. **Capability filter** — eliminate agents that lack required tools/skills for the task. If the task needs Jira access and the agent doesn't have the Jira MCP server, it's out. This is fast (set intersection).

2. **Persona filter** — match the user's intent to one of four collaboration quadrants:

| | Exploring (low agency) | Directing (high agency) |
|---|---|---|
| **High clarity** (knows the problem) | Research Analyst | Autonomous Executor |
| **Low clarity** (doesn't know the problem) | Investigator | Technical Partner |

   The system classifies the user's request using signal words (imperative verbs = high agency, question framing = low agency, specific outcomes = high clarity, exploratory language = low clarity). Agents are labeled with their quadrant. Filter to matching quadrant.

3. **Ranking remaining candidates** — when multiple agents pass both filters, candidates are scored deterministically:
   - **Failure exclusion** (hard): agents that already failed this specific task are excluded (this implements diversity-on-retry).
   - **Label affinity** (primary): keyword overlap between the task's labels + description and the agent's description + capability tags.
   - **Track record** (secondary): historical success rate on tasks with similar labels, from the metrics store (§8). Agents with no history score neutral — cold start never disqualifies.
   - **Recency** (tiebreak): more recently used ranks higher; final ties break alphabetically so selection is reproducible.

   Every selection is logged with its scores, so retros (§7) can evaluate whether ranking chooses well. The scorer is a swappable interface — an embedding- or LLM-based ranker can replace lexical affinity later if it proves too crude.

**For pipeline execution**: selection is simpler — tasks are always high-clarity + high-agency (autonomous executor quadrant). The pipeline engine filters by capability and picks the named agent.

**For interactive sessions**: the user picks their agent (no invisible auto-select). The quadrant classification helps surface good suggestions in the agent picker UI ("recommended for your request: ...").

### Agent Generation

When auto-select finds no adequate match (capability filter eliminates all candidates, or no agents exist for the needed quadrant):

1. The system generates a new agent using the Fast Model (prompt + capabilities + permissions derived from the task context)
2. The user is notified that an agent is being generated ("Creating a specialized agent for this task...")
3. Generation typically takes 5-15 seconds
4. Generated agents are **ephemeral** — they exist for the session but are not saved to the registry unless the user explicitly saves them
5. The generation prompt is not user-visible (black box), but the resulting agent definition is fully viewable and editable after creation

### Agent Builder UX

Users create agents through two paths:

1. **Natural language → agent**: describe what you need ("I need an agent that specializes in database migrations and knows PostgreSQL") and the system generates a complete agent definition. The user reviews and saves.

2. **Form-based editing**: direct editing of agent fields (name, prompt, capabilities, permissions, model preference, quadrant label). For users who know exactly what they want.

Both paths produce the same artifact. The builder also offers **LLM-assisted enrichment** — generate a description from a prompt, suggest capabilities from the description, refine prompt wording.

### Requirements

1. **Automatic selection** — given a task and a stage set to "auto", the system selects an appropriate agent. Selection should be fast and reliable.

2. **Diversity on retry** — if an agent fails on a task, the next attempt uses a different agent. The system tracks which agents have failed on which tasks.

3. **User-configurable roster** — users can create, edit, and delete agents. Agents are global (available to all sessions/workspaces).

4. **Default agent for interactive sessions** — defaults to the last agent the user chose. The user always picks; there is no invisible auto-selection for interactive sessions.

5. **LLM-driven agent generation** — the system can generate new agents on demand. Generated agents are ephemeral unless explicitly saved by the user.

6. **Agent authoring assistance** — the system offers LLM-assisted enrichment (generate description, refine prompt, suggest capabilities from partial input).

7. **Avoid unnecessary proliferation** — the system recognizes when a requested agent is substantially similar to an existing one and offers to reuse or adapt.

8. **Retro-driven refinement** — the primary mechanism for improving agents is the retrospective flow (Section 7). The retro identifies what went wrong and suggests agent config changes. The user approves and applies.

### Open Questions

- ~~Exact auto-selection algorithm~~ — Resolved: capability filter → persona quadrant filter → ranking (TBD for large candidate sets)
- ~~Whether agents should have a "suited for" tag~~ — Resolved: agents are labeled with their quadrant (autonomous executor, technical partner, investigator, research analyst) plus capability tags. These serve as fast-filter dimensions.
- Shared/team agent registries — deferred for later
- ~~Ranking heuristic for final candidate selection~~ — Resolved: deterministic scoring (failure exclusion → label affinity → track record → recency), logged for retro evaluation, behind a swappable scorer interface.

## 10. Configuration & Settings

**Problem**: The system has many knobs — cost controls, display preferences, provider defaults, pipeline configuration. These need to be discoverable, editable, validated, and exportable.

### Requirements

1. **All settings editable from the UI** — no requirement to hand-edit config files (though a file-based config exists for power users and automation).

2. **Centralized config file** — all user settings are written to a single config file, generated on first install with default values. Changes update this file. Exporting settings = copying the file.

3. **Validation** — when the user changes settings, validate all fields at once and return all errors together (not fail-on-first). Inline feedback in the UI.

4. **Sensible defaults** — the system works out of the box with zero configuration. Every setting has a default.

5. **Profiles** — named bundles of global settings for different user contexts (e.g., "Work" vs "Personal"). Switching profiles = bulk-editing the global config layer. Not a separate precedence tier — just a convenience for adjusting multiple knobs at once. Example: a "Personal" profile sets token-saving options because the user's personal API plan is more restrictive than their work plan.

6. **Feature flags** — the system supports feature flags for gating experimental or in-progress functionality. Flags are user-visible and togglable from settings.

7. **Change detection** — some settings require a restart to take effect. The UI clearly indicates which changes are live immediately vs. which require restart.

### Settings Categories

**Cost & Token Controls:**
- Synthesis mode: Off / Light / Full — controls whether and how aggressively the system takes notes during conversations
  - **Off**: no synthesis runs. Zero token overhead. Users create and manage tasks manually. The system is fully usable without synthesis.
  - **Light**: synthesis fires only on high-confidence structural signals (tool completions, explicit user confirmations). Low token cost (~8-13% overhead).
  - **Full**: synthesis fires on every substantive turn. Higher accuracy but higher token cost (~30-40% overhead).
- Agent selection: Light (fast heuristic) / Full (LLM-assisted generation/scoring)
- Default model tier: cheapest / balanced / best — which model is chosen when auto-selecting
- Cost alerts: optional warning threshold per session or per day

**Agent Defaults:**
- Default workspace (dropdown of saved workspaces)
- Default provider
- Default trust level (manual / accept-edits / auto)
- Default pipeline template
- Fast model — the model used for synthesis, agent selection, retros, and session summaries (non-hot-path LLM calls). Defaults to the cheapest available model.

**UI Preferences:**
- Display mode: chat / terminal
- Compose keybindings: default / vim / emacs
- Theme
- Editor command (for "open in editor" from file tracking)

**System:**
- Concurrency limit (max parallel agents in pipeline execution)
- Synthesis debounce / trigger frequency
- Log retention (days)
- Data directory path
- Version info + update button (check for new version, view release notes, trigger update)

**Feature Flags:**
- Toggleable per experimental feature (e.g., "enable multi-repo workspace", "enable subagent retry in pipeline")

### Profiles vs Workspaces

These are orthogonal concepts:

| | Profile | Workspace |
|---|---------|-----------|
| **Audience** | Human user | Agent |
| **Contains** | Provider, model, trust, UI preferences, cost controls | Repos, env vars, isolation level, credentials, pipeline template |
| **Scope** | Global (changes how the system feels to use) | Per-project (changes how agents interact with the codebase) |
| **Switching** | Bulk-updates global settings | Changes which project environment is active |
| **Example** | "Personal" = cheaper model, light synthesis, conservative trust | "Backend API" = repo X, full write access, production vault keys |

### Precedence

Settings resolve in one direction:
- **Workspace overrides global** (workspace says "use this pipeline template" → that's what's used)
- **Session overrides workspace** (user changes model mid-session → takes effect for that session)
- **Profiles are just global** (switching profile = editing global, workspace/session still override)

## 11. Provider Abstraction

**Problem**: mads doesn't run LLMs itself — it delegates to external agent harnesses (kiro-cli, Claude Code, Gemini CLI, OpenCode, etc.) and potentially direct LLM APIs. Each harness has different invocation patterns, session storage, transcript formats, and capabilities. The system must abstract over these differences.

### What Is a Provider?

A provider is the **harness**, not the model. Kiro-cli is a provider. Claude Code is a provider. The system interfaces primarily with the harness (starting sessions, sending messages, receiving events, resuming). The model running behind the harness is a configuration detail within the provider.

This distinction matters because:
- The harness determines: how tools are exposed, how sessions resume, what transcript format is used, what trust/permission model applies
- The model determines: reasoning quality, context window, cost per token
- Switching models within a harness is trivial (flag change). Switching harnesses mid-session is not supported (different tool interfaces, session state formats, etc.)

### Requirements

1. **Two categories of LLM interaction**:
   - **Agent sessions** (interactive) — long-running harness processes with tool access to the user's machine. These power planning conversations and execution pipeline stages. Delivered via ACP (structured protocol) or PTY (terminal).
   - **Direct API calls** (one-shot) — stateless LLM calls without a full interactive agent session. Used for task synthesis, session summaries, agent selection scoring. Baseline implementation (known to work): a one-shot, non-interactive harness invocation using the Fast Model (e.g., `claude -p`). Optimization: the user may configure an OpenAI-compatible API endpoint in Settings; when present, mads calls it directly instead of spawning a harness process. Open investigation: whether harness providers (Claude Code, Cursor, Codex, kiro) also expose direct model API access that mads could scaffold onto.

2. **Uniform agent interface** — regardless of provider/harness, the system must be able to: start an agent, send a message, receive streaming responses, interrupt, and resume. The internal API is provider-agnostic.

3. **Two display protocols for agents** — providers expose either a structured protocol (ACP/JSON-RPC for chat mode) or a PTY interface (terminal mode). The system supports both. Terminal display must be backed by a real PTY; the renderer may be an embedded terminal emulator of VS Code quality (e.g., xterm.js), since the UI is a desktop app.

4. **Model discovery** — the system knows which models a provider supports, so the user can switch models within a provider.

5. **Provider switching NOT supported mid-session** — the session is bound to a provider at creation time. Model switching within that provider IS supported.

6. **Transcript access** — the system can read the provider's conversation transcript for synthesis (§4) and resume (§1). The abstraction normalizes across provider-specific transcript formats.

7. **Resume strategy per provider** — each provider has its own mechanism for resuming a prior conversation (--resume flag, conversation ID, etc.). The system encapsulates this so resume "just works" from the user's perspective.

8. **New providers addable without core changes** — adding support for a new agent harness should be a configuration/registration task, not a code change to mads's core.

9. **Rate limit handling** — when a provider returns a rate limit signal (429 or equivalent), the system surfaces this to the user clearly ("provider unavailable, try again in N seconds"). The system does not silently retry in a loop — it informs the user and waits.

10. **Cost tracking hookpoint** — the provider abstraction is where token usage and cost data originates. Each provider must report (or allow the system to compute): tokens in, tokens out, model used. This feeds into observability (§8).

### Launch Providers

- **Claude Code** — primary. The reference provider; every required behavior must work here first.
- **kiro-cli** — secondary. Keeps the abstraction honest; every behavior is implemented or explicitly degraded here.

### Provider Capability Matrix

This spec assumes providers expose a set of behaviors, but real harnesses support scattered subsets. mads maintains an explicit capability matrix: each behavior below is classified per provider as **native**, **emulated** (mads implements it around the harness), or **unsupported** (the feature degrades, and the UI says so).

| Behavior | Required? | Degraded behavior when unsupported |
|---|---|---|
| Start session / send message / stream events | Required | Provider cannot be supported without these |
| Interrupt mid-response | Required | — |
| One-shot non-interactive invocation | Required | Needed for pipeline stages and the Fast Model baseline |
| Resume prior conversation | Expected | Fallback: reconstruct from task state + handoffs (§1) |
| Transcript access | Expected | Synthesis quality degrades; transcript search unavailable |
| Model discovery + mid-session model switch | Expected | Model pinned at session start |
| Token/cost reporting | Expected | Cost dashboard shows "unavailable" for this provider |
| Conversation branching (rewind) | Optional | Branching UI hidden for this provider |
| Mid-session cwd re-root | Optional | Constraint surfaced to user (§1) |
| ACP/structured event protocol | One of ACP/PTY | PTY-only providers render in terminal mode only |

**Code structure**: mads mirrors this matrix as a per-provider strategy registry — each behavior maps to a strategy implementation per provider. Adding a provider, or absorbing a provider's breaking change, means adding or editing strategies — not touching core. The matrix is the contract; the registry is its implementation.

### Notes

- Self-hosted/local models (ollama, vLLM) are a provider configuration question — they'd need an ACP-compatible harness (like OpenCode) to function as an agent. mads doesn't need special support; it just talks to whatever harness wraps the model.
- Direct API calls: the one-shot harness invocation baseline works today; the OpenAI-compatible endpoint path and the harness-provided-API investigation are optimizations layered on top (see Requirement 1).
- Provider credentials (API keys for the underlying model service) live in the Credentials section (§13), not in provider config.

## 12. Agent & Capabilities Registry

**Problem**: Agents need tools (git, jira, playwright, etc.) and knowledge (how to navigate codebases, how to write tests). The system needs to know what's available, which agents need what, and whether those tools are healthy.

### Requirements

1. **Capability registry** — a catalog of available tools/integrations. Each capability has: name, description, how to invoke it (skill or MCP server), and what credentials it needs.

2. **Two capability types**:
   - **Skills** — documents that teach an agent how to do something or do it better. Skills may be structured as nested directories (industry standard) if the skill references a CLI or progressively discloses more documentation or something like that. Examples: "how to use the GitHub CLI," "how to navigate a large codebase efficiently," "how to write Appian SAIL expressions."
   - **MCP servers** — running tool servers that agents connect to for structured tool access (Jira, GitHub, Playwright, etc.). These could be remote or running locally.

3. **Agent-capability binding** — each agent declares which capabilities it needs. The system uses this to: scope credential access, validate tool health before execution, and ensure skills are available in the agent's config.

4. **Skill availability vs injection**:
   - **Availability**: when an agent is created/configured, its declared skills are set up in the provider's agent config file (if the provider supports that). The agent has access from the start.
   - **Injection**: making the agent aware of a skill mid-session without modifying config files. Three paths:
     - **User-directed**: the user tells the agent to use a specific skill (e.g., "use the gws CLI for this")
     - **System-directed**: the system injects a skill based on context (e.g., telling the executor to use the "code review response" skill after failing review in a pipeline)
     - **On-demand via /command**: the user invokes a skill explicitly from the compose UI

5. **MCP server lifecycle**:
   - MCP servers associated with an agent are started automatically when a session begins (if not already running)
   - "Start if not already up" — the system checks if the server is running; if not, starts it
   - MCP servers are system-wide (not per-session) to avoid cross-session interference on cleanup
   - Cleanup: servers stay running until the user explicitly stops them or the system shuts down

6. **Health checks** — the system can verify that a capability is working (MCP server reachable, skill file exists). Surface health status to the user.

7. **Guided diagnostics** — when a capability is unhealthy, offer guided troubleshooting.

8. **Capability builder UI** — users can create and edit capabilities in the UI, analogous to the agent builder.

9. **Remote package registry** — users can download capabilities (and agents) from a remote server maintained by peers/community. This extends the local registry with shared packages. Updates are user-initiated (not automatic). Users can choose a specific version if available.

10. **Reference material lives inside skills** — there is no separate "knowledge document" entity. The industry-standard skill format (a named directory: SKILL.md plus nested reference content) is where a skill's supporting docs belong. Reusable reference material a user wants agents to see is authored as a skill or placed in workspace steering files; @-mentions (§2.4) resolve steering files and session summaries.

### System Agents & Built-in Skills

The system ships with a set of **system agents** that handle internal jobs. These are pre-configured with baseline skills for their roles:

| System Agent | Role | Built-in Skills |
|---|---|---|
| Synthesizer | Task synthesis during planning sessions | Conversation analysis, task schema compliance |
| Agent Builder | Generating/refining agent definitions | Persona design, capability matching, prompt engineering |
| Architect | Pipeline stage: design/localization | Efficient codebase navigation, file localization, design note structure |
| Executor | Pipeline stage: implementation | Minimal-edit discipline, structured output formats |
| Code Reviewer | Pipeline stage: review | Acceptance criteria evaluation, diff analysis |
| Verifier | Pipeline stage: verification | Test execution, integration checking |

System agents are editable (user can customize their skills/prompts) but ship with sensible defaults. Their skills encode process knowledge that makes each role effective (e.g., the Architect skill teaches systematic file localization rather than brute-force reading).

### Notes

- Skills are just documents — no special runtime, no categorization, no execution engine. They're text that goes into the agent's context.
- The current system has four capability types (CLI, MCP, skill, builtin). The rebuild simplifies to Skills + MCP only.
- Shared/team registries are supported via the remote package server. Local registry is the user's own; remote is the community/team source.
- One exception to "MCP servers are system-wide" (#5): the linked-sessions leader capability (`docs/LINKED-SESSIONS-DESIGN.md` decomposition item 5) is a synthetic, session-scoped MCP server — spawned per leader session that actually holds an `orchestrate` grant, never registered in the user-facing capability registry. It reuses the same `pkg/capability.Registry` process-lifecycle machinery, just with a runtime-built `contracts.Capability` instead of a stored one.

## 13. Credentials & Secrets

**Problem**: Capabilities need API tokens, keys, and credentials. The system must store them securely, inject them into the right agents, and never leak them across workspace boundaries.

### Requirements

1. **Secure storage** — credentials stored locally with restricted file permissions. Never committed to git, never transmitted externally.

2. **Scoped injection** — agents only receive credentials for capabilities declared in their manifest. An agent that doesn't declare "jira" never sees the Jira token.

3. **Two injection paths**:
   - MCP capabilities: credentials passed as environment variables to the server process
   - Skills: skills reference credentials by env var name (e.g., `JIRA_READ_TOKEN`), not by value. The system resolves the env var at runtime. Users should NOT embed raw secrets in skill documents.

4. **Managed in Settings** — credentials are part of the settings/profile UI (§10), not a separate panel. Users can add, view (masked), edit, and delete credentials.

5. **Profile + Workspace composition** — credentials compose across layers:
   - Profiles define the user's credential pool (work profile has work tokens, personal has personal tokens)
   - Workspaces reference credentials by name (e.g., "this workspace needs JIRA_READ_TOKEN")
   - At runtime, the workspace's referenced credentials resolve against the active profile's pool
   - **No cross-workspace leakage** — a workspace only exposes credentials it explicitly declares

6. **Session-scoped sharing** — all tasks within a session share the same credential set (determined by workspace + profile). No per-task credential isolation within a session.

7. **Environment variable import** — the system can scan existing environment variables and import them, so users don't need to copy/paste tokens they already have configured in their shell.

8. **Vault integration** — support pulling credentials from external secret managers (1Password CLI, AWS Secrets Manager, etc.) rather than requiring local plaintext storage. Desirable for security-conscious users and teams.

9. **Graceful degradation** — if a credential is missing, the capability is unavailable but the agent still runs. No hard crash.

10. **Rotation is the user's responsibility** — the system does not detect or manage token expiry. If a token is expired, the capability fails and the user updates it.

## 14. Installation & Updates

**Problem**: Users need to install mads, keep it updated, and verify their environment is healthy. This should be frictionless.

### Requirements

1. **One-command install** — new users get running with a single curl-to-shell script. Homebrew is a stretch goal (requires registering a tap or getting into homebrew-core). The installer downloads the correct platform binary and places it on PATH.

2. **First-run setup** — on first launch, walk the user through essential configuration: provider setup, credentials, and any platform-specific prerequisites. Creates the data directory with default config.

3. **User-initiated updates** — the system checks for new versions and surfaces the latest version + release notes in Settings. The user decides when to upgrade. No auto-update.

4. **Safe update lifecycle** — updates stop running agents gracefully, apply the update, and restart. Downloads are checksum-verified. On failure, automatic rollback to previous version. Active work is not lost (sessions resume after update).

5. **Backwards-compatible upgrades** — upgrades are structured to be backwards-compatible. Session data, config, agents, workspaces all migrate automatically. No manual migration steps.

6. **No side-by-side versions** — only one installation of mads at a time. Upgrades replace the previous version.

7. **Uninstall** — `mads uninstall` removes the binary and, after explicit confirmation (or preserved with `--keep-data`), deletes the user data directory. Clean removal, nothing left behind.

8. **Preflight/health check** — on-demand verification that all binaries, dependencies, and services are present and working. Surfaces what's broken and how to fix it.

9. **Provider compatibility matrix** — the system maintains a mapping of mads versions to compatible provider/harness versions. If a provider introduces a backwards-incompatible change, the system warns the user before they attempt to use that provider version.

10. **Cross-platform** — macOS and Linux are required at launch; Windows is a goal. The desktop UI framework (Electron) is Windows-capable, so the service layer should avoid Windows blockers from the start (ConPTY for PTY sessions, ReadDirectoryChangesW for file watching, path handling) even if Windows ships after launch. Distributed as separate platform-specific binaries.

11. **Single release channel** — one consumer build. No beta/stable split.

### Notes

- Dependencies for the rebuild are TBD (code hasn't been written yet). The installer will detect and guide installation of whatever runtime dependencies emerge.
- Homebrew registration: requires either a homebrew tap (self-hosted, easy) or acceptance into homebrew-core (requires popularity threshold). Start with tap, graduate later.

## 15. System Architecture & Communication

**Problem**: The system must be modular enough that individual services can be developed, tested, and maintained independently — including by agentic coding tools that work best with bounded, well-defined contexts.

### Design Principles

1. **Independently runnable and testable** — each service can start in isolation, respond to requests, and be tested without the full system running.
2. **Loose coupling** — services communicate via defined protocols, not shared memory or internal function calls.
3. **Single responsibility** — each service owns one concern and owns the data it mutates.
4. **Agent-maintainable** — service boundaries are small enough that an AI agent can hold the full context of one service in a single session.

### Service Topology

```
┌─────────────────────────────────────────────────────────┐
│                         UI                               │
│  (thin client — renders state, sends user actions)       │
└────────────────────────┬────────────────────────────────┘
                         │ HTTP + SSE
┌────────────────────────▼────────────────────────────────┐
│                    Router Service                         │
│  Owns: session lifecycle, pipeline lifecycle,            │
│        linked session coordination, agent selection       │
│  Does: routes UI requests to sessions/pipelines,         │
│        manages start/stop/link, serves real-time streams │
└──┬──────────────┬──────────────┬────────────────────────┘
   │              │              │
   │ spawn/kill   │ spawn/kill   │ HTTP (internal)
   │              │              │
   ▼              ▼              ▼
┌──────┐   ┌──────────┐   ┌──────────────┐
│Session│   │ Pipeline │   │ Data Service  │
│  (×n) │   │  Engine  │   │              │
│       │   │   (×n)   │   │ Owns: tasks, │
│ Owns: │   │          │   │  sessions,   │
│ agent │   │ Owns:    │   │  files,      │
│ proc, │   │ stage    │   │  metrics,    │
│ synth,│   │ progress,│   │  agents,     │
│ file  │   │ worktree │   │  registry,   │
│ obs   │   │ mgmt     │   │  workspaces  │
└───┬───┘   └────┬─────┘   └──────▲───────┘
    │             │                │
    └─────────────┴────────────────┘
          HTTP (internal) — task reads/writes via lease
```

### Service Responsibilities

**Router Service** (always running, user-facing)
- Routes UI requests to the appropriate session or pipeline
- Manages session lifecycle (create, terminate, link, bounce)
- Manages pipeline lifecycle (start, pause, resume, cancel)
- Brokers real-time streams (SSE) from sessions/pipelines to UI
- Handles agent selection (calls Data Service for registry, applies quadrant filter)
- Single entry point for the UI — no direct UI→session communication

**Data Service** (always running, internal-facing)
- Owns ALL persistent state: tasks, session metadata, file tracking, metrics, agent registry, capability registry, workspace configs
- Provides a CRUD + query API over this data
- Implements write leases: when a session claims a task, only that session can mutate it until release
- Handles schema migrations and data integrity
- Independently testable: start it up, hit it with curl, verify behavior

**Session Service** (one instance per active session)
- Owns the running agent process (spawning, communicating via ACP/PTY, killing)
- Runs synthesis (triggers, LLM calls, writes results to Data Service via lease)
- Observes file changes (FSEvents/inotify, tool-use events)
- Streams agent output to Router (which forwards to UI)
- Reports metrics events to Data Service
- Independently testable: mock the Data Service, feed it fake agent events

**Pipeline Engine** (one instance per pipeline run)
- Owns stage progression (which stage is active, what's next, loop-back logic)
- Manages worktrees (creation before agent launch, cleanup after merge)
- Spawns agent processes per stage (same mechanism as Session, but one-shot)
- Writes stage outputs and task updates to Data Service via lease
- Reports metrics events to Data Service
- Independently testable: mock the Data Service, feed it a pipeline template + tasks

### Two Communication Planes

Service communication separates two kinds of information flow:

**Control plane — durable lifecycle events.** Facts about state: `task.created`, `task.status_changed`, `stage.started`, `pipeline.paused`, `session.deactivated`, `commit.linked`. Low rate (a few events per second, worst case). Must never be missed. Delivered via the event log (below).

**Data plane — ephemeral streams.** Live agent output: tokens, tool-call events, thinking status. High rate, transient by design (§1: live agent status is never persisted). Flows Session/Pipeline → Router → UI over SSE and is never stored. A viewer that disconnects misses data-plane traffic and loses nothing — current state is always recoverable from the control plane.

**Hard rule: no peer-to-peer service calls.** Services never call each other directly. All coordination happens through Data Service state + events, or through the Router for lifecycle actions (spawn, kill, interrupt). Every service therefore has exactly two counterparties to mock in tests.

### Event Log (Control Plane)

The Data Service maintains an append-only `events` table. Every state mutation and its event append happen in **one SQLite transaction** (the transactional outbox pattern) — state and its announcement cannot disagree. There is no "task was created but the pipeline never heard about it."

**Envelope** (uniform across all event types):

| Field | Purpose |
|---|---|
| seq | Monotonic sequence number — the global cursor |
| timestamp | When it happened |
| type | Namespaced event type (`task.created`, `stage.completed`, ...) |
| session_id | Session scope (nullable for system-level events) |
| task_id | Task scope (nullable) |
| actor | Who caused it (user, agent name, service) |
| payload | Type-specific JSON |

**Subscriptions are filtered server-side.** Subscribers never scan the whole log. A subscriber opens `GET /events?since=<seq>&session_id=<id>&types=task.*` (SSE); the Data Service serves matching historical events from an indexed query (index on `seq` and `(session_id, seq)`), then pushes matching live events as they commit. Each subscriber holds its own cursor (`seq`), so reconnects resume exactly where they left off — no missed events, and no buffering logic in subscribers.

**Consumers:**
- The Router subscribes on behalf of the UI (filtered per open view) and forwards over SSE
- A Pipeline Engine subscribes to its session's task events — this is how it notices mid-run task creation (§6 Dynamic Work Queue)
- Session services subscribe where useful (e.g., synthesis reacting to file-change events)
- Retro jobs and the metrics dashboard **query** the log historically rather than subscribing

**The event log IS the metrics store** (§8). There is no separate metrics-events path — dashboards, cost aggregation, and retro inputs are queries/views over the same log. Retention follows §8: forever, with compression of old events.

**Replay**: the log retains enough to reconstruct a pipeline run's timeline after the fact. A replay UI is not a launch feature, but the data model supports it by construction. (Interactive-session conversation replay is a different mechanism — that is the provider's transcript/resume, §11.)

### Write Lease Model

Tasks are the shared data problem — both sessions and pipeline stages mutate them. The lease model prevents contention:

A lease means "this process is working on this task right now". It is not the task's progress, and losing one never changes the task's status (docs/PIPELINE-LEASES-DESIGN.md).

- When a session or pipeline stage claims a task (status → in_progress), it acquires a write lease from the Data Service
- **Holders are per process.** A pipeline run holds its leases as `pipeline:<runID>`, so two runs never share a holder; the claim's holder must be the caller's actor or start with `<actor>:`. Attribution is unchanged: events and notes still read `pipeline`
- **Tokens prove possession.** A claim returns a random token, kept only in the claiming process's memory (the store keeps its hash). Renewing, releasing and writing a leased task need the token; a write without it is refused, which is why a task's details are read-only while a run works on it. Renewal never creates a lease, so a holder that lost its lease can't silently take it back
- The lease is released when the task transitions to a terminal or handoff state (done, blocked, needs_review)
- Leases carry a **TTL** (e.g., 60s) that renews on every write, plus a cheap renewal call when the holder is idle. If a holder dies, its leases expire and the Data Service deletes them
- **Primary liveness is supervision, not polling**: the Router spawns sessions and pipelines as child processes and receives process-exit from the OS. On exit it releases the dead process's leases immediately (a pipeline run's by its `pipeline:<runID>` holder) — the TTL expiry is only the backstop for the case where the Router itself missed the exit. §1's "no liveness polling" holds: nothing ever asks "are you alive?"; a lease simply expires like a parking meter
- **Reap-on-launch**: a starting pipeline lists every pipeline lease (`GET /leases?kind=pipeline`) and deletes those whose holder process it can prove dead (machine, boot id, pid and start time), so a crash or reboot doesn't wait out the TTL
- **Lease loss changes no status.** The TTL reaper, the Router's exit cleanup and reap-on-launch only delete leases. An `in_progress` task the pipeline started that has no live lease is **Interrupted**: work started and nobody is doing it now. Interrupted is derived (never stored) and shows only in the Tasks tab's run views
- **Any new run picks Interrupted tasks up** where they stopped, alongside Ready ones: it keeps the task's worktree and branch (committing stray work first), skips the stages already completed, and resumes the agent session of an interrupted agent stage where the provider can (command stages rerun). Under a different or since-edited template it restarts from the first stage and keeps the work. Stop (formerly Cancel) ends a run this way on purpose: it keeps all state and returns the leases

### Communication Protocols

**UI ↔ Router**: HTTP REST + SSE over localhost. The UI makes REST calls for actions and subscribes to SSE for real-time updates. Port is configurable (default 17373).

**Router ↔ Sessions/Pipeline**: HTTP over localhost (each session/pipeline gets an ephemeral port or unix socket). Router discovers these at spawn time.

**Sessions/Pipeline ↔ Data Service**: HTTP REST over localhost (fixed port). CRUD + event operations: `GET /tasks/:id`, `POST /tasks/:id/claim`, `PUT /tasks/:id`, `POST /events` (append), `GET /events?since=...` (filtered subscription, SSE).

**Session ↔ Agent Process**: stdio (ACP protocol) or PTY, depending on provider display mode. The session service owns this communication exclusively.

### Storage

SQLite in WAL mode, owned exclusively by the Data Service. No other service touches the database files directly — all access goes through the Data Service's API.

- Single database file (simpler backup, migration, and atomicity) with logical table separation
- Schema versioned with migration tracking
- If SQLite becomes a contention bottleneck at scale, the Data Service's API doesn't change — only its backing store would be swapped (e.g., to PostgreSQL)

### Transient Jobs

Some operations are triggered by user action, run a single LLM call (or short burst of work), read/write to the Data Service, and exit. These are not long-running services — they're **transient jobs** spawned by the Router:

| Job | Trigger | Reads | Writes |
|-----|---------|-------|--------|
| Session Summary | Session active → inactive | Tasks, notes, handoffs for session | Session metadata (summary field) |
| Retro | User initiates retro | Tasks, file changes, session metadata for selected sessions | New session + DPA tasks |
| Agent Generation | Auto-select finds no match | Task content, agent registry | Ephemeral agent definition (or registry if saved) |
| Agent Selection | Session start or pipeline stage | Task content, agent registry | Selected agent assignment |

These jobs use the Fast Model and communicate exclusively via the Data Service API. The Router is responsible for spawning them and reporting their completion status to the UI.

**Note**: The exact boundaries here will be revisited with mockups in hand — user flows will clarify what communication patterns are actually needed between services.

### Failure Isolation

- Agent crash → Session detects, reports to Router, UI shows disconnected state. Other sessions unaffected.
- Session crash → Router detects (process exit), releases any task leases via Data Service, UI shows "needs attention." Other sessions unaffected.
- Pipeline crash → Router detects, releases the run's task leases and ends the run as interrupted. Its in-flight tasks stay in_progress (Interrupted) with their work kept, and the next run picks them up. A run lost to a reboot is marked interrupted when the Router starts. Planning sessions unaffected.
- Data Service crash → everything pauses (no reads or writes). Router holds requests until Data Service restarts. Each service keeps a small in-memory outbox and retries event appends in order, with backoff, until the log accepts them.
- Router crash → UI loses connection. Sessions and pipelines continue running (they're independent processes). UI reconnects when Router restarts.

### Notes

- This is NOT microservices deployed separately. Everything ships together, installs together, updates together. The separation is for testability, maintainability, and agent-friendly development — not independent deployment.
- Each service is its own Go package (or binary) with a clean entry point and a well-defined API contract. **Language (final): Go** for all services — single static binaries, trivial cross-compilation, stdlib HTTP/SSE, mature process/PTY handling, and a fast, agent-friendly test loop. The UI is a cross-platform Electron desktop app (TypeScript); the service/UI boundary is HTTP, so the two-language split follows the architecture.
- The Qwen Code `qwen serve` pattern is a reference: daemon + child processes + HTTP for coordination.

### Quality & Testing Strategy

mads is production software distributed to other engineers. Quality is enforced by construction: every service is independently testable, and tests are how agents prove their work during the build (the pipeline's own verify stage runs them).

**The keystone: a fake provider.** `provider-fake` implements the full provider abstraction with scripted, deterministic agent behavior — respond, edit files, create tasks, emit events, crash on cue. Sessions, pipelines, synthesis, and UI flows all test against it: no LLM calls, no flakiness, CI speed. It is a first-class deliverable built alongside the provider layer, not an afterthought.

**Per-service methodology** (every service ships `make test`, green before merge):

- **Data Service** — migration up/down tests; transactional-outbox atomicity (state mutation and event append commit or roll back together); event-log cursor-resume property tests (no gaps, no duplicates across reconnects); lease TTL expiry and renewal-on-write; concurrent-writer contention under `-race`.
- **Router** — supervision (child process crash → immediate lease release); SSE fan-out and server-side filtered-subscription correctness; reconnect-with-cursor; provisioning-sequence event ordering.
- **Session service** — ACP and PTY adapters exercised against provider-fake; resume, interrupt, restart-agent, and model-switch flows; transcript normalization pinned by golden files.
- **Pipeline Engine** — stage-machine tests (conditions, on_fail loop-backs, max-loop → blocked); dynamic work-queue insertion with cycle re-check; worktree lifecycle against real git in temp directories; concurrency-limit enforcement.
- **Provider adapters** — the §11 capability matrix is an **executable conformance suite**: one shared test suite runs against every provider strategy and asserts that native/emulated/unsupported behavior matches the declared matrix. Real-harness smoke tests exist with recorded fixtures but are opt-in, never CI-default.
- **Synthesis & transient jobs** — prompt-in/structured-out golden tests against provider-fake; draft → confirm/discard state transitions.
- **UI (Electron)** — component tests for the token system and core components; Playwright end-to-end against the real service topology plus provider-fake (launch → session → tasks → pipeline → retro happy path); the E2E launch doubles as the per-platform packaging smoke test.

**Cross-cutting gates**

- **Contract tests at every boundary.** Each service has exactly two counterparties (no peer-to-peer, above), so its API contract is pinned by goldens both sides consume; a contract change fails both suites until updated together.
- **One end-to-end smoke** spawns the full topology and runs a scripted session through pipeline and retro on provider-fake — on every merge, and it must stay fast (minutes, not tens of minutes).
- **Go hygiene:** `-race` always on in CI; golangci-lint clean; coverage tracked per package with each service's critical paths enumerated in its README — no vanity global coverage number.
- **Flaky tests are defects:** quarantine immediately, fix or delete within the week. A flaky suite trains agents and humans to ignore red.
- **Release gate:** cross-platform build matrix; install → first-run → update → uninstall smoke on macOS and Linux (Windows when it lands, §14).
- **The agent-maintainability contract:** every service README documents how to run it alone, how to test it, its API contract, and its critical paths.

### Open Questions

- ~~Inter-service communication patterns~~ — Resolved: two-plane model (control-plane event log via transactional outbox in the Data Service; ephemeral data-plane streams through the Router), supervision + TTL leases for liveness, and a hard no-peer-to-peer rule. See Two Communication Planes and Event Log above.
- **UX design pass pending** — a UI/UX design session is the next step; it will surface data-handling needs (views, filters, drill-downs) that refine the event vocabulary and subscription filters. The two-plane architecture is stable; the set of event types is expected to grow.

## 16. Session Summary & Recovery Context

**Problem**: When a session is resumed or referenced, the system needs a concise representation of what happened — not a task list, not AI slop, but a compressed narrative that conveys the arc of the work.

### What Makes a Good Summary

A session with 20 tasks might boil down to: "Wrote a platform spec across multiple passes of increasing granularity, and ran controlled experiments on agent prompt effectiveness using SWE-bench." The summary should compress, not enumerate. It captures what was *accomplished*, not what was *done*.

### Requirements

1. **Compressed narrative** — the summary conveys the 2-3 major threads of work in the session, not a rehash of the task list. A reader should understand the session's purpose and outcome in 10 seconds.

2. **Grounded in tasks** — the summary is derived from task data (titles, statuses, notes, handoffs), not from re-reading the full transcript. This makes regeneration cheap.

3. **Structured sections**:
   - **What was accomplished** — the primary thread(s) of work, described at the level of intent not mechanics
   - **Key decisions made** — drawn from `decision` type notes on tasks
   - **What remains** — open tasks or known next steps, if any

4. **Generated on session deactivation** — triggered when a session transitions from active → inactive. Not regenerated on every task change (too expensive), not on-demand-only (too stale).

5. **System-generated, not editable** — produced by a single Fast Model LLM call. The user cannot edit it directly (it regenerates on next deactivation anyway). If the user wants to annotate, they add notes to tasks.

6. **Top-level metadata field** — stored as a field on the session record, not as a note or sub-document. Accessible via `session.summary` in the data contract.

7. **Used for**:
   - Resume context — when resuming, the summary orients the agent
   - Cross-session reference — when one session @-mentions another, the summary is injected
   - Session list preview — shown in the UI session list for quick scanning
   - Linked sessions — the leader session receives summaries of linked followers

### Open Design Questions

- ~~Exact format/structure~~ — Resolved: compressed narrative with three sections (accomplished, decisions, remaining)
- ~~Trigger mechanism~~ — Resolved: on active → inactive transition
- ~~Whether the user should be able to edit~~ — Resolved: no, system-generated only

## 17. Environment & Workspace

**Problem**: Every session needs somewhere to work. The environment varies wildly — from a massive monorepo with parallel agents to a non-git directory where the user just wants help writing a document. The system must provide isolation when needed, tracking when possible, and configuration that persists across sessions.

### Workspace (Persistent Configuration)

A **workspace** is a saved configuration bundle that applies to sessions started within it. It is a persistent, reusable artifact — the user selects a workspace before starting a session.

A workspace may include:
- Default agent, model, trust mode
- Allowed capabilities / scoped API keys (e.g., read-only API access in workspace A)
- Associated repos
- Environment variables / secrets available to agents
- Pipeline template to use
- Isolation defaults
- Any other session-level defaults

**Why workspaces exist**:
- Users don't reconfigure settings every time they start a session in the same project
- Security boundaries become explicit ("agents in this workspace can't write to production")
- Natural grouping for "all sessions related to Project X"
- A single user might have different intents in the same repo (one workspace for "development" with full write, another for "audit" with read-only)
- Workspaces can span multiple repos (which file-based config discovery cannot)

**Precedence**: workspace overrides global config; session overrides workspace. The UI makes this visible ("this session uses workspace X, which sets model=claude-sonnet; you've overridden model to claude-opus for this session").

### Environment Rules

The system detects the environment and applies defaults:

**Git repo detected** → isolation by default (session gets its own worktree). User can opt out.

**No git repo** → no workspace management. File tracking is limited to:
- Agent tool-use events (file write/edit/create/delete)
- Directory watching via FSEvents/inotify as a best-effort supplemental layer
- No commit linkage, no worktree isolation

### File System Watching

For non-git environments (and as a supplemental layer in git environments), the system watches the working directory for changes:

- **macOS**: FSEvents provides recursive directory watching with a single watch point. Zero performance concern regardless of repo size.
- **Linux**: inotify requires per-directory watches. For typical repos (< 5K directories), sub-second setup. For massive repos (60K+ directories), use lazy watch expansion or fall back to periodic `git status` polling.
- **Windows** (when supported): ReadDirectoryChangesW provides recursive watching of a directory tree.
- **Scope**: watches are per-directory-tree. Worktrees are physically separate paths — no cross-talk between watches on different worktrees.

### Isolation During Interactive Sessions

When a user starts an interactive session in a git repo:
- A new worktree is created (default behavior, opt-out available)
- The agent works in this worktree — safe to experiment without affecting the user's main checkout
- File tracking watches this worktree specifically
- Merge-back is **user-triggered**: a "Merge back" action in the session UI. The user picks the target branch (defaults to the session's base branch); the system attempts the merge automatically and, if it isn't clean, presents resolution options in the UI (embedded merge editor or open in external editor)
- Ending a session does not force a merge — an unmerged worktree is flagged in the session list so the user knows merge/cleanup is available (see worktree cleanup in Open Questions)

### Isolation During Pipeline Execution

When the pipeline runs tasks in parallel:
- Each task gets its own worktree (mandatory for parallel execution — agents cannot share file state)
- Each worktree has its own file watcher (scoped, no cross-talk)
- The post-commit hook in each worktree reports to mads with the task context

### Merge-Back

When isolated work is ready to return to trunk:

**In the default pipeline** (architect → executor → review → merge → verify):
- Merge happens between review and verify
- The merge is a `command` stage: merge the task branch into the session's base branch
- The verifier then tests against trunk (catches integration issues)
- Merge order follows dependency order (topological sort)

**Conflict handling**:
- **Loud conflicts** (same file, same lines): detected at merge time, task blocks, user resolves
- **Quiet conflicts** (semantic incompatibilities): caught by the verify stage (build + test on merged state)
- Both result in: task blocked with clear explanation, user attention needed
- Merge conflicts signal hidden coupling in the task decomposition — they're feedback, not system failure

**Mitigation**:
- Dependency ordering serializes related tasks (prevents most conflicts by construction)
- Truly independent tasks (running in parallel) shouldn't touch the same code — if they do, the dependency graph was wrong
- Sequential merge + build between each merge identifies which task introduced a break

### Configuration Scope

Environment/workspace settings can be configured at multiple levels:
- **Global** — user-wide defaults
- **Workspace** — project-level overrides (persistent, reusable)
- **Session** — per-session overrides
- **Pipeline template** — isolation and merge behavior for execution

### Open Questions

- ~~Worktree cleanup policy~~ — Resolved: user-triggered. Inactive sessions with worktrees should flag this in the session UX so the user knows cleanup is available.
- ~~Whether workspaces should support container/sandbox isolation beyond worktrees~~ — Deferred.
- ~~Multi-repo worktree coordination~~ — Resolved: deferred. Workspaces can declare multiple repos for tracking purposes, but worktree isolation only applies to the primary repo. Multi-repo isolation adds significant machinery for an edge case; users who need it manage it themselves.
- User experience for merge conflict resolution — partially resolved: attempt merge automatically, present in-UI resolution if it fails (Monaco merge editor or similar). Exact UI design deferred to mockups.

## 18. About Page: How the System Works

**Purpose**: A plain-language explanation for new users that orients them to the system's mental model before they start using it. This is the "landing page" content — not a reference, but a narrative.

### Skeleton (content deferred — to be written once the UI takes shape; not a build work item)

1. **What is mads?** — One-paragraph elevator pitch.

2. **Core concepts**:
   - Sessions — goal-scoped containers for your work
   - Tasks — cohesive units of work (a task fits in context, has minimal dependencies, includes a verifiable completion signal)
   - Agents — AI personalities configured for specific jobs
   - Pipelines — automated workflows that take tasks from ready to done

3. **Two modes of working**:
   - Planning (interactive, you + an agent)
   - Execution (autonomous, agents work in parallel)

4. **What happens when you start a session** — implicit creation, agent selection, workspace binding

5. **What happens during execution** — pipeline stages, isolation, review loops, merge-back

6. **Key design principles**:
   - You're always in control (pause, cancel, override)
   - Synthesis is optional (you can manage tasks manually)
   - Model quality matters more than prompt tricks
   - Everything is observable (no black boxes)
