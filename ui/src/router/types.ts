// Hand-written mirrors of pkg/contracts wire types actually consumed by the
// UI. Field names stay snake_case to match the JSON the Go services emit —
// no camelCase translation layer for this first pass.

export type SessionActivity = "active" | "inactive";
export type SessionTrust = "full" | "limited" | "read_only";
export type SessionNameSource = "derived" | "user" | "generated";

export interface Session {
  id: string;
  name: string;
  /** Whether `name` is still a placeholder the auto-title job may replace ("derived"), or claimed ("user"/"generated"). */
  name_source?: SessionNameSource;
  activity: SessionActivity;
  workspace?: string;
  provider?: string;
  model?: string;
  agent?: string;
  isolation?: string;
  trust?: SessionTrust;
  worktree_dir?: string;
  worktree_branch?: string;
  /** The branch the checkout had open when this session's worktree was made — the Land target. Absent for a session working in place. */
  worktree_base?: string;
  /**
   * The header state (docs/SESSION-WORKTREES-DESIGN.md D4): this session's
   * files git still reports changed, and its branch's commits the base
   * lacks (always 0 in place). Recomputed server-side after file changes,
   * Commit, Land and pipeline merges — read, never derived, here.
   */
  uncommitted_count?: number;
  unlanded_count?: number;
  /**
   * The directory the session asked to start in — the Launchpad's folder,
   * else its workspace's primary repo, else Config.default_dir. Absent means
   * nobody asked: the agent runs wherever the Router was started.
   */
  dir?: string;
  /** Absolute directory the agent runs in — the docked shell opens here so it shares the agent's context. */
  cwd?: string;
  summary?: string;
  active_task_id?: string;
  tags?: string[];
  retro_id?: string;
  synthesis_mode?: string;
  provider_ref?: string;
  /** provider_ref is an imported native conversation: the next start forks it into a new ref (then clears this) instead of appending to it. */
  fork_on_resume?: boolean;
  /** Native conversation id this session was imported from; survives the first wake's fork, which replaces provider_ref. Set once by the import. */
  imported_from?: string;
  /** Most recent turn's token accounting, persisted by the Runner on every turn end — the context meter's data source at wake time, before this process has seen a turn_end of its own. */
  last_usage?: Usage;
  /**
   * The latest main-agent transition, mirrored onto the record by the same
   * write that appends the session.agent_state event — so a cold reader (a
   * page load or an SSE reconnect mid-turn) is correct without replaying the
   * log. Absent before the session's first transition, and to be read as
   * idle on an inactive session: only the live Runner clears it, so a
   * hard-killed process leaves its last working state behind.
   */
  agent_state?: AgentState;
  /**
   * When the most recent turn ended. The rail's last-reply stamp and its
   * recency key: unlike updated_at — which renames, summary writes and
   * activity flips all bump — this is the only field that means "last
   * response". Absent until the session's first turn ends.
   */
  last_turn_at?: string;
  created_at: string;
  updated_at: string;
}

/**
 * mirrors pkg/contracts/conversation.go AgentState — the session main agent's
 * in-flight state. The payload of a session.agent_state envelope, and the
 * value mirrored onto Session.agent_state above.
 */
export interface AgentState {
  state: "working" | "idle";
  /**
   * The server-built one-line clause naming what the agent is doing: a verb
   * for the enriched tool kind plus the adapter-built title verbatim
   * ("Running bash · make check"), the generic "Working…" before a turn's
   * first activity, or "Thinking…". Built from the same enrichment
   * ToolUse.title carries, so the rail line and the conversation's working
   * indicator can never disagree. Empty on idle.
   */
  label?: string;
  /**
   * When the CURRENT state began — the turn's start for every working event
   * in that turn, unchanged as the label transitions, so an elapsed counter
   * driven off it spans the whole turn rather than resetting per tool.
   */
  since: string;
}

export interface WorkspaceRepo {
  path: string;
  primary?: boolean;
}

export interface WorkspaceDefaults {
  agent?: string;
  /** Redesign §7: the workspace's default persona. Mirror of `agent` (the
   * single source of truth is `agent`); the persona UI reads/writes this. */
  persona?: string;
  model?: string;
  isolation?: string;
  template?: string;
  /** Redesign §7: the workspace's preferred harness (falls back to the
   * global DefaultProvider when absent). */
  provider?: string;
}

export interface Workspace {
  id: string;
  name: string;
  description?: string;
  repos?: WorkspaceRepo[];
  defaults: WorkspaceDefaults;
  steering_files?: string[];
  capabilities?: string[];
  created_at: string;
  updated_at: string;
}

// mirrors pkg/contracts/sessiondefaults.go — which layer produced a
// resolved session-default value (GET /sessions/defaults, Launchpad preview).
export type SessionDefaultSource = "workspace" | "config" | "provider_default" | "builtin";

export interface ResolvedSessionField {
  value?: string;
  source?: SessionDefaultSource;
}

export interface SessionDefaults {
  provider: ResolvedSessionField;
  model: ResolvedSessionField;
  agent: ResolvedSessionField;
  isolation: ResolvedSessionField;
  trust: ResolvedSessionField;
  /** Where the session would start. Source "builtin" = the Router's own directory (nobody asked). */
  dir: ResolvedSessionField;
  /** What's at `dir` on disk — "plain" means worktree isolation will run in the folder itself. */
  dir_status?: DirStatus;
  /** The branch the checkout at `dir` has open — only for a git dir on a branch (absent when detached). */
  dir_branch?: string;
  workspace?: string;
}

// mirrors pkg/contracts/sessiondefaults.go DirStatus.
export type DirStatus = "git" | "plain" | "missing";

// mirrors pkg/contracts/registry.go Quadrant/Quadrants — a closed 4-value
// enum (W6.8, MADS-UX §3.5's quadrant tooltip).
export type Quadrant =
  | "autonomous_executor"
  | "technical_partner"
  | "investigator"
  | "research_analyst";

