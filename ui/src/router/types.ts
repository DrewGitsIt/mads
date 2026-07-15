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

export type TaskStatus =
  | "ready"
  | "in_progress"
  | "blocked"
  | "needs_review"
  | "done"
  | "wont_do";

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
