import type {
  CompactStatus,
  Envelope,
  PermissionRequest,
  SubagentEvent,
  SubagentTask,
  ThinkingBlock,
  ToolStatus,
  ToolStatusChange,
  ToolUse,
  TranscriptMessage,
} from "../../router/types";
import { kindOf } from "./toolDisplay";

/**
 * Conversation-item model (T12/T14/T15/T17): one normalization pass turns
 * the flat message/stream sequence into what the pane actually renders —
 * prose bubbles, always-grouped activity runs (any count ≥ 1), a single
 * in-place-updating plan widget, and Task subagent cards. Live and
 * historical rendering share this exact pass; the only live-only bit is the
 * `live` flag that gates entrance animation and the open-group treatment.
 */

export interface ToolEntry {
  type: "tool";
  tool: ToolUse;
  timestamp?: string;
  live?: boolean;
}

export interface ThinkingEntry {
  type: "thinking";
  text: string;
  timestamp?: string;
  live?: boolean;
}

export type ActivityEntry = ToolEntry | ThinkingEntry;

export interface MessageEntry {
  type: "message";
  message: TranscriptMessage;
  live?: boolean;
}

/**
 * A system card the transcript interleaves by time — Commit/Land results
 * (conversation/Worktree). Only its id travels through here; the renderer
 * looks the card up, so this pass stays ignorant of what cards say.
 */
export interface CardEntry {
  type: "card";
  id: string;
  timestamp?: string;
}

export type SourceEntry = MessageEntry | ActivityEntry | CardEntry;

export interface PlanTodo {
  content: string;
  status: "pending" | "in_progress" | "completed";
}

export type ConversationItem =
  | { key: string; type: "message"; message: TranscriptMessage; live?: boolean }
  | { key: string; type: "activity"; entries: ActivityEntry[]; live?: boolean }
  | { key: string; type: "plan"; todos: PlanTodo[]; live?: boolean }
  | { key: string; type: "task"; tool: ToolUse; timestamp?: string; live?: boolean }
  | { key: string; type: "card"; id: string };

/** Parses a TodoWrite input into plan items; null when the shape is foreign. */
export function parseTodos(input: unknown): PlanTodo[] | null {
  if (!input || typeof input !== "object") return null;
  const todos = (input as { todos?: unknown }).todos;
  if (!Array.isArray(todos)) return null;
  const out: PlanTodo[] = [];
  for (const t of todos) {
    if (!t || typeof t !== "object") return null;
    const content = (t as { content?: unknown }).content;
    const status = (t as { status?: unknown }).status;
    if (typeof content !== "string") return null;
    out.push({
      content,
      status: status === "completed" || status === "in_progress" ? status : "pending",
    });
  }
  return out;
}

function isTaskSpawn(tool: ToolUse): boolean {
  return kindOf(tool) === "agent";
}

function isPlan(tool: ToolUse): boolean {
  return kindOf(tool) === "plan";
}

/**
 * Folds a source sequence into conversation items:
 * - consecutive tool/thinking entries form one activity group (always
 *   grouped, even a single call);
 * - TodoWrite never renders as a row — the first call anchors the plan
 *   widget, every later call updates that same widget in place;
 * - an Agent-kind tool_use renders as a Task subagent card, not a row.
 *
 * Keys are stable across a live-turn's growth and the turn-end transcript
 * reconciliation (keyed by the first tool's id when it has one), so group
 * open/close state survives the refetch.
 */
export function buildItems(entries: SourceEntry[]): ConversationItem[] {
  const items: ConversationItem[] = [];
  let planItem: Extract<ConversationItem, { type: "plan" }> | undefined;
  let currentGroup: Extract<ConversationItem, { type: "activity" }> | undefined;

  const endGroup = () => {
    currentGroup = undefined;
  };

  // Cards don't count toward positional keys, so one arriving mid-history
  // never re-keys (and remounts) everything after it.
  let n = 0;
  entries.forEach((entry) => {
    if (entry.type === "card") {
      endGroup();
      items.push({ key: `c-${entry.id}`, type: "card", id: entry.id });
      return;
    }
    const i = n++;
    if (entry.type === "message") {
      endGroup();
      items.push({ key: `m-${i}`, type: "message", message: entry.message, live: entry.live });
      return;
    }
    if (entry.type === "tool") {
      const tool = entry.tool;
      if (isPlan(tool)) {
        const todos = parseTodos(tool.input);
        if (todos) {
          if (planItem) {
            // In-place update: successive TodoWrite calls never move or
            // duplicate the widget, and never break group contiguity.
            planItem.todos = todos;
            if (entry.live) planItem.live = entry.live;
          } else {
            endGroup();
            planItem = { key: `plan-${tool.id ?? i}`, type: "plan", todos, live: entry.live };
            items.push(planItem);
          }
          return;
        }
        // Unparseable plan input falls through to an ordinary row — never
        // silently dropped.
      }
      if (isTaskSpawn(tool)) {
        endGroup();
        items.push({
          key: `task-${tool.id ?? i}`,
          type: "task",
          tool,
          timestamp: entry.timestamp,
          live: entry.live,
        });
        return;
      }
    }
    // tool row or thinking row — join/open the current activity group
    if (!currentGroup) {
      const firstId = entry.type === "tool" ? entry.tool.id : undefined;
      currentGroup = {
        key: `g-${firstId ?? `i${i}`}`,
        type: "activity",
        entries: [],
        live: entry.live,
      };
      items.push(currentGroup);
    }
    currentGroup.entries.push(entry);
    if (entry.live) currentGroup.live = true;
  });

  return items;
}