export interface AgentCapabilities {
  tools?: string[];
  mcp_servers?: string[];
  skills?: string[];
}

/**
 * POST /agents/suggest-capabilities reply: registry-grounded skill and MCP
 * server names plus a ToolIntent allowlist. `dropped` names anything the
 * Fast Model proposed that isn't registered — already removed server-side.
 */
export interface CapabilitySuggestion {
  allowlist?: ToolIntent[];
  mcp_servers?: string[];
  skills?: string[];
  dropped?: string[];
}

export interface AgentPermissions {
  allowed_paths?: string[];
  denied_commands?: string[];
}

// mirrors pkg/contracts/permission.go PermissionIntent — the portable,
// provider-independent vocabulary that SessionTrust and AgentPermissions
// both compile down to (see AgentPermissions.Intents() in that file).
// read_only and require_approval are session-trust-only today: they have no
// AgentPermissions field, so nothing here can compile an agent record to
// those two intents yet.
export type PermissionIntent = "deny_commands" | "path_scope" | "read_only" | "require_approval";

// mirrors providers/provider/intent.go IntentMatrix — a provider's declared
// enforcement level for each intent in the v1 vocabulary. Absent means the
// provider doesn't implement IntentEnforcer and has taken no stance; treat
// that identically to "unsupported" (see ProviderInfo.intents below).
export type IntentMatrix = Partial<Record<PermissionIntent, ProviderSupport>>;

export interface AgentPreferences {
  model?: string;
}

// mirrors pkg/contracts/toolintent.go — the closed ToolIntent vocabulary the
// persona's allowlist/denylist speak (agent-concept redesign §2). Harness-
// free by construction: no concrete tool name belongs here.
export type ToolIntent =
  | "read"
  | "write"
  | "edit"
  | "shell"
  | "search"
  | "fetch"
  | "skill"
  | "mcp";

// The full v1 closed set (contracts.ToolIntents) — the enumeration the
// persona editor's chip list iterates over.
export const TOOL_INTENTS: readonly ToolIntent[] = [
  "read",
  "write",
  "edit",
  "shell",
  "search",
  "fetch",
  "skill",
  "mcp",
] as const;

export interface Agent {
  id: string;
  name: string;
  description?: string;
  prompt: string;
  quadrant?: Quadrant;
  tags?: string[];
  system?: boolean;
  created_at: string;
  updated_at: string;
  /** Redesign §2: the persona's allowed tool intents (harness-free). */
  allowlist?: ToolIntent[];
  /** Redesign §2: the persona's denied tool intents (override allowlist). */
  denylist?: ToolIntent[];
  capabilities?: AgentCapabilities;
  permissions?: AgentPermissions;
  preferences?: AgentPreferences;
}

// mirrors pkg/agentselect/similarity.go Similar — a near-duplicate warning
// attached to a freshly generated agent draft.
export interface Similar {
  agent: string;
  score: number;
}

export type CapabilityType = "skill" | "mcp";

export interface CapabilityConfig {
  path?: string; // skills
  command?: string; // mcp, local process
  url?: string; // mcp, remote
}

export interface CapabilityHealth {
  healthy: boolean;
  reason?: string;
  checked_at?: string;
}

// Full Capability — the Library editor's shape. CapabilitySummary (above)
// stays as-is for the / command palette.
export interface Capability {
  id: string;
  name: string;
  description?: string;
  type: CapabilityType;
  config: CapabilityConfig;
  credential_env_vars?: string[]; // NAMES ONLY — never a value, see CredentialEnvVarsField
  health?: CapabilityHealth;
  created_at: string;
  updated_at: string;
}

export interface Template {
  id: string;
  name: string;
  description?: string;
  source: string; // the YAML text — single source of truth for both editor views
  system?: boolean;
  // Monotonically increasing save counter — 1 on create, incremented on
  // every update. An audit-trail affordance only; never used for optimistic
  // concurrency or diffing.
  version: number;
  created_at: string;
  updated_at: string;
}

// mirrors the parallel validate-all sweep endpoint's response item — POST
// /templates/validate-all. `errors` is always an array, never null, even
// when `valid` is true.
export interface TemplateValidationResult {
  id: string;
  name: string;
  valid: boolean;
  errors: { field: string; message: string }[];
}

// mirrors pkg/registryclient's wire contract for the remote registry
// (spec §12.9, W7.6) — deliberately excludes workspace bundles.
export type RegistryItemType = "agent" | "capability" | "template";

export interface RegistryEntry {
  type: RegistryItemType;
  name: string;
  description?: string;
  versions: string[];
  latest: string;
}

export interface RegistryPublishItem {
  type: RegistryItemType;
  id: string;
}

// pkg/template's parsed grammar, frontend-only — this is YAML the Library's
// Templates shelf parses client-side for the visual editor view (no Go JSON
// tags to mirror; pkg/template only ever round-trips YAML).
// `when` on a stage's retry block defaults server-side to "error" (retry
// infrastructure failures only — crashes, transport errors, timeouts — but
// NOT a considered verdict like a reviewer's rejection or a command's
// non-zero exit); "always" also retries verdicts; "never" disables. Left as
// a plain string (not a closed union) since the frontend never branches on
// it — only renders whatever the Select picked and forwards it verbatim.
export interface TemplateRetry {
  max?: number; // 0..10; rejected outright by the validator on a `git merge` stage
  when?: string;
  backoff?: string; // Go duration string, e.g. "5s"
}

export interface TemplateStage {
  name: string;
  agent?: string; // "auto" or a registry agent name; mutually exclusive with command
  command?: string;
  when?: string;
  on_fail?: { loop_back?: string; max_loops?: number; then?: string };
  model?: string; // freeform model id; empty/absent = inherit (agent pref, then provider default)
  timeout?: string; // Go duration string, e.g. "15m"
  retry?: TemplateRetry;
}

