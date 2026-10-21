import type { Attribution, QueuedMessage, ToolUse, TranscriptMessage } from "../../router/types";

/**
 * The keyed conversation (docs/CHAT-STREAM-DESIGN.md §5, §6): one ordered
 * list of rows keyed by the provider's opaque item key, built from
 * `/transcript?v=2` pages and `/output?v=2` frames alike. A row is a
 * TranscriptMessage (so the renderer draws live and stored items the same
 * way) plus where it came from. React keys are the row keys: nothing
 * remounts when a row arrives, settles or is replaced by its stored copy.
 *
 * Pure: the fetching and the EventSource live in useKeyedConversation.
 */
export interface KeyedRow extends TranscriptMessage {
  key: string;
  /** Built from stream frames and not (yet) replaced by the stored row. */
  live?: boolean;
  /** A text or thinking item still streaming (item_start seen, no item_stop). */
  open?: boolean;
  /** The agent will never store it (the `unsaved` frame): kept as streamed. */
  unsaved?: boolean;
  /** A send that failed: its user_message key came back `unsaved`. */
  failed?: boolean;
  /** A subagent's item: the spawning tool call's id. Not in the main list. */
  parent?: string;
  /** Arrived after replay_end: the pacer reveals it at reading pace. */
  paced?: boolean;
  /**
   * Built from this turn's frames (kept when it settles or its stored row
   * replaces it); cleared when the next turn starts. What the renderer
   * treats as the turn's own: its activity group stays open until the turn
   * ends, then folds.
   */
  current?: boolean;
  /** Built from stream frames in this tab (kept after it settles): its arrival times are the measured ones. */
  streamed?: boolean;
  /**
   * A live row from before a replay of the whole tail (hello `fresh` or
   * `resync`): the replay resends its content, which replaces rather than
   * extends it.
   */
  stale?: boolean;
  /** Set by joinRows on a bubble that joined several text rows: their keys, in order. */
  joined?: string[];
}

export interface TurnState {
  active: boolean;
  startedAt?: string;
  interrupted?: boolean;
}

export interface ConvState {
  rows: Record<string, KeyedRow>;
  /** Keys in transcript order: rows from pages, and live rows the stream said settled. */
  stored: string[];
  /** Keys of live rows, in arrival order; always after `stored`. */
  tail: string[];
  /** Older pages exist before `cursor`. */
  hasMore: boolean;
  /** rows[0].key of the oldest page loaded: the next `before`. */
  cursor?: string;
  epoch?: string;
  /** The last frame id seen (`<epoch>:<seq>`): a reconnect's cursor. */
  lastId?: string;
  /** Between hello and replay_end: frames are the replay, rendered instantly. */
  replaying: boolean;
  /** hello.keyed: false → keys are runner-local; replace at turn end, never merge. */
  keyed: boolean;
  turn: TurnState;
  queued: QueuedMessage[];
}

export function emptyConversation(): ConvState {
  return { rows: {}, stored: [], tail: [], hasMore: false, replaying: false, keyed: true, turn: { active: false }, queued: [] };
}

/** The frame an action carries: the SSE event name, its parsed data, its id line. */
export interface Frame {
  kind: string;
  data: Record<string, unknown>;
  id?: string;
}

export type ConvAction =
  /** The last page (mount, turn end): merged by key. */
  | { type: "page"; rows: TranscriptMessage[]; hasMore: boolean }
  /** An older page (scroll-up): prepended. */
  | { type: "olderPage"; rows: TranscriptMessage[]; hasMore: boolean }
  /**
   * resync / tail_truncated / an unkeyed turn end, applied as one swap so the
   * screen never shows the gap: drop `drop` (every live row when "live"),
   * then merge the page.
   */
  | { type: "swap"; rows: TranscriptMessage[]; hasMore: boolean; drop: "live" | string[] }
  | { type: "frame"; frame: Frame }
  /** Pending queue changes (session.queue_changed). */
  | { type: "queued"; queued: QueuedMessage[] }
  /** The turn state from outside the stream (Stop, the session record). */
  | { type: "turn"; turn: Partial<TurnState> }
  /** Another session: start over. */
  | { type: "reset" };

/** The main list in render order: stored rows, then live ones (subagent rows excluded). */
export function orderedRows(s: ConvState): KeyedRow[] {
  const out: KeyedRow[] = [];
  const seen = new Set<string>();
  for (const k of [...s.stored, ...s.tail]) {
    if (seen.has(k)) continue;
    seen.add(k);
    const r = s.rows[k];
    if (r && !r.parent) out.push(r);
  }
  return out;
}