/**
 * Where each permission card renders. A subagent's call anchors to its
 * spawning Agent's task card (the inner rows only exist in the dock); a
 * main-agent call anchors to its own row, or its task card when the call is
 * itself an Agent spawn. Anything else — no tool_use_id, or a call not
 * rendered (yet) — is unanchored and falls back to the trailing list, so a
 * request is never dropped. Anchored maps key by the anchor's tool_use id.
 */
export function anchorPermissions(
  items: ConversationItem[],
  requests: PermissionRequest[],
): { anchored: Map<string, PermissionRequest[]>; unanchored: PermissionRequest[] } {
  const rendered = new Set<string>();
  for (const item of items) {
    if (item.type === "task" && item.tool.id) rendered.add(item.tool.id);
    if (item.type !== "activity") continue;
    for (const e of item.entries) if (e.type === "tool" && e.tool.id) rendered.add(e.tool.id);
  }
  const anchored = new Map<string, PermissionRequest[]>();
  const unanchored: PermissionRequest[] = [];
  for (const req of requests) {
    const anchor = [req.parent_tool_use_id, req.tool_use_id].find(
      (id) => id !== undefined && rendered.has(id),
    );
    if (anchor) anchored.set(anchor, [...(anchored.get(anchor) ?? []), req]);
    else unanchored.push(req);
  }
  return { anchored, unanchored };
}

/**
 * Merges historical thinking blocks (session.thinking envelopes — the only
 * place claude-code's transcript-omitted thinking survives) into the folded
 * transcript by timestamp, producing the source sequence buildItems eats.
 */
export function mergeTranscript(
  foldedMessages: TranscriptMessage[],
  thinking: { text: string; timestamp?: string }[] = [],
): SourceEntry[] {
  const entries: SourceEntry[] = [];
  const pending = [...thinking];
  const flushBefore = (iso: string) => {
    while (pending.length > 0) {
      const t = pending[0];
      if (t.timestamp && Date.parse(t.timestamp) > Date.parse(iso)) break;
      entries.push({ type: "thinking", text: t.text, timestamp: t.timestamp });
      pending.shift();
    }
  };
  for (const message of foldedMessages) {
    if (message.timestamp) flushBefore(message.timestamp);
    if (message.role === "tool" && message.tool) {
      entries.push({ type: "tool", tool: message.tool, timestamp: message.timestamp });
    } else {
      entries.push({ type: "message", message });
    }
  }
  // Whatever's left (later than every message, or timestamp-less) trails.
  for (const t of pending) entries.push({ type: "thinking", text: t.text, timestamp: t.timestamp });
  return entries;
}

/**
 * Interleaves cards into the source sequence by time: each goes before the
 * first entry stamped later than it, and before any live (in-flight)
 * entry — a card that arrives mid-turn sits above the turn's stream, not
 * inside it. Cards are expected in log order; unstamped ones trail.
 */
export function interleaveCards(
  entries: SourceEntry[],
  cards: { id: string; timestamp?: string }[],
): SourceEntry[] {
  if (cards.length === 0) return entries;
  const pending = [...cards];
  const out: SourceEntry[] = [];
  const stampOf = (e: SourceEntry): string | undefined =>
    e.type === "message" ? e.message.timestamp : e.timestamp;
  for (const entry of entries) {
    const live = entry.type !== "card" && entry.live;
    const ts = stampOf(entry);
    const at = ts ? Date.parse(ts) : NaN;
    while (pending.length > 0) {
      const c = pending[0];
      const cAt = c.timestamp ? Date.parse(c.timestamp) : NaN;
      if (Number.isNaN(cAt)) break;
      if (!live && (Number.isNaN(at) || cAt > at)) break;
      out.push({ type: "card", id: c.id, timestamp: c.timestamp });
      pending.shift();
    }
    out.push(entry);
  }
  for (const c of pending) out.push({ type: "card", id: c.id, timestamp: c.timestamp });
  return out;
}

// ---------------------------------------------------------------------------
// Task subagent lifecycle (T15/T17)