// Template-level defaults a stage inherits when it doesn't set its own
// timeout/model/retry — same shapes as the per-stage fields above.
export interface TemplateDefaults {
  timeout?: string;
  model?: string;
  retry?: TemplateRetry;
}

export interface ParsedTemplate {
  name: string;
  stages: TemplateStage[];
  defaults?: TemplateDefaults;
  // Other top-level keys the grammar defines that this frontend doesn't
  // model explicitly yet. templateSync.parse() must preserve these verbatim
  // — see the comment there for why.
  [key: string]: unknown;
}

export type Provider = string;

export interface Model {
  id: string;
  name: string;
  default?: boolean;
}

// mirrors contracts.ModelsResponse's state field. An empty model list means
// three different things and the picker renders each differently:
// "refreshing" — discovery is still running, so say so rather than showing
// an empty menu; "unavailable" — the harness could not be asked (binary
// missing, not logged in), paired with a human-readable detail; "ready" —
// the provider genuinely offers nothing.
export type ModelCatalogState = "pending" | "refreshing" | "ready" | "unavailable";

export interface ModelCatalog {
  models: Model[];
  state?: ModelCatalogState;
  detail?: string;
  fetchedAt?: string;
}

export interface Envelope {
  seq: number;
  timestamp: string;
  type: string;
  session_id?: string;
  task_id?: string;
  actor: string;
  payload?: unknown;
}

/**
 * TS mirrors of Go contracts consumed by the Conversation tab. Keep field
 * names/optionality in lockstep with the Go source so wiring a real Router
 * endpoint later is a rename, not a redesign — see ui/src/conversation/
 * HANDOFF.md §1 for which of these already exist on the Router vs. only on
 * the per-session process or not at all.
 */

// mirrors providers/provider/event.go ToolKind — ACP's tool-kind vocabulary
// plus mads extensions (mcp, skill, plan). Icon choice and activity grouping
// key off this, never off the harness-specific tool name.
export type ToolKind =
  | "read"
  | "edit"
  | "delete"
  | "move"
  | "search"
  | "execute"
  | "think"
  | "fetch"
  | "agent"
  | "mcp"
  | "skill"
  | "plan"
  | "other";

// mirrors providers/provider/event.go ToolStatus — a tool call's lifecycle
// state. Absent means the wire didn't state one (yet).
export type ToolStatus = "pending" | "in_progress" | "completed" | "failed" | "interrupted";

// mirrors providers/provider/event.go ToolLocation — one file position a
// tool call touched (absolute path when known; line is 1-based, absent
// means the whole file).
export interface ToolLocation {
  path: string;
  line?: number;
}

// mirrors providers/provider/event.go ToolUse
export interface ToolUse {
  id?: string;
  name: string;
  input?: unknown; // json.RawMessage
  path?: string;
  // Enriched rendering fields (all optional — records persisted before
  // enrichment simply lack them; treat absence as unknown).
  kind?: ToolKind;
  // Server-built one-line human summary of the call — supersedes the UI's
  // own summarize() derivation when present.
  title?: string;
  status?: ToolStatus;
  locations?: ToolLocation[];
  // MCP server name, set only when kind is "mcp".
  server?: string;
  // result is attached client-side (toolPairing.ts's foldToolResults) by
  // correlating a "tool_result" TranscriptMessage back to this tool_use by
  // ID — never present on the wire shape itself.
  result?: ToolResult;
}

export interface ToolResult {
  text?: string;
  is_error?: boolean;
  timestamp?: string; // RFC3339
}

// mirrors providers/provider/event.go Usage
export interface Usage {
  input_tokens: number;
  output_tokens: number;
  // Prompt-cache halves of context, billed separately from input_tokens.
  // On a cached turn the bulk of the real context is cache_read — omitting
  // these makes input_tokens look like just the newest (uncached) message.
  cache_creation_input_tokens?: number;
  cache_read_input_tokens?: number;
  model?: string;
  // Prompt-side occupancy of the turn's FINAL API call — the accurate
  // context measure. The token fields above are a per-turn billing
  // aggregate on providers that iterate (claude-code sums cache reads
  // across every tool round-trip), which overstates occupancy by roughly
  // the iteration count; prefer this when present.
  context_tokens?: number;
  // Provider-reported window size for model — authoritative over the
  // static CONTEXT_WINDOWS table (modelDisplay.ts).
  context_window?: number;
}

// mirrors pkg/contracts/transcript.go Attribution — identifies the linked
// session a message was injected from (docs/LINKED-SESSIONS-DESIGN.md
// decision 2).
export interface Attribution {
  link_id: string;
  source_name: string;
}

// mirrors providers/provider/event.go Message (TranscriptReader shape)
export interface TranscriptMessage {
  role: "user" | "agent" | "tool" | "tool_result" | "compact" | "notification";
  // on role "compact", the summary the agent carried forward; on role
  // "notification" (a background task finished — the harness telling the
  // agent, not the human speaking), the harness's one-line summary
  text?: string;
  tool?: ToolUse;
  attribution?: Attribution;
  timestamp?: string; // RFC3339, when the provider recorded this entry
  is_error?: boolean; // set on role "tool_result" when the tool call failed
  compact?: CompactMarker; // set on role "compact": one compaction pass, where it happened
}

// mirrors pkg/contracts/transcript.go CompactMarker. Zero/absent fields mean
// the provider didn't record them.
export interface CompactMarker {
  trigger?: "manual" | "auto" | string;
  pre_tokens?: number;
  post_tokens?: number;
  duration_ms?: number;
}

// mirrors pkg/contracts/link.go Grant
export type Grant = "read" | "inject" | "orchestrate";

// mirrors pkg/contracts/link.go LinkPreset. "hand_raiser" is system-only —
// never offered via the user-facing link chooser or the user CRUD route.
export type LinkPreset = "monitor" | "hand_raiser" | "leader";