/** A subagent's rows (by its spawning tool call id), in arrival order. */
export function subagentRows(s: ConvState, parent: string): KeyedRow[] {
  return s.tail.map((k) => s.rows[k]).filter((r): r is KeyedRow => !!r && r.parent === parent);
}

/**
 * Page rows as keyed rows: a tool_result folds into the tool row with its
 * key (a call and its result share one). Rows without a key (an unkeyed
 * provider) get positional keys — stable only within one page, which is all
 * an unkeyed session ever merges.
 */
export function keyPage(rows: TranscriptMessage[]): KeyedRow[] {
  const out: KeyedRow[] = [];
  const byKey = new Map<string, KeyedRow>();
  rows.forEach((m, i) => {
    const key = m.key || `row:${i}`;
    if (m.role === "tool_result") {
      const call = byKey.get(key) ?? (m.tool?.id ? out.find((r) => r.role === "tool" && r.tool?.id === m.tool?.id) : undefined);
      if (call?.tool) {
        call.tool = { ...call.tool, result: { text: m.text, is_error: m.is_error, timestamp: m.timestamp } };
      }
      return; // an orphan result has nothing to attach to
    }
    const row: KeyedRow = { ...m, key };
    out.push(row);
    byKey.set(key, row);
  });
  return out;
}

export function conversationReducer(s: ConvState, a: ConvAction): ConvState {
  switch (a.type) {
    case "page":
      return mergePage(s, keyPage(a.rows), a.hasMore);
    case "olderPage":
      return prependPage(s, keyPage(a.rows), a.hasMore);
    case "swap": {
      const dropped = a.drop === "live" ? new Set(s.tail.filter((k) => s.rows[k]?.live)) : new Set(a.drop);
      const rows = { ...s.rows };
      for (const k of dropped) delete rows[k];
      const next = {
        ...s,
        rows,
        stored: s.stored.filter((k) => !dropped.has(k)),
        tail: s.tail.filter((k) => !dropped.has(k)),
      };
      return mergePage(next, keyPage(a.rows), a.hasMore);
    }
    case "frame":
      return applyFrame(s, a.frame);
    case "queued":
      return { ...s, queued: a.queued };
    case "turn": {
      const next = a.turn.active && !s.turn.active ? startTurn(s) : s;
      return { ...next, turn: { ...next.turn, ...a.turn } };
    }
    case "reset":
      return emptyConversation();
  }
}

/**
 * Merge the last page. A row whose key is rendered live replaces it in place
 * (same key, the stored content, still `paced` so a reveal in progress
 * finishes); a new key is inserted in page order. Stored rows ahead of the
 * page's first overlap stay ahead of it; stored rows the page doesn't have
 * yet (settled after it was read) stay after it. A page that overlaps
 * nothing loaded is a new history (a gap, or the first page): it replaces
 * the stored rows and resets the paging cursor.
 */
function mergePage(s: ConvState, page: KeyedRow[], hasMore: boolean): ConvState {
  const rows = { ...s.rows };
  const pageKeys = new Set(page.map((r) => r.key));
  const overlap = s.stored.findIndex((k) => pageKeys.has(k));
  let before: string[] = [];
  let after: string[] = [];
  if (overlap >= 0) {
    before = s.stored.slice(0, overlap);
    after = s.stored.slice(overlap).filter((k) => !pageKeys.has(k));
  } else {
    for (const k of s.stored) if (!rows[k]?.live) delete rows[k];
  }
  for (const r of page) {
    const prev = rows[r.key];
    const merged: KeyedRow = { ...r };
    if (prev?.paced) merged.paced = true;
    if (prev?.current) merged.current = true;
    // The same send, stored: its failure (if any) stays shown.
    if (prev?.failed) merged.failed = true;
    if (prev?.streamed) merged.streamed = true;
    if (prev?.tool && merged.tool) {
      merged.tool = withLiveDisplay(merged.tool, prev.tool, !!prev.streamed);
      // A call watched live keeps its measured duration: stores stamp
      // coarser (goose: whole seconds) than the arrival times.
      if (prev.streamed && prev.timestamp) merged.timestamp = prev.timestamp;
    }
    rows[r.key] = merged;
  }
  const next: ConvState = {
    ...s,
    rows,
    stored: [...before, ...page.map((r) => r.key), ...after],
    tail: s.tail.filter((k) => !pageKeys.has(k)),
  };
  if (before.length === 0) {
    next.cursor = page[0]?.key;
    next.hasMore = hasMore;
  }
  return next;
}

/**
 * A stored tool call keeps what the live one showed where the store has
 * nothing: an ACP agent's store (goose, hermes) keeps the call's name and
 * input but not the display kind and title the wire carried, and the
 * stored row replacing the live one mustn't change the row at turn end
 * (nor its duration, for a call this tab watched live).
 */
