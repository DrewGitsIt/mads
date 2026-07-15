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
export interface Session {
  id: string;
  name: string;
  activity: "active" | "inactive";
  workspace?: string;
  provider?: string;
  model?: string;
  agent?: string;
  worktree_branch?: string;
  // Already a rendered markdown string (accomplished/decisions/remaining
  // sections omitted when empty — pkg/sessionsummary renders this
  // server-side, so SessionSummaryCard does no section-presence logic).
  summary?: string;
}

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

// mirrors pkg/contracts/event.go Envelope — the wire shape delivered over
// GET /events (SSE `data:` line is exactly this JSON).
export interface Envelope {
  seq: number;
  timestamp: string;
  type: string;
  session_id?: string;
  task_id?: string;
  actor: string;
  payload?: unknown;
}
