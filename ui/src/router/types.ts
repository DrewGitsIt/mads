// Hand-written mirrors of pkg/contracts wire types actually consumed by the
// UI. Field names stay snake_case to match the JSON the Go services emit —
// no camelCase translation layer for this first pass.

export type SessionActivity = "active" | "inactive";
export type SessionTrust = "full" | "limited" | "read_only";

export interface Session {
  id: string;
  name: string;
  activity: SessionActivity;
  workspace?: string;
  provider?: string;
  model?: string;
  agent?: string;
  isolation?: string;
  trust?: SessionTrust;
  worktree_dir?: string;
  worktree_branch?: string;
  summary?: string;
  active_task_id?: string;
  linked_leader_id?: string;
  tags?: string[];
  retro_id?: string;
  synthesis_mode?: string;
  provider_ref?: string;
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

// pkg/template's parsed grammar, frontend-only — this is YAML the Library's
// Templates shelf parses client-side for the visual editor view (no Go JSON
// tags to mirror; pkg/template only ever round-trips YAML).
export interface TemplateStage {
  name: string;
  agent?: string; // "auto" or a registry agent name; mutually exclusive with command
  command?: string;
  when?: string;
  on_fail?: { loop_back?: string; max_loops?: number; then?: string };
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
  name: string;
  input?: unknown; // json.RawMessage
  path?: string;
}

// mirrors providers/provider/event.go Usage
export interface Usage {
  input_tokens: number;
  output_tokens: number;
  model?: string;
}

// mirrors providers/provider/event.go Message (TranscriptReader shape)
export interface TranscriptMessage {
  role: "user" | "agent" | "tool";
  text?: string;
  tool?: ToolUse;
}

// mirrors providers/provider/event.go Event (normalized data-plane stream)
export type StreamEvent =
  | { kind: "text"; text: string }
  | { kind: "tool_use"; tool: ToolUse }
  | { kind: "turn_end"; usage?: Usage; interrupted?: boolean }
  | { kind: "error"; text: string };

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
}

// mirrors pkg/contracts/session.go MergeResult
export interface MergeResult {
  conflicted: boolean;
  branch?: string;
  files?: string[];
}

// mirrors pkg/contracts/event.go Envelope (subset fields the Files tab reads).
// `type` is intentionally `string`, not a closed union — the vocabulary
// grows as services land (see event.go's own comment) and this tab only
// ever branches on the three merge.* values it cares about.
export interface ControlEvent {
  seq: number;
  timestamp: string;
  type: string;
  session_id?: string;
  task_id?: string;
  actor: string;
  payload?: unknown;
}

// Frontend-only — NO Go equivalent exists anywhere (ui/src/files/HANDOFF.md
// §1: no diff-content endpoint at all). Flag for whoever builds GET
// .../diff to reconcile field names. `language` is a Monaco language id,
// guessed client-side from the path extension — fine to get wrong, it only
// affects syntax highlighting.
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
export type PipelineRunOutcome = "completed" | "incomplete" | "cancelled" | "failed" | "running";

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
export type PaletteName = "clay" | "space" | "glacier";

export interface FeatureFlags {
  multi_repo: boolean;
  linked_sessions: boolean;
  remote_registry: boolean;
}

export interface Config {
  synthesis_mode: SynthesisMode;
  agent_selection_mode: AgentSelectionMode;
  default_model_tier: ModelTier;
  cost_alert_cents?: number;

  default_workspace?: string;
  default_provider?: string;
  default_trust: SessionTrust;
  default_template?: string;
  fast_model?: string;

  display_mode: string;
  keybindings: string;
  theme: ThemeMode;
  palette: PaletteName;
  editor_command?: string;

  concurrency_limit: number;
  synthesis_debounce_ms: number;
  log_retention_days: number;
  feature_flags: FeatureFlags;

  onboarding_complete: boolean;
}

// mirrors pkg/contracts/profile.go ProfileSettings — only the fields a
// profile can override; zero/absent means "leave the current config alone".
export interface ProfileSettings {
  synthesis_mode?: SynthesisMode;
  agent_selection_mode?: AgentSelectionMode;
  default_model_tier?: ModelTier;
  default_workspace?: string;
  default_provider?: string;
  default_trust?: SessionTrust;
  default_template?: string;
  fast_model?: string;
  concurrency_limit?: number;
}

// mirrors pkg/contracts/profile.go Profile
export interface Profile {
  id: string;
  name: string;
  description?: string;
  settings: ProfileSettings;
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