function withLiveDisplay(stored: ToolUse, live: ToolUse, watched: boolean): ToolUse {
  const result = stored.result && watched && live.result?.timestamp ? { ...stored.result, timestamp: live.result.timestamp } : stored.result;
  return {
    ...stored,
    kind: stored.kind ?? live.kind,
    title: stored.title ?? live.title,
    locations: stored.locations ?? live.locations,
    server: stored.server ?? live.server,
    path: stored.path ?? live.path,
    result: result ?? live.result,
  };
}

function prependPage(s: ConvState, page: KeyedRow[], hasMore: boolean): ConvState {
  const rows = { ...s.rows };
  const fresh = page.filter((r) => !rows[r.key]);
  for (const r of fresh) rows[r.key] = r;
  return {
    ...s,
    rows,
    stored: [...fresh.map((r) => r.key), ...s.stored],
    cursor: page[0]?.key ?? s.cursor,
    hasMore,
  };
}

const ITEM_ROLE: Record<string, TranscriptMessage["role"]> = {
  text: "agent",
  thinking: "thinking",
  tool_use: "tool",
  notice: "agent",
};

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** Whether a frame for `key` must be dropped: the stored row has it. */
function storedHas(s: ConvState, key: string): boolean {
  const r = s.rows[key];
  return !!r && !r.live;
}

/**
 * A turn starts (a send, or the first item of one nobody here sent): the
 * previous turn's rows stop being current. A no-op while one already runs.
 */
function startTurn(s: ConvState): ConvState {
  if (s.turn.active) return s;
  const rows = { ...s.rows };
  for (const k of Object.keys(rows)) if (rows[k].current) rows[k] = { ...rows[k], current: false };
  return { ...s, rows, turn: { active: true, startedAt: new Date().toISOString() } };
}

/**
 * A new main-agent row from a live (not replayed) frame while no turn is
 * known to run: a turn this client didn't start (an inbound wake, a
 * compaction pass, another client's send seen only from its output).
 */
function implicitStart(s: ConvState, key: string, parent: unknown): ConvState {
  return s.replaying || s.rows[key] || parent ? s : startTurn(s);
}

/** Add or patch a live row, appending a new key to the tail. */
function upsertLive(s: ConvState, key: string, patch: (prev: KeyedRow | undefined) => KeyedRow): ConvState {
  const prev = s.rows[key];
  const row = prev ? patch(prev) : { ...patch(prev), streamed: true };
  const rows = { ...s.rows, [key]: row };
  return { ...s, rows, tail: prev ? s.tail : [...s.tail, key] };
}