// mirrors pkg/contracts/link.go PresetGrants
export const PRESET_GRANTS: Record<LinkPreset, Grant[]> = {
  monitor: ["read"],
  hand_raiser: ["read", "inject"],
  leader: ["read", "inject", "orchestrate"],
};

// mirrors pkg/contracts/link.go LinkCadence
export type LinkCadence = "per_turn" | "on_signal" | "manual_only";

// mirrors pkg/contracts/link.go OrchestrateStatus. Server-computed only —
// never sent in a LinkRequest. Empty ("" — omitted from Link.orchestrate_status
// when the link carries no orchestrate grant) reads the same as absent.
export type OrchestrateStatus = "pending_restart" | "active" | "unsupported" | "failed";

// mirrors pkg/contracts/link.go Link
export interface Link {
  id: string;
  granting_session_id: string;
  subject_session_id: string;
  grants: Grant[];
  cadence: LinkCadence;
  implicit?: boolean;
  orchestrate_status?: OrchestrateStatus;
  /** Why the tools failed to start; set only while orchestrate_status is "failed". */
  orchestrate_error?: string;
  created_at: string;
  updated_at: string;
}

// mirrors pkg/contracts/link.go LinkRequest
export interface LinkRequest {
  granting_session_id: string;
  subject_session_id: string;
  grants?: Grant[];
  preset?: LinkPreset;
  cadence?: LinkCadence;
}

// mirrors pkg/contracts/link.go NewSessionPlaceholder — the to-be-created
// session's slot in a staged LinkRequest on POST /sessions.
export const NEW_SESSION_PLACEHOLDER = "$new";

export type CreateSessionPayload = Partial<Session> & {
  links?: LinkRequest[];
  /** Composer's full opening text — only consumed when agent="auto" (pkg/capinfer inference); never persisted on the Session record. */
  opening_prompt?: string;
};

// mirrors providers/provider/event.go PermissionRequest — one parked
// PreToolUse hook call awaiting an allow/deny decision (chat transport only,
// TrustLimited/TrustReadOnly). status starts "pending" and moves to
// "allowed"/"denied"/"expired"; GET /sessions/{id}/permission-requests
// returns only still-pending ones (services/session's PermissionRequests),
// which is what lets a still-open card survive a transcript refetch.
export interface PermissionRequest {
  id: string;
  tool: string;
  input?: unknown; // json.RawMessage
  status: "pending" | "allowed" | "denied" | "expired";
}

// mirrors providers/provider/event.go CompactStatus — which phase of a
// native compaction pass this is. Phase "start" is the ongoing state; "done"
// carries success/error. Only claude-code's stream-json transport emits this
// today (providers/claudecode/wire.go's systemEvents).
export interface CompactStatus {
  phase: "start" | "done";
  success?: boolean;
  error?: string;
}

// mirrors pkg/contracts/conversation.go QueuedMessage: one user message
// waiting in the session process's queue behind an in-flight turn.
export interface QueuedMessage {
  id: string;
  text: string;
  queued_at: string;
}

// mirrors pkg/contracts/conversation.go QueueChanged (payload of
// "session.queue_changed"): the whole remaining queue, plus the entry that
// just went out as the next turn (delivered) or failed to (failed + error).
export interface QueueChanged {
  queued: QueuedMessage[];
  delivered?: QueuedMessage;
  failed?: QueuedMessage;
  error?: string;
}

/** What POST /sessions/{id}/messages did with a send: ran it, or queued it behind the in-flight turn. */
export type SendResult = { status: "accepted" } | { status: "queued"; queued: QueuedMessage };

// mirrors providers/provider/event.go TaskStatus (payload of the "task"
// stream event) and pkg/contracts/conversation.go SubagentTask (payload of
// persisted "session.subagent_task" envelopes — identical shape, plus
// parent_tool_use_id for a nested spawn's lifecycle line, depth >= 2).
// Which fields are present depends on phase; absent means the wire line
// didn't carry the field.
export interface SubagentTask {
  phase: "started" | "progress" | "updated" | "notification";
  task_id: string;
  // The spawning Agent tool_use's ID — correlation key to the subagent's
  // own parent-tagged stream. Absent on "updated" lines.
  tool_use_id?: string;
  description?: string;
  subagent_type?: string;
  prompt?: string; // "started" only
  is_backgrounded?: boolean; // "started" only
  spawn_depth?: number; // "started" only; 1 = spawned by the main agent
  // Running counters ("progress") or final totals ("notification").
  total_tokens?: number;
  tool_uses?: number;
  duration_ms?: number;
  last_tool_name?: string; // "progress" only
  status?: string; // "updated" and "notification", e.g. "completed"
  summary?: string; // "notification" only — the subagent's final report
  output_file?: string; // "notification" only
  parent_tool_use_id?: string; // contracts payload only: nested spawn's tag
}

// mirrors providers/provider/event.go Event's subagent tagging fields —
// present on any stream event a subagent's sidechain produced. Absent (all
// three) means the main agent produced the event; consumers must route
// tagged events to the spawning tool call's own stream, never interleave
// them into the main transcript.
export interface SubagentTag {
  parent_tool_use_id?: string;
  subagent_type?: string;
  task_description?: string;
}

// mirrors providers/provider/event.go Event (normalized data-plane stream).
// Every variant may additionally carry the SubagentTag fields.
export type StreamEvent = SubagentTag &
  (
    | { kind: "text"; text: string }
    | { kind: "tool_use"; tool: ToolUse }
    | { kind: "turn_end"; usage?: Usage; interrupted?: boolean }
    | { kind: "error"; text: string }
    | { kind: "pty_output"; text: string }
    | { kind: "tool_result"; tool?: ToolUse; text?: string; is_error?: boolean }
    | { kind: "permission_request"; permission: PermissionRequest }
    | { kind: "compact"; compact: CompactStatus }
    // A spawned subagent task's lifecycle update (the harness's own
    // bookkeeping — spawn, progress counters, final status + summary).
    | { kind: "task"; task: SubagentTask }
    // Extended thinking: text set = a whole thinking block;
    // thinking_tokens alone = a live estimated-token progress tick for the
    // "Thinking…" indicator title.
    | { kind: "thinking"; text?: string; thinking_tokens?: number }
  );