export interface TaskModel {
  taskId: string;
  toolUseId?: string;
  description?: string;
  /** The subagent's latest step ("Searching for …") — a progress line's description, never the task's name. */
  currentStep?: string;
  subagentType?: string;
  isBackgrounded?: boolean;
  totalTokens?: number;
  toolUses?: number;
  durationMs?: number;
  lastToolName?: string;
  status?: string;
  summary?: string;
  /** Live counters still ticking — set only by live "task" stream events, cleared by notification/turn end. */
  running: boolean;
  /** A task_notification arrived — the completed variant with inline summary. */
  completed: boolean;
  /** When the task's first lifecycle event was logged — replay only (see resumeRunningTasks). */
  startedAt?: string;
}

/** Folds one task lifecycle event into the map (immutably — safe as a state update). */
export function applyTaskEvent(
  tasks: ReadonlyMap<string, TaskModel>,
  task: SubagentTask,
  live: boolean,
  at?: string,
): Map<string, TaskModel> {
  const next = new Map(tasks);
  const prev = next.get(task.task_id);
  const model: TaskModel = prev
    ? { ...prev }
    : { taskId: task.task_id, running: false, completed: false, startedAt: at };
  if (task.tool_use_id) model.toolUseId = task.tool_use_id;
  // A progress line's description narrates the current step; letting it
  // overwrite the name would retitle the card on every tick.
  if (task.description) {
    if (task.phase === "progress") model.currentStep = task.description;
    else model.description = task.description;
  }
  if (task.subagent_type) model.subagentType = task.subagent_type;
  if (task.is_backgrounded !== undefined) model.isBackgrounded = task.is_backgrounded;
  if (task.total_tokens !== undefined) model.totalTokens = task.total_tokens;
  if (task.tool_uses !== undefined) model.toolUses = task.tool_uses;
  if (task.duration_ms !== undefined) model.durationMs = task.duration_ms;
  if (task.last_tool_name !== undefined) model.lastToolName = task.last_tool_name;
  if (task.status !== undefined) model.status = task.status;
  if (task.summary !== undefined) model.summary = task.summary;
  switch (task.phase) {
    case "started":
    case "progress":
    case "updated":
      // Only a live stream may claim "running" — a replayed lifecycle with
      // no notification is interrupted-shaped, never a fake spinner (T17).
      model.running = live;
      break;
    case "notification":
      model.running = false;
      model.completed = true;
      break;
  }
  return next.set(task.task_id, model);
}

/**
 * Reopening a conversation mid-turn: replay marks every un-notified task not
 * running (the no-fake-spinner rule above), but a task spawned inside the
 * turn that is still in flight right now genuinely is running. `turnSince`
 * is that turn's start (Session.agent_state.since) — only tasks whose first
 * event is at or after it are revived, so an earlier turn's abandoned task
 * stays interrupted-shaped.
 */
export function resumeRunningTasks(
  tasks: ReadonlyMap<string, TaskModel>,
  turnSince: string,
): Map<string, TaskModel> {
  const since = Date.parse(turnSince);
  const next = new Map(tasks);
  if (Number.isNaN(since)) return next;
  for (const [id, t] of next) {
    if (t.running || t.completed || !t.startedAt) continue;
    if (Date.parse(t.startedAt) >= since) next.set(id, { ...t, running: true });
  }
  return next;
}

/** Turn end with no notification: every still-running task is interrupted, not running. */
export function finishRunningTasks(tasks: ReadonlyMap<string, TaskModel>): Map<string, TaskModel> {
  let changed = false;
  const next = new Map(tasks);
  for (const [id, t] of next) {
    if (t.running) {
      next.set(id, { ...t, running: false, status: t.completed ? t.status : "interrupted" });
      changed = true;
    }
  }
  return changed ? next : new Map(tasks);
}

export function tasksByToolUse(tasks: ReadonlyMap<string, TaskModel>): Map<string, TaskModel> {
  const byTool = new Map<string, TaskModel>();
  for (const t of tasks.values()) if (t.toolUseId) byTool.set(t.toolUseId, t);
  return byTool;
}

// ---------------------------------------------------------------------------
// Subagent streams (dock content) — live-tagged events and persisted
// session.subagent_event envelopes fold into the same shape.

export type SubStreamItem = { type: "text"; text: string } | ActivityEntry;

/** Appends one subagent-tagged event, folding a tool_result into its tool by
 * id. `timestamp` is the event's arrival/persistence time — it becomes the
 * tool entry's start and the folded result's end, which is all the duration
 * rendering has (subagent wire frames carry no timestamps of their own). */