function applyFrame(s0: ConvState, f: Frame): ConvState {
  const s = f.id ? { ...s0, lastId: f.id } : s0;
  const d = f.data;
  const key = str(d.key) ?? "";
  const paced = !s.replaying;
  switch (f.kind) {
    case "hello": {
      const turn = (d.turn ?? {}) as { active?: boolean; started_at?: string; interrupted?: boolean };
      let rows = s.rows;
      if (d.resume !== "resumed") {
        rows = { ...rows };
        for (const k of s.tail) if (rows[k]?.live) rows[k] = { ...rows[k], stale: true };
      }
      return {
        ...s,
        rows,
        epoch: str(d.epoch),
        keyed: d.keyed !== false,
        replaying: true,
        turn: { active: !!turn.active, startedAt: turn.started_at, interrupted: !!turn.interrupted },
        queued: Array.isArray(d.queued) ? (d.queued as QueuedMessage[]) : s.queued,
      };
    }
    case "replay_end": {
      const rows = { ...s.rows };
      for (const k of s.tail) if (rows[k]?.stale) rows[k] = { ...rows[k], stale: false };
      return { ...s, rows, replaying: false };
    }
    case "user_message": {
      if (!key || s.rows[key]) return s;
      // A send (a queue delivery too) opens its turn; a replayed one doesn't —
      // hello.turn already said whether a turn runs.
      const base = s.replaying ? s : startTurn(s);
      return upsertLive(base, key, () => ({
        key,
        role: "user",
        text: str(d.text) ?? "",
        attribution: d.attribution as Attribution | undefined,
        timestamp: str(d.ts),
        live: true,
        current: true,
      }));
    }
    case "item_start": {
      const prev = s.rows[key];
      if (prev?.stale) {
        const reset = { ...prev, stale: false, open: d.item === "text" || d.item === "thinking" };
        if (d.item === "text" || d.item === "thinking") reset.text = "";
        return { ...s, rows: { ...s.rows, [key]: reset } };
      }
      if (!key || prev) return s;
      const item = str(d.item) ?? "text";
      return upsertLive(implicitStart(s, key, d.parent_tool_use_id), key, () => ({
        key,
        role: ITEM_ROLE[item] ?? "agent",
        text: item === "tool_use" ? undefined : "",
        notice: item === "notice" || undefined,
        parent: str(d.parent_tool_use_id),
        open: item === "text" || item === "thinking",
        paced: item === "text" && paced,
        live: true,
        current: true,
      }));
    }
    case "text_delta":
    case "thinking_delta": {
      if (!key || storedHas(s, key)) return s;
      const text = str(d.text) ?? "";
      return upsertLive(implicitStart(s, key, undefined), key, (prev) =>
        prev
          ? { ...prev, stale: false, text: (prev.stale ? "" : (prev.text ?? "")) + text }
          : {
              key,
              role: f.kind === "text_delta" ? "agent" : "thinking",
              text,
              open: true,
              paced: f.kind === "text_delta" && paced,
              live: true,
              current: true,
            },
      );
    }
    case "item_stop": {
      const r = s.rows[key];
      if (!r?.open) return s;
      return { ...s, rows: { ...s.rows, [key]: { ...r, open: false } } };
    }
    case "tool_use": {
      if (!key || storedHas(s, key)) return s;
      const tool = d.tool as ToolUse | undefined;
      if (!tool) return s;
      return upsertLive(implicitStart(s, key, undefined), key, (prev) => ({
        ...(prev ?? { key, role: "tool", live: true, current: true, timestamp: new Date().toISOString() }),
        role: "tool",
        timestamp: prev?.timestamp ?? new Date().toISOString(),
        tool: { ...tool, result: prev?.tool?.result },
      }));
    }
    case "tool_result": {
      if (!key || storedHas(s, key)) return s;
      const tool = d.tool as ToolUse | undefined;
      return upsertLive(s, key, (prev) => {
        const base = prev?.tool ?? tool ?? { name: "" };
        return {
          ...(prev ?? { key, role: "tool", live: true, current: true }),
          role: "tool",
          tool: {
            ...base,
            ...(tool && !prev?.tool ? tool : {}),
            result: { text: str(d.text), is_error: !!d.is_error, timestamp: new Date().toISOString() },
          },
        };
      });
    }
    case "notice": {
      if (!key || s.rows[key]) return s;
      return upsertLive(s, key, () => ({ key, role: "agent", notice: true, text: str(d.text) ?? "", live: true, current: true }));
    }
    case "inbound": {
      if (!key || s.rows[key]) return s;
      return upsertLive(implicitStart(s, key, undefined), key, () => ({
        key,
        role: "inbound",
        text: str(d.text) ?? "",
        inbound: d.inbound as TranscriptMessage["inbound"],
        timestamp: str(d.timestamp) ?? new Date().toISOString(),
        live: true,
        current: true,
      }));
    }
    case "settled": {
      const keys = (d.keys as string[] | undefined) ?? [];
      const settled = s.tail.filter((k) => keys.includes(k));
      if (settled.length === 0) return s;
      const rows = { ...s.rows };
      for (const k of settled) rows[k] = { ...rows[k], live: false, open: false };
      const moved = settled.filter((k) => !rows[k].parent);
      return {
        ...s,
        rows,
        stored: [...s.stored, ...moved.filter((k) => !s.stored.includes(k))],
        tail: s.tail.filter((k) => !moved.includes(k)),
      };
    }
    case "unsaved": {
      const keys = (d.keys as string[] | undefined) ?? [];
      const rows = { ...s.rows };
      let changed = false;
      let failed = false;
      for (const k of keys) {
        const r = rows[k];
        if (!r?.live) continue;
        rows[k] = r.role === "user" ? { ...r, failed: true, open: false } : { ...r, unsaved: true, open: false };
        failed ||= r.role === "user";
        changed = true;
      }
      if (!changed) return s;
      // A failed send opened a turn that never ran (the runner sends no
      // turn_end for it): over, unless something streamed after it.
      const main = s.tail.filter((k) => !rows[k]?.parent);
      const lastKey = main[main.length - 1];
      if (failed && !s.replaying && lastKey && rows[lastKey].failed) return { ...s, rows, turn: { active: false } };
      return { ...s, rows };
    }
    case "turn_end": {
      // Every item ends with its turn (the server sends item_stop first; a
      // dropped connection may not have).
      const rows = { ...s.rows };
      for (const k of s.tail) if (rows[k]?.open) rows[k] = { ...rows[k], open: false };
      return { ...s, rows, turn: { active: false } };
    }
    default:
      return s;
  }
}