// mirrors pkg/contracts/conversation.go SubagentEvent — the payload of a
// persisted "session.subagent_event" Envelope: one conversation event a
// subagent sidechain produced. The event log is the ONLY historical source
// for these (claude-code's transcript omits sidechains); fetch a subagent's
// stream with GET /events?session_id=…&types=session.subagent_event and
// filter by parent_tool_use_id.
export interface SubagentEvent {
  parent_tool_use_id: string;
  subagent_type?: string;
  task_description?: string;
  kind: "text" | "tool_use" | "tool_result" | "thinking";
  text?: string;
  tool?: ToolUse;
  is_error?: boolean;
}

// mirrors pkg/contracts/conversation.go ThinkingBlock — the payload of a
// persisted "session.thinking" Envelope: one whole main-agent thinking
// block (token ticks are never persisted).
export interface ThinkingBlock {
  text: string;
  thinking_tokens?: number;
}

// mirrors pkg/contracts/conversation.go ToolStatusChange — the payload of a
// persisted "session.tool_status_changed" Envelope: the runner's status
// synthesis (pkg/toolstatus) moved a tool call to a state no wire event
// carries ("interrupted" on a resultless turn end, "pending" while
// permission-parked, "in_progress" on allow). Merge by tool_use_id.
export interface ToolStatusChange {
  tool_use_id: string;
  status: ToolStatus;
  parent_tool_use_id?: string;
}

// mirrors pkg/contracts/session.go Session (fields this tab reads)

// New contract this task introduces on the frontend only (no Go type yet —
// flag for whoever implements GET /mentionables to reconcile).
export type MentionableKind = "skill_doc" | "session_summary" | "steering_file";

export interface Mentionable {
  id: string;
  kind: MentionableKind;
  label: string;
  detail?: string;
}

// mirrors pkg/contracts/registry.go Capability (subset used by / commands)
export interface CapabilitySummary {
  id: string;
  name: string;
  type: "skill" | "mcp";
  description?: string;
  healthy: boolean;
}

// mirrors pkg/contracts/task.go TaskStatus (W6.4). Dependency-waiting tasks
// keep status "blocked" in the schema — see ui/src/tasks/HANDOFF.md §2 for
// the client-side heuristic that renders this calmly ("Queued") vs. red
// ("needs human").
export type TaskStatus =
  | "ready"
  | "in_progress"
  | "blocked"
  | "needs_review"
  | "done"
  | "wont_do";

// mirrors pkg/contracts/task.go Task (fields this tab reads/writes).
// Handoffs are omitted — not read by this tab (HANDOFF.md §1a).
export interface Task {
  id: string;
  session_id: string;
  status: TaskStatus;
  dependencies?: string[];
  labels?: string[];
  required_capabilities?: string[];
  retry_count: number;
  created_at: string;
  updated_at: string;
  title: string;
  description?: string;
  acceptance_criteria?: string;
  scope?: string;
  draft?: boolean;
  source: string;
  dpa_id?: string;
  notes?: Note[];
}

// mirrors pkg/contracts/task.go NoteType/NoteStatus/Note
export type NoteType =
  | "blocker"
  | "decision"
  | "context"
  | "risk"
  | "follow_up"
  | "observation";
export type NoteStatus = "draft" | "confirmed";
export interface Note {
  id?: number;
  type: NoteType;
  content: string;
  source: string;
  status: NoteStatus;
  timestamp: string;
}

// mirrors pkg/contracts/proposal.go ProposalType/ProposalStatus/Proposal —
// synthesizer-authored suggestions awaiting review, rendered as cards in the
// Context Rail (spec §4).
export type ProposalType = "create_task" | "add_note" | "update_task";
export type ProposalStatus = "proposed" | "approved" | "dismissed";

export interface ProposedTask {
  title: string;
  description?: string;
  acceptance_criteria?: string;
  scope?: string;
}

export interface ProposedNote {
  type: NoteType;
  content: string;
}

// Non-empty fields replace the target task's on approval; empty fields leave it unchanged.
export interface ProposedTaskUpdate {
  title?: string;
  description?: string;
  acceptance_criteria?: string;
  scope?: string;
}

export interface Proposal {
  id: string;
  session_id: string;
  type: ProposalType;
  status: ProposalStatus;
  task_id?: string;
  task?: ProposedTask;
  note?: ProposedNote;
  update?: ProposedTaskUpdate;
  applied_task_id?: string;
  source: string;
  created_at: string;
  updated_at: string;
}

// mirrors pkg/contracts/session.go FileChange (Context Rail Changes tab)
// mirrors pkg/contracts/session.go FileChange. `change` is a plain string on
// the Go struct (comment-documented as added|modified|deleted); narrowed to
// a closed union here since the frontend only ever branches on those three.
export type ChangeKind = "added" | "modified" | "deleted";
export type ChangeOrigin = "tool_use" | "commit" | "observed";

export interface FileChange {
  id: number;
  session_id: string;
  task_id?: string;
  repo?: string;
  path: string;
  change: "added" | "modified" | "deleted";
  origin: string;
  attributed: boolean;
  actor?: string;
  timestamp: string;
}

// mirrors pkg/contracts/session.go Commit
export type LinkedVia = "trailer" | "session_context" | "heuristic";

// mirrors services/router/internal/api/commits.go commitFile — Router-
// derived via `git show --name-status`, not stored by the Data Service.
export interface CommitFile {
  path: string;
  change: "added" | "modified" | "deleted";
}

