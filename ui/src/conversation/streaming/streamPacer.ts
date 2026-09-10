import type { ToolUse } from "../../router/types";
import type { StreamItem } from "../ChatRenderer/ChatRenderer";

/**
 * Pure state machine for pacing a live turn's render, decoupled from how fast
 * tokens actually arrive off the wire (the "typewriter" pattern every polished
 * chat UI uses — see docs research, Vercel AI SDK smoothStream et al.).
 *
 * Stream events are enqueued in order; `step` reveals a bounded number of
 * characters per tick into `items` (the trailing text run grows char-by-char;
 * a tool card pops for free when the text before it has drained). This keeps
 * the reveal smooth and, because reveals are batched per animation frame rather
 * than per token, also avoids a React re-render on every delta.
 *
 * All functions are pure — the timing/rAF lives in useStreamPacer.
 */

type QueueEntry =
  | { kind: "text"; text: string }
  | { kind: "tool"; tool: ToolUse; timestamp?: string }
  | { kind: "thinking"; text: string };

export interface PacerState {
  /** Revealed items — exactly what ChatRenderer renders for the live turn. */
  items: StreamItem[];
  /** Pending, ordered — not yet revealed. */
  queue: QueueEntry[];
  /** Fractional character budget carried across ticks (rates below 1 char/frame). */
  carry: number;
}

export function emptyPacer(): PacerState {
  return { items: [], queue: [], carry: 0 };
}

/** Append revealed text to the trailing text item, or start a new one (after a tool). */
function reveal(items: StreamItem[], text: string): StreamItem[] {
  const last = items[items.length - 1];
  if (last && last.kind === "text") {
    return [...items.slice(0, -1), { kind: "text", text: last.text + text }];
  }
  return [...items, { kind: "text", text }];
}

/** Queue text — merged into the trailing pending text run so it drains as one. */
export function enqueueText(s: PacerState, text: string): PacerState {
  if (!text) return s;
  const last = s.queue[s.queue.length - 1];
  if (last && last.kind === "text") {
    return { ...s, queue: [...s.queue.slice(0, -1), { kind: "text", text: last.text + text }] };
  }
  return { ...s, queue: [...s.queue, { kind: "text", text }] };
}

/** Queue a tool card at the current position in the stream. `timestamp` is
 * the client-side arrival time — the wire's live frames carry none, and
 * without a start stamp no duration can ever render until the post-turn
 * transcript refetch replaces the live items. */
export function enqueueTool(s: PacerState, tool: ToolUse, timestamp?: string): PacerState {
  return { ...s, queue: [...s.queue, { kind: "tool", tool, timestamp }] };
}

/** Queue a whole thinking block — reveals like a tool (pops for free, never paced char-by-char). */
export function enqueueThinking(s: PacerState, text: string): PacerState {
  return { ...s, queue: [...s.queue, { kind: "thinking", text }] };
}

/** Attach a tool result to its card, whether already revealed or still queued (matched by id). */
export function applyToolResult(
  s: PacerState,
  id: string | undefined,
  result: { text?: string; is_error?: boolean; timestamp?: string },
): PacerState {
  if (!id) return s;
  const patch = (e: QueueEntry): QueueEntry =>
    e.kind === "tool" && e.tool.id === id ? { ...e, tool: { ...e.tool, result } } : e;
  const patchItem = (it: StreamItem): StreamItem =>
    it.kind === "tool" && it.tool.id === id ? { ...it, tool: { ...it.tool, result } } : it;
  return { ...s, items: s.items.map(patchItem), queue: s.queue.map(patch) };
}

/** Total un-revealed text characters still queued — drives backlog-bounded pacing. */
export function pendingChars(s: PacerState): number {
  let n = 0;
  for (const e of s.queue) if (e.kind === "text") n += e.text.length;
  return n;
}

export function hasPending(s: PacerState): boolean {
  return s.queue.length > 0;
}

/**
 * Reveal up to `chars` characters this tick (plus any carried fraction). Tool
 * entries pop for free the moment the text ahead of them has drained, so
 * ordering is preserved without a tool consuming the character budget.
 */
export function step(s: PacerState, chars: number): PacerState {
  if (s.queue.length === 0) return s;
  let budget = chars + s.carry;
  let items = s.items;
  let queue = s.queue;

  while (queue.length > 0) {
    const head = queue[0];
    if (head.kind !== "text") {
      items = [...items, head];
      queue = queue.slice(1);
      continue;
    }
    const take = Math.floor(budget);
    if (!(take > 0)) break;
    if (head.text.length <= take) {
      items = reveal(items, head.text);
      budget -= head.text.length;
      queue = queue.slice(1);
    } else {
      items = reveal(items, head.text.slice(0, take));
      queue = [{ kind: "text", text: head.text.slice(take) }, ...queue.slice(1)];
      budget -= take;
    }
  }

  // Keep the sub-1 fraction only while text still waits; drop it once drained.
  // A non-finite budget (bad rate upstream) must not poison carry for later,
  // legitimate calls — fall back to 0 rather than propagating NaN/Infinity.
  const carry = queue.some((e) => e.kind === "text") && Number.isFinite(budget) ? Math.max(0, budget) : 0;
  return { items, queue, carry };
}

/** Reveal everything immediately — "off" mode and the fast-finish path. */
export function drainAll(s: PacerState): PacerState {
  if (s.queue.length === 0) return s;
  let items = s.items;
  for (const e of s.queue) {
    items = e.kind === "text" ? reveal(items, e.text) : [...items, e];
  }
  return { items, queue: [], carry: 0 };
}
