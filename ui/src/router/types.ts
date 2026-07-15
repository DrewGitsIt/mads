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
  // Added for the Files tab's merge-back flow (W6.5) — the "open worktree
  // in external editor" action needs the isolated worktree's absolute path,
  // which the conflict payload itself doesn't carry.
  worktree_dir?: string;
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
  change: ChangeKind;
  origin: ChangeOrigin;
  attributed: boolean;
  actor?: string;
  timestamp: string; // ISO
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