export interface Commit {
  hash: string;
  session_id: string;
  task_id?: string;
  repo?: string;
  message: string;
  author?: string;
  linked_via?: LinkedVia;
  orphan?: boolean;
  timestamp: string;
  files?: CommitFile[]; // Router-derived, best-effort, absent when unresolvable
}

// --- Commit and Land (docs/SESSION-WORKTREES-DESIGN.md D5, D6) ---
// mirrors pkg/contracts/session_worktree.go

export type WorktreeFileKind = "added" | "modified" | "deleted";

/** One session file git still reports changed: a Commit dialog row. Path is relative to the session's folder. */
export interface WorktreeFile {
  path: string;
  kind: WorktreeFileKind;
}

export interface WorktreeCommit {
  hash: string;
  subject: string;
  undoable: boolean;
}

/** Why Land won't (or didn't) run. Reason is a LandRefusalReason; Detail is human text or git's own output. */
export interface LandRefusal {
  reason: string;
  detail?: string;
  files?: string[];
}

/** GET /sessions/{id}/worktree — runs git per call, so fetched when a dialog opens, never per render. */
export interface WorktreeStatus {
  isolated: boolean;
  folder?: string;
  branch?: string;
  base?: string;
  files: WorktreeFile[];
  uncommitted_count: number;
  unlanded_count: number;
  last_commit?: WorktreeCommit;
  land_blocked?: LandRefusal;
  /** Router-added: other sessions working inside this folder (a linked session that joined it). Delete keeps the folder while any do. */
  shared_with?: WorktreeTenant[];
  /** Router-added: a pipeline run for this session is active. Delete keeps the folder then too. */
  pipeline_running?: boolean;
}

export interface WorktreeTenant {
  id: string;
  name: string;
}

export interface CommitRequest {
  /** Relative to the session's folder; empty means every uncommitted session file. */
  paths?: string[];
  /** Empty asks the Fast Model to draft one (only when Config.auto_commit_messages is on). */
  message?: string;
}

export interface CommitPath {
  path: string;
  status: "committed" | "already_committed" | "rejected";
  /** Rejected paths only: "outside_folder", "not_session_file", "pipeline_task_file". */
  reason?: string;
}

export interface CommitResult {
  hash: string;
  message: string;
  drafted?: boolean;
  undoable: boolean;
  paths: CommitPath[];
}

export type CommitErrorCode =
  | "invalid_paths"
  | "nothing_to_commit"
  | "message_required"
  | "git_failed"
  | "no_folder"
  | "no_fast_model"
  | "draft_failed"
  | "locked";

/** A refused POST /commit or /commit/draft: rendered in place, keeping the user's selection. */
export interface CommitError {
  code: CommitErrorCode | string;
  error: string;
  /** git's own output, verbatim (git_failed). */
  detail?: string;
  paths?: CommitPath[];
  selection?: string[];
}

export interface CommitDraft {
  message: string;
  /** The diff was cut to fit, so the draft saw only part of the change. */
  truncated?: boolean;
}

export interface UndoCommitResult {
  undone: boolean;
  hash: string;
  /** "not_tip", "not_session_commit", "merge_commit", "already_landed", "already_pushed", "mid_merge", "no_folder", "locked". */
  reason?: string;
  detail?: string;
}

/**
 * auto_resolved: bringing the base into the session's branch merged cleanly
 * only because mergiraf resolved files plain git would have conflicted on.
 * That merge is committed in the session's folder (merge_commit) and Land
 * stopped before the user's checkout; files names what mergiraf merged.
 */
export type LandOutcome = "landed" | "conflicted" | "refused" | "auto_resolved";

/** Land refusal reasons — gates checked before anything runs, then what step 1/2 can refuse with. */
export type LandRefusalReason =
  | "not_isolated"
  | "busy"
  | "pipeline"
  | "mid_merge"
  | "off_branch"
  | "dirty"
  | "base_not_checked_out"
  | "primary_mid_merge"
  | "nothing_to_land"
  | "merge_failed"
  | "base_moving"
  | "primary_dirty"
  | "primary_untracked"
  | "primary_refused"
  | "locked";

/** POST /sessions/{id}/land — HTTP 200 for every outcome. */
export interface LandResult {
  outcome: LandOutcome;
  branch?: string;
  base?: string;
  merge_commit?: string;
  fast_forwarded?: boolean;
  files?: string[];
  reason?: string;
  detail?: string;
}

// Matches the fileDiff response shared by GET
// /sessions/{id}/commits/{hash}/diff (commit_diff.go) and GET
// /sessions/{id}/changes (working_diff.go, working-change diffs).
// `language` is a Monaco language id, guessed client-side from the path
// extension — fine to get wrong, it only affects syntax highlighting.
export interface FileDiff {
  path: string;
  language?: string;
  original: string;
  modified: string;
}

// mirrors services/router/internal/api/tool_diff.go toolDiff — one mutating
// tool call's historically-accurate before/after content from the shadow
// store (GET /sessions/{id}/tool-diff?tool_use_id=…). A 404 on that route
// means "no capture recorded — keep the input-derived diff" and is surfaced
// as null by RouterClient.getToolDiff, never as an error.
export interface ToolDiff {
  path: string;
  original: string;
  modified: string;
  // The pre-image exceeded the ~10 MB capture guard — original is empty and
  // the UI must say so rather than render an empty-old-side lie.
  original_too_large?: boolean;
  // The after side was itself a too-large marker.
  modified_too_large?: boolean;
}

/**
 * TS mirrors of pkg/contracts/metrics.go, consumed by the Dashboard tab
 * (W6.9, MADS-UX §3.6). Generic Data Service contracts, not Dashboard-
 * private, so they live here alongside the rest of the router types.
 */

// mirrors pkg/contracts/metrics.go CostPoint. `day` is an ISO timestamp,
// UTC-truncated to midnight.
export interface CostPoint {
  day: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
}

// mirrors pkg/contracts/metrics.go CostBySession
export interface CostBySession {
  session_id: string;
  cost_usd: number;
}

