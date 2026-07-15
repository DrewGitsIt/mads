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

export interface Agent {
  id: string;
  name: string;
  description?: string;
  prompt: string;
  quadrant?: string;
  tags?: string[];
  system?: boolean;
  created_at: string;
  updated_at: string;
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