export function appendSubagentItem(
  streams: ReadonlyMap<string, SubStreamItem[]>,
  parentToolUseId: string,
  event: Pick<SubagentEvent, "kind" | "text" | "tool" | "is_error"> & { timestamp?: string },
): Map<string, SubStreamItem[]> {
  const next = new Map(streams);
  const items = [...(next.get(parentToolUseId) ?? [])];
  switch (event.kind) {
    case "text":
      if (event.text) {
        const last = items[items.length - 1];
        if (last && last.type === "text") {
          items[items.length - 1] = { type: "text", text: last.text + event.text };
        } else {
          items.push({ type: "text", text: event.text });
        }
      }
      break;
    case "tool_use":
      if (event.tool) items.push({ type: "tool", tool: event.tool, timestamp: event.timestamp });
      break;
    case "tool_result": {
      const id = event.tool?.id;
      let idx = -1;
      if (id !== undefined) {
        for (let i = items.length - 1; i >= 0; i--) {
          const it = items[i];
          if (it.type === "tool" && it.tool.id === id) {
            idx = i;
            break;
          }
        }
      }
      if (idx !== -1) {
        const owner = items[idx] as ToolEntry;
        items[idx] = {
          ...owner,
          tool: {
            ...owner.tool,
            // Preserve any enriched status/kind the result frame carries.
            ...(event.tool?.status ? { status: event.tool.status } : {}),
            result: { text: event.text, is_error: event.is_error, timestamp: event.timestamp },
          },
        };
      }
      break;
    }
    case "thinking":
      if (event.text) items.push({ type: "thinking", text: event.text });
      break;
  }
  next.set(parentToolUseId, items);
  return next;
}

// ---------------------------------------------------------------------------
// History replay (T17): fold the control-plane event log into the same
// state shapes the live stream feeds.

export const CONVERSATION_EVENT_TYPES = [
  "session.subagent_event",
  "session.subagent_task",
  "session.thinking",
  "session.tool_status_changed",
  "session.compact",
];

export interface ConversationHistory {
  statuses: Map<string, ToolStatus>;
  tasks: Map<string, TaskModel>;
  subagentStreams: Map<string, SubStreamItem[]>;
  thinking: { text: string; timestamp?: string }[];
  /** The latest compaction phase in the log, with when it was logged. */
  compact?: { status: CompactStatus; timestamp: string };
}

export function foldHistoryEvents(envelopes: Envelope[]): ConversationHistory {
  const sorted = [...envelopes].sort((a, b) => a.seq - b.seq);
  let statuses = new Map<string, ToolStatus>();
  let tasks = new Map<string, TaskModel>();
  let subagentStreams = new Map<string, SubStreamItem[]>();
  const thinking: { text: string; timestamp?: string }[] = [];
  let compact: ConversationHistory["compact"];

  for (const env of sorted) {
    switch (env.type) {
      case "session.tool_status_changed": {
        const p = env.payload as ToolStatusChange | undefined;
        if (p?.tool_use_id && p.status) statuses = new Map(statuses).set(p.tool_use_id, p.status);
        break;
      }
      case "session.subagent_task": {
        const p = env.payload as SubagentTask | undefined;
        if (p?.task_id) tasks = applyTaskEvent(tasks, p, false, env.timestamp);
        break;
      }
      case "session.subagent_event": {
        const p = env.payload as SubagentEvent | undefined;
        if (p?.parent_tool_use_id) {
          subagentStreams = appendSubagentItem(subagentStreams, p.parent_tool_use_id, {
            ...p,
            timestamp: env.timestamp,
          });
        }
        break;
      }
      case "session.thinking": {
        const p = env.payload as ThinkingBlock | undefined;
        if (p?.text) thinking.push({ text: p.text, timestamp: env.timestamp });
        break;
      }
      case "session.compact": {
        const p = env.payload as CompactStatus | undefined;
        if (p?.phase === "start" || p?.phase === "done") compact = { status: p, timestamp: env.timestamp };
        break;
      }
    }
  }
  return { statuses, tasks, subagentStreams, thinking, compact };
}

/**
 * The compaction banner a freshly opened conversation should show, from the
 * log's latest session.compact entry — the live "compact" frame that drove
 * it is never replayed. A pass still running shows only while the session is
 * active (a process that died mid-pass never logs its "done"). A failure
 * stands until the user dismisses it, as it does live; `dismissedAt` is when
 * they last did, so an acknowledged failure stays gone. A success is a
 * transient confirmation, never replayed.
 */
export function replayedCompactStatus(
  latest: ConversationHistory["compact"],
  sessionActive: boolean,
  dismissedAt?: string,
): CompactStatus | undefined {
  if (!latest) return undefined;
  const { status, timestamp } = latest;
  if (status.phase === "start") return sessionActive ? status : undefined;
  if (status.success) return undefined;
  if (dismissedAt && Date.parse(timestamp) <= Date.parse(dismissedAt)) return undefined;
  return status;
}