// mirrors pkg/contracts/metrics.go CostByPipelineRun
export interface CostByPipelineRun {
  session_id: string;
  run_seq: number;
  cost_usd: number;
}

// mirrors pkg/contracts/metrics.go CostMetrics — GET /metrics/cost's response
export interface CostMetrics {
  by_day: CostPoint[];
  by_session: CostBySession[];
  by_pipeline_run: CostByPipelineRun[];
}

// mirrors pkg/contracts/metrics.go AgentLeaderboardEntry
export interface AgentLeaderboardEntry {
  agent: string;
  usage_count: number;
  successes: number;
  failures: number;
  success_rate: number;
  retries: number;
}

// mirrors pkg/contracts/metrics.go TaskOutcomes — GET /metrics/tasks's response
export interface TaskOutcomes {
  counts: Partial<Record<TaskStatus, number>>;
  total: number;
  completion_rate: number;
}

// mirrors pkg/contracts/metrics.go CapabilityUsage
export interface CapabilityUsage {
  capability: string;
  type: CapabilityType;
  invocations: number;
}

// mirrors pkg/contracts/metrics.go PipelineRun — one reconstructed pipeline
// run. `outcome` matches the Go comment's closed set.
export type PipelineRunOutcome =
  | "completed"
  | "incomplete"
  | "cancelled"
  | "failed"
  | "running"
  | "interrupted";

export interface PipelineRun {
  session_id: string;
  run_seq: number;
  run_id?: string; // from pipeline.started; absent for runs from before it carried one
  started_at: string;
  ended_at?: string;
  outcome: PipelineRunOutcome;
  tasks_done: number;
  tasks_blocked: number;
  tasks_skipped: number;
  cost_usd: number;
  agents?: string[];
}

// mirrors pkg/contracts/retro.go DPAStatus
export type DPAStatus = "accepted" | "rejected" | "edited";

// mirrors pkg/contracts/retro.go RemediationLayer
export type RemediationLayer =
  | "agent_config"
  | "tool_config"
  | "prompt_engineering"
  | "task_decomposition"
  | "dependency_structure"
  | "pipeline_template"
  | "human_process";

// mirrors pkg/contracts/retro.go DPA. ID is unique within its owning Retro
// only (minted "dpa_1", "dpa_2", ... server-side).
export interface DPA {
  id: string;
  problem: string;
  evidence: string;
  root_cause: string;
  remediation_layer: RemediationLayer;
  suggested_action: string;
  status: DPAStatus;
}

// mirrors pkg/contracts/retro.go Retro. `dpas` is present but can be an
// empty array (never null — Data Service always writes []DPA, never nil)
// before generation finishes; the review UI only ever renders this after
// GET /retros/{id} following retro.completed, so treat it as always
// populated at the point this UI reads it.
export interface Retro {
  id: string;
  session_ids: string[];
  user_observations?: string;
  dpas: DPA[];
  follow_up_session_id?: string;
  created_at: string;
  updated_at: string;
}

// mirrors pkg/contracts/config.go (W6.10 Settings). One global row — see
// services/data/internal/store/config.go.
export type SynthesisMode = "off" | "light" | "full";
export type AgentSelectionMode = "light" | "full";
export type ModelTier = "cheapest" | "balanced" | "best";
export type ThemeMode = "dark" | "light" | "system";
// mirrors contracts.StreamSpeed — caps the conversation's render rate.
export type StreamSpeed = "off" | "slow" | "medium" | "fast";
export type PaletteName = "clay" | "space" | "glacier";
// FastModelMode mirrors pkg/contracts/session.go: provider uses the default
// provider's hermetic Completer capability (zero-config), endpoint uses the
// explicit fast_model_base_url, off disables Fast Model features entirely.
export type FastModelMode = "provider" | "endpoint" | "off";

export interface FeatureFlags {
  multi_repo: boolean;
  linked_sessions: boolean;
  remote_registry: boolean;
}

export interface Config {
  synthesis_mode: SynthesisMode;
  // SynthesisAutoApprove applies every synthesis proposal immediately on
  // creation instead of leaving it pending for review (power-user
  // preference, spec §4). Applies live — not in RestartRequiredFields.
  synthesis_auto_approve: boolean;
  agent_selection_mode: AgentSelectionMode;
  default_model_tier: ModelTier;
  cost_alert_cents?: number;
  // pipeline_stage_timeout_minutes bounds how long a single pipeline stage
  // (one agent invocation) may run before the engine kills it as stuck —
  // mirrors pkg/contracts/config.go's PipelineStageTimeoutMinutes. 0/absent
  // means the server-side default (30m), so this is intentionally typed
  // as a plain number, not optional: the server always returns a resolved
  // value, never a bare absence.
  pipeline_stage_timeout_minutes: number;
  // allow_sleep_during_pipelines mirrors pkg/contracts/config.go's
  // AllowSleepDuringPipelines. Negatively named so its zero value (false)
  // means "prevent sleep" for every stored config predating it; the
  // Settings checkbox binds to its negation. Read once at run launch.
  allow_sleep_during_pipelines: boolean;
  // auto_commit_messages mirrors pkg/contracts/config.go's
  // AutoCommitMessages — Settings' "Write commit messages for me": the
  // header's Commit skips the dialog and the Fast Model drafts the message.
  // Optional: configs saved before it read as off.
  auto_commit_messages?: boolean;

  default_workspace?: string;
  default_provider?: string;
  default_trust: SessionTrust;
  /** Launchpad "Save as my defaults" — below a workspace's own defaults, above the builtins. */
  default_dir?: string;
  /** Only applies while the session's provider is default_provider. */
  default_model?: string;
  /** Persona id, or "auto". */
  default_agent?: string;
  default_isolation?: string;
  default_template?: string;
  fast_model?: string;
  fast_model_base_url?: string;
  fast_model_api_key_env?: string;
  fast_model_mode?: FastModelMode;

