import type {
  CompactStatus,
  Delegate,
  DelegateState,
  Envelope,
  PermissionRequest,
  SubagentEvent,
  SubagentTask,
  ToolStatus,
  ToolStatusChange,
  ToolUse,
  TranscriptMessage,
} from "../../router/types";
import type { KeyedRow } from "../keyed/conversation";
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
  /** The row's key, in the main conversation (a subagent's stream has none). */
  key?: string;
}

export interface ThinkingEntry {
  type: "thinking";
  text: string;
  timestamp?: string;
  live?: boolean;
  key?: string;
}

export type ActivityEntry = ToolEntry | ThinkingEntry;

export interface MessageEntry {
  type: "message";
  message: TranscriptMessage;
  live?: boolean;
  /** The row's key: the item's React key. */
  key: string;
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
 * Keys are the rows' keys (docs/CHAT-STREAM-DESIGN.md §6): a message's is
 * its row's, a group's its first row's, the plan's and a task card's their
 * call's. So nothing remounts when a row arrives, settles or is replaced by
 * its stored copy, and a group's open/close state survives all three.
 */
export function buildItems(entries: SourceEntry[]): ConversationItem[] {
  const items: ConversationItem[] = [];
  let planItem: Extract<ConversationItem, { type: "plan" }> | undefined;
  let currentGroup: Extract<ConversationItem, { type: "activity" }> | undefined;

  const endGroup = () => {
    currentGroup = undefined;
  };

  entries.forEach((entry, i) => {
    if (entry.type === "card") {
      endGroup();
      items.push({ key: `c-${entry.id}`, type: "card", id: entry.id });
      return;
    }
    if (entry.type === "message") {
      endGroup();
      items.push({ key: entry.key, type: "message", message: entry.message, live: entry.live });
      return;
    }
    const key = entry.key ?? (entry.type === "tool" ? entry.tool.id : undefined) ?? `e-${i}`;
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
            planItem = { key, type: "plan", todos, live: entry.live };
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
          key,
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
      currentGroup = {
        key,
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
 * The keyed rows (joined, failed sends folded) as the source sequence
 * buildItems eats: a tool row (its result attached) and a thinking row are
 * activity entries, everything else a message. Each carries its row's key,
 * and `live` when the row is the current turn's own.
 */
export function rowsToEntries(rows: KeyedRow[]): SourceEntry[] {
  return rows.map((row): SourceEntry => {
    const live = row.current || undefined;
    if (row.role === "tool" && row.tool) {
      return { type: "tool", tool: row.tool, timestamp: row.timestamp, live, key: row.key };
    }
    if (row.role === "thinking") {
      return { type: "thinking", text: row.text ?? "", timestamp: row.timestamp, live, key: row.key };
    }
    return { type: "message", message: row, live, key: row.key };
  });
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
  /** Tools and time: the live run's, from task lines, until mergeDelegates totals them across runs. */
  toolUses?: number;
  durationMs?: number;
  /** A running delegate's earlier runs' time (its record's) — the live clock adds the run's own. */
  priorDurationMs?: number;
  lastToolName?: string;
  status?: string;
  summary?: string;
  /** Live counters still ticking — set only by live "task" stream events, cleared by notification/turn end. */
  running: boolean;
  /** A task_notification arrived — the completed variant with inline summary. */
  completed: boolean;
  /**
   * When the task (its latest run, once a delegate record says so) started —
   * the live elapsed counter's origin. From a replayed lifecycle it is the
   * first event's log stamp (see resumeRunningTasks).
   */
  startedAt?: string;
  /**
   * The server's delegate state (docs/DELEGATES-DESIGN.md), when a record
   * exists — authoritative over everything folded from task lines here:
   * mergeDelegates() sets running/completed from it. Absent for harnesses
   * that keep no record, which fall back to the task-line fold.
   */
  state?: DelegateState;
  /** Whether mads concluded `state` rather than the harness reporting it. */
  inferred?: boolean;
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
  // First seen wins: a resumed delegate's lifecycle lines (a Claude Code
  // relay) carry the resuming SendMessage call's id, while its own events
  // stay tagged with the original spawn — moving the anchor would detach
  // the card from its stream.
  if (task.tool_use_id && !model.toolUseId) model.toolUseId = task.tool_use_id;
  // A progress line's description narrates the current step; letting it
  // overwrite the name would retitle the card on every tick.
  if (task.description) {
    if (task.phase === "progress") model.currentStep = task.description;
    else model.description = task.description;
  }
  if (task.subagent_type) model.subagentType = task.subagent_type;
  if (task.is_backgrounded !== undefined) model.isBackgrounded = task.is_backgrounded;
  // The harness counts each run's tools and time from zero, and a resumed
  // delegate's start line opens its next run; the earlier runs' totals are
  // its record's (mergeDelegates).
  if (task.phase === "started") {
    model.toolUses = undefined;
    model.durationMs = undefined;
  }
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

/**
 * Lays the server's delegate records over the task-line fold: a record is
 * the one authority on whether a delegate is running and how it ended, so
 * it overrides running/completed (and the turn-edge guesses
 * finishRunningTasks/resumeRunningTasks make) wherever one exists. The
 * anchor and start come from the record; the name, type and current step
 * from the task lines when they have them.
 *
 * Counters: the task lines count the live run from zero; the record totals
 * every run once one ends, and keeps the runs before the live one apart
 * (prior_*). So a running delegate shows its prior runs plus the task lines'
 * live run — the record's own totals only change at an ending — and a
 * finished one shows the record's totals. Tokens are the latest report's
 * either way, never summed.
 */
export function mergeDelegates(
  tasks: ReadonlyMap<string, TaskModel>,
  delegates: ReadonlyMap<string, Delegate>,
): Map<string, TaskModel> {
  const next = new Map(tasks);
  for (const d of delegates.values()) {
    const prev = next.get(d.harness_id) ?? { taskId: d.harness_id, running: false, completed: false };
    next.set(d.harness_id, {
      ...prev,
      toolUseId: d.tool_use_id ?? prev.toolUseId,
      description: prev.description ?? d.description,
      subagentType: prev.subagentType ?? d.type,
      isBackgrounded: prev.isBackgrounded ?? d.backgrounded,
      startedAt: d.started_at,
      state: d.state,
      inferred: d.inferred,
      running: d.state === "running",
      completed: d.state === "completed",
      status: d.reason ?? prev.status,
      ...(d.state === "running"
        ? {
            totalTokens: prev.totalTokens ?? d.total_tokens,
            toolUses: withPrior(d.prior_tool_uses, prev.toolUses),
            durationMs: prev.durationMs,
            priorDurationMs: d.prior_duration_ms,
          }
        : {
            totalTokens: d.total_tokens ?? prev.totalTokens,
            toolUses: d.tool_uses ?? prev.toolUses,
            durationMs: d.duration_ms ?? prev.durationMs,
            priorDurationMs: undefined,
          }),
    });
  }
  return next;
}

/** Earlier runs' count plus the live run's; undefined when neither is known. */
function withPrior(prior: number | undefined, live: number | undefined): number | undefined {
  return prior === undefined && live === undefined ? undefined : (prior ?? 0) + (live ?? 0);
}

export function tasksByToolUse(tasks: ReadonlyMap<string, TaskModel>): Map<string, TaskModel> {
  const byTool = new Map<string, TaskModel>();
  for (const t of tasks.values()) if (t.toolUseId) byTool.set(t.toolUseId, t);
  return byTool;
}

// ---------------------------------------------------------------------------
// Subagent streams (dock content) — live-tagged events and persisted
// session.subagent_event envelopes fold into the same shape.

export type SubStreamItem =
  /** The subagent's own words; `timestamp` is when they began (the file's
   * line, or the first delta's arrival/persistence time). */
  | { type: "text"; text: string; timestamp?: string }
  /** Input the subagent was given — its prompt, or a message relayed through
   * its parent. Only the subagent's own transcript has these: the live
   * stream never carries them. */
  | { type: "message"; text: string; timestamp?: string }
  | ActivityEntry;

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
          items[items.length - 1] = { ...last, text: last.text + event.text };
        } else {
          items.push({ type: "text", text: event.text, timestamp: event.timestamp });
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

/**
 * What a conversation reads back from the log when it opens — every one, in
 * pages (getAllEvents). Not the subagents' own streams
 * (session.subagent_event): those are the bulk of a long session's log, and
 * a subagent's dock reads its own transcript file instead, falling back to
 * the log only when there's no file (SUBAGENT_STREAM_EVENT_TYPES). Nor
 * main-agent thinking (session.thinking): the chat shows stored thinking as
 * keyed /transcript rows, and live thinking while it's in the stream's tail.
 */
export const CONVERSATION_EVENT_TYPES = [
  "session.subagent_task",
  "session.tool_status_changed",
  "session.compact",
];

/** The subagents' own streams, read back only for a dock with no file to read. */
export const SUBAGENT_STREAM_EVENT_TYPES = ["session.subagent_event"];

export interface ConversationHistory {
  statuses: Map<string, ToolStatus>;
  tasks: Map<string, TaskModel>;
  subagentStreams: Map<string, SubStreamItem[]>;
  /** The latest compaction phase in the log, with when it was logged. */
  compact?: { status: CompactStatus; timestamp: string };
}

export function foldHistoryEvents(envelopes: Envelope[]): ConversationHistory {
  const sorted = [...envelopes].sort((a, b) => a.seq - b.seq);
  let statuses = new Map<string, ToolStatus>();
  let tasks = new Map<string, TaskModel>();
  let subagentStreams = new Map<string, SubStreamItem[]>();
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
      case "session.compact": {
        const p = env.payload as CompactStatus | undefined;
        if (p?.phase === "start" || p?.phase === "done") compact = { status: p, timestamp: env.timestamp };
        break;
      }
    }
  }
  return { statuses, tasks, subagentStreams, compact };
}

/**
 * A subagent's own transcript (GET .../delegates/{harnessId}/transcript) as
 * the same stream the dock folds from live events: the file's agent text,
 * tool calls and results, plus what only the file has — its prompt and any
 * message relayed to it, as `message` items. Timestamps are the harness's
 * own, per line. Thinking never appears: the file keeps it without text.
 */
export function transcriptSubStream(messages: readonly TranscriptMessage[]): SubStreamItem[] {
  const KEY = "";
  let streams = new Map<string, SubStreamItem[]>();
  const push = (item: SubStreamItem) => streams.set(KEY, [...(streams.get(KEY) ?? []), item]);
  for (const m of messages) {
    switch (m.role) {
      case "user":
        if (m.text) push({ type: "message", text: m.text, timestamp: m.timestamp });
        break;
      case "agent":
        // A paragraph per message: the file's text blocks are whole, unlike
        // the live stream's deltas, so they don't run together.
        if (m.text) push({ type: "text", text: m.text, timestamp: m.timestamp });
        break;
      case "tool":
        streams = appendSubagentItem(streams, KEY, { kind: "tool_use", tool: m.tool, timestamp: m.timestamp });
        break;
      case "tool_result":
        streams = appendSubagentItem(streams, KEY, {
          kind: "tool_result",
          text: m.text,
          tool: m.tool,
          is_error: m.is_error,
          timestamp: m.timestamp,
        });
        break;
    }
  }
  return streams.get(KEY) ?? [];
}

/**
 * A subagent's live rows (subagentRows: its items from the keyed stream, in
 * arrival order) as the dock's stream. Its text items stay apart — each is
 * one of the subagent's text runs.
 */
export function rowsSubStream(rows: readonly KeyedRow[]): SubStreamItem[] {
  const out: SubStreamItem[] = [];
  for (const r of rows) {
    if (r.role === "tool" && r.tool) out.push({ type: "tool", tool: r.tool, timestamp: r.timestamp, key: r.key });
    else if (r.role === "thinking" && r.text) out.push({ type: "thinking", text: r.text, timestamp: r.timestamp, key: r.key });
    else if (r.role === "agent" && r.text) out.push({ type: "text", text: r.text, timestamp: r.timestamp });
  }
  return out;
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
