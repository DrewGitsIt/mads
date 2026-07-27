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
  /** Absolute directory the agent runs in — the docked shell opens here so it shares the agent's context. */
  cwd?: string;
  summary?: string;
  active_task_id?: string;
  tags?: string[];
  retro_id?: string;
  synthesis_mode?: string;
  provider_ref?: string;
  /** Most recent turn's token accounting, persisted by the Runner on every turn end — the context meter's data source at wake time, before this process has seen a turn_end of its own. */
  last_usage?: Usage;
  created_at: string;
  updated_at: string;
}

export interface WorkspaceRepo {
  path: string;
  primary?: boolean;
}

export interface WorkspaceDefaults {
  agent?: string;
  model?: string;
  isolation?: string;
  template?: string;
}

export interface Workspace {
  id: string;
  name: string;
  description?: string;
  repos?: WorkspaceRepo[];
  defaults: WorkspaceDefaults;
  steering_files?: string[];
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
  workspace?: string;
}

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
  created_at: string;
  updated_at: string;
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
export interface TemplateStage {
  name: string;
  agent?: string; // "auto" or a registry agent name; mutually exclusive with command
  command?: string;
  when?: string;
  on_fail?: { loop_back?: string; max_loops?: number; then?: string };
  model?: string; // freeform model id; empty/absent = inherit (agent pref, then provider default)
}

export interface ParsedTemplate {
  name: string;
  stages: TemplateStage[];
}

export type Provider = string;

export interface Model {
  id: string;
  name: string;
  default?: boolean;
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

// mirrors providers/provider/event.go ToolUse
export interface ToolUse {
  id?: string;
  name: string;
  input?: unknown; // json.RawMessage
  path?: string;
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
  role: "user" | "agent" | "tool" | "tool_result";
  text?: string;
  tool?: ToolUse;
  attribution?: Attribution;
  timestamp?: string; // RFC3339, when the provider recorded this entry
  is_error?: boolean; // set on role "tool_result" when the tool call failed
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
export type LinkCadence = "per_turn" | "manual_only";

// mirrors pkg/contracts/link.go Link
export interface Link {
  id: string;
  granting_session_id: string;
  subject_session_id: string;
  grants: Grant[];
  cadence: LinkCadence;
  implicit?: boolean;
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

export type CreateSessionPayload = Partial<Session> & { links?: LinkRequest[] };

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

// mirrors providers/provider/event.go Event (normalized data-plane stream)
export type StreamEvent =
  | { kind: "text"; text: string }
  | { kind: "tool_use"; tool: ToolUse }
  | { kind: "turn_end"; usage?: Usage; interrupted?: boolean }
  | { kind: "error"; text: string }
  | { kind: "pty_output"; text: string }
  | { kind: "tool_result"; tool?: ToolUse; text?: string; is_error?: boolean }
  | { kind: "permission_request"; permission: PermissionRequest }
  | { kind: "compact"; compact: CompactStatus };

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

// mirrors pkg/contracts/session.go MergeResult
export interface MergeResult {
  conflicted: boolean;
  branch?: string;
  files?: string[];
}

// Matches GET /sessions/{id}/commits/{hash}/diff's fileDiff response
// (services/router/internal/api/commit_diff.go). GET /sessions/{id}/changes
// (getFileDiff, working-change diffs) has no Router route yet — see
// knownGapRoutes in route_manifest_test.go — but the shape is shared.
// `language` is a Monaco language id, guessed client-side from the path
// extension — fine to get wrong, it only affects syntax highlighting.
export interface FileDiff {
  path: string;
  language?: string;
  original: string;
  modified: string;
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

  default_workspace?: string;
  default_provider?: string;
  default_trust: SessionTrust;
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

// mirrors pkg/preflight/preflight.go Check
export interface PreflightCheck {
  name: string;
  healthy: boolean;
  detail?: string;
  fix_hint?: string;
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