  display_mode: string;
  keybindings: string;
  theme: ThemeMode;
  palette: PaletteName;
  stream_speed: StreamSpeed;
  editor_command?: string;

  concurrency_limit: number;
  synthesis_debounce_ms: number;
  log_retention_days: number;
  feature_flags: FeatureFlags;

  onboarding_complete: boolean;
  update_feed_url?: string;

  // active_profile_id/credential_bindings mirror pkg/contracts/config.go
  // (profiles-v2). credential_bindings holds NAMES only — never values.
  active_profile_id?: string;
  credential_bindings?: string[];

  // env_mode/env_passthrough mirror pkg/contracts/config.go's EnvMode —
  // trust governs what a spawned agent can DO, env_mode governs what it can
  // SEE. env_passthrough holds parent env var NAMES only, consulted only
  // when env_mode is "isolated".
  env_mode?: EnvMode;
  env_passthrough?: string[];

  // platform is the Router host's Go runtime.GOOS ("linux", "darwin", …),
  // stamped onto every Config response by the Router — response-only, never
  // stored (PUT /config drops it). The host is what matters (it runs the
  // pipeline), so never substitute the renderer's navigator.
  platform?: string;
}

export type EnvMode = "inherit" | "isolated";

// FastModelStatus mirrors services/router/internal/api/config.go's
// fastModelStatusResponse: the live effective mode plus, in provider mode,
// whether the resolved default provider actually supports background
// completions (provider.Completer) — the signal the Settings panel needs to
// show the "your provider doesn't support background completions" line.
export interface FastModelStatus {
  mode: FastModelMode;
  provider_name?: string;
  provider_completer: boolean;
}

// mirrors pkg/contracts/profile.go ProfileSnapshot — a sparse, per-field
// capture of Config, keyed by Config's own JSON field names. Present only
// for fields captured at save time.
export type ProfileSnapshot = Record<string, unknown>;

// mirrors pkg/contracts/profile.go Profile
export interface Profile {
  id: string;
  name: string;
  description?: string;
  snapshot: ProfileSnapshot;
  system?: boolean;
  created_at: string;
  updated_at: string;
}

// mirrors providers/provider/matrix.go Behavior/Support/Level (§11 capability
// matrix) — left as plain strings rather than closed unions since the Go
// side is the single source of truth (provider.Behaviors()) and the UI
// renders whatever rows the Router hands back, not a hardcoded list.
export type ProviderBehavior = string;
export type ProviderSupport = "native" | "emulated" | "unsupported";
export type BehaviorLevel = "required" | "expected" | "optional";

// mirrors services/router/internal/api/reads.go providerInfo
export interface ProviderInfo {
  name: string;
  matrix: Record<ProviderBehavior, ProviderSupport>;
  /** Absent when the provider doesn't implement IntentEnforcer — no stance taken on any intent. */
  intents?: IntentMatrix;
}

// mirrors services/router/internal/api/reads.go behaviorInfo
export interface BehaviorInfo {
  behavior: ProviderBehavior;
  level: BehaviorLevel;
}

// GET /credentials/env-candidates — the import picklist plus the allowlist
// that produced it (prefixes as "AWS_*"), so an empty picklist can say what
// was looked for.
export interface EnvCandidates {
  candidates: string[];
  looked_for: string[];
}

// GET /imports/conversations — what POST would import (conversations mads
// doesn't already have): counts by provider and by folder, never the
// conversations themselves. looked_for names the places read, so an empty
// result can say what was looked at.
export interface ConversationImportScan {
  providers: { provider: string; count: number }[];
  /** exists=false: the folder is gone; the conversation is still imported and re-rooted when it first wakes. */
  folders: { dir: string; count: number; exists: boolean }[];
  total: number;
  already_imported: number;
  looked_for: string[];
}

// POST /imports/conversations
export interface ConversationImportResult {
  imported: number;
  skipped_existing: number;
}

// mirrors pkg/preflight/preflight.go Check
export interface PreflightCheck {
  name: string;
  healthy: boolean;
  warning?: boolean;
  detail?: string;
  fix_hint?: string;
  not_installed?: boolean;
  install_url?: string;
  install_command?: string;
  /** A tool mads works without (mergiraf): absent is a warning with install guidance, never a failure. */
  optional?: boolean;
  /** What the optional tool is for, in plain words. */
  purpose?: string;
  /** The version and path found, when present. */
  version?: string;
  path?: string;
}

// Body for POST /agents/generate — moved here from useAgentGeneration.ts so
// client.ts can reference it.
export interface GenerateAgentBody {
  description?: string;
  title?: string;
  session_id?: string;
  task_id?: string;
}

// Response body for POST /capabilities/:id/diagnose — moved here from
// DiagnosePanel.tsx so client.ts can reference it.
export interface DiagnoseResult {
  healthy: boolean;
  reason?: string;
  suggestions: string[];
}

// mirrors services/router/internal/api/version.go VersionInfo
export interface VersionInfo {
  version: string;
}

// mirrors pkg/update/check.go CheckResult (W7.5) — Detail carries a
// human-readable reason when the feed couldn't be reached or isn't
// configured, never an HTTP error (§14.3's "never a hard error" contract).
export interface UpdateCheckResult {
  current_version: string;
  latest_version?: string;
  update_available: boolean;
  notes?: string;
  detail?: string;
}

/**
 * POST /sessions/{id}/model's response (contracts.SetSessionModelResponse).
 * The model is persisted on `session` whatever happens to the live agent, so
 * a later restart always picks up the choice.
 *
 * `queued` is about the process running right now: false means it was cycled
 * onto the new model immediately, true means a turn was in flight and the
 * switch is parked until that turn ends — a model change never interrupts an
 * agent mid-turn. The UI says which of the two happened instead of claiming
 * the switch always took effect.
 */
export type SetSessionModelResponse = {
  session: Session;
  queued: boolean;
};
