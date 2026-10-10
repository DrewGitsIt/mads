import type { ToolUse, TranscriptMessage } from "../../router/types";
import type { StreamItem } from "../ChatRenderer/ChatRenderer";

/**
 * Pure state machine for pacing a live turn's render, decoupled from how fast
 * tokens actually arrive off the wire.
 *
 * Stream events are enqueued in order; `step` reveals a number of *words* per
 * tick into `items` — a reading pace, so a burst (a model that thinks, then
 * delivers its whole answer in half a second) plays out at the speed the
 * user chose instead of exploding onto the screen. The trailing text run
 * grows word by word; a fenced code block reveals whole, as one word, since
 * nobody reads code at prose pace; a tool card pops for free once the text
 * before it has drained. Reveals are batched per animation frame rather than
 * per token, which also avoids a React re-render on every delta.
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
  /** Fractional word budget carried across ticks (rates below 1 word/frame). */
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

export function hasPending(s: PacerState): boolean {
  return s.queue.length > 0;
}

// Word segmentation: Intl.Segmenter knows where words end in scripts that
// don't separate them with spaces (Chinese, Japanese, Thai); without it,
// whitespace-separated runs stand in.
const segmenter =
  typeof Intl !== "undefined" && "Segmenter" in Intl ? new Intl.Segmenter(undefined, { granularity: "word" }) : null;

// How far ahead of the reveal point one word lookup scans. A word longer
// than this (a URL, a hash) reveals in window-sized pieces.
const WINDOW = 256;

/**
 * End offset of the next word unit in `text` from `pos`: any leading
 * whitespace/punctuation, the word itself, and punctuation glued to its end
 * ("word," not "word" then ", next"). undefined when no word starts inside
 * the window.
 */
function nextWordEnd(text: string, pos: number): number | undefined {
  const chunk = text.slice(pos, pos + WINDOW);
  let end: number | undefined;
  if (segmenter) {
    for (const seg of segmenter.segment(chunk)) {
      if (end === undefined) {
        if (seg.isWordLike) end = seg.index + seg.segment.length;
        continue;
      }
      // Glue trailing punctuation, but stop at whitespace.
      if (seg.isWordLike || /^\s/.test(seg.segment)) break;
      end = seg.index + seg.segment.length;
    }
  } else {
    const m = /^\s*\S+/.exec(chunk);
    if (m) end = m[0].length;
  }
  return end === undefined ? undefined : pos + end;
}

const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})[^\n]*(\n|$)/;

/**
 * If a fenced code block opens at `pos` (a line start), the end offset of
 * the whole block through its closing fence line — or undefined when the
 * closing fence hasn't arrived yet. Not a block at all → null.
 */
function fenceEnd(text: string, pos: number): number | undefined | null {
  const open = FENCE_OPEN.exec(text.slice(pos));
  if (!open) return null;
  const marker = open[1];
  if (!open[2]) return undefined; // the opening line itself is still arriving
  let from = pos + open[0].length;
  for (;;) {
    const nl = text.indexOf("\n", from);
    const line = text.slice(from, nl === -1 ? text.length : nl);
    const close = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(line);
    if (close && close[1][0] === marker[0] && close[1].length >= marker.length) {
      // A closing line still arriving could yet grow past a bare fence.
      if (nl === -1) return undefined;
      return nl + 1;
    }
    if (nl === -1) return undefined;
    from = nl + 1;
  }
}

/**
 * How much of `text` to reveal for up to `words` word units. `atLineStart`
 * says whether `text` begins a line (what precedes it was empty or ended in
 * a newline). `final` means no more text will join this run — the turn is
 * finishing, or a tool card follows — so a word touching the end of the
 * buffer is whole; otherwise it may be half of a word still streaming in,
 * and is held back.
 */
function takeWords(
  text: string,
  words: number,
  atLineStart: boolean,
  final: boolean,
): { end: number; used: number } {
  let pos = 0;
  let used = 0;
  while (used < words && pos < text.length) {
    const lineStart = pos === 0 ? atLineStart : text[pos - 1] === "\n";
    // Blank-line remainders are free: they carry no words, and consuming
    // them puts the next fence check at a real line start.
    const blank = /^[ \t]*\n/.exec(text.slice(pos));
    if (blank) {
      pos += blank[0].length;
      continue;
    }
    if (lineStart) {
      const fence = fenceEnd(text, pos);
      if (fence !== null) {
        if (fence === undefined) {
          if (final) return { end: text.length, used: used + 1 };
          break; // wait for the closing fence
        }
        pos = fence;
        used++;
        continue;
      }
    }
    const end = nextWordEnd(text, pos);
    if (end === undefined) {
      if (pos + WINDOW < text.length) {
        pos += WINDOW; // a long run with no word in it: reveal a window
        used++;
        continue;
      }
      if (final) pos = text.length;
      break;
    }
    if (end >= text.length && !final) break;
    pos = end;
    used++;
  }
  return { end: pos, used };
}

/** Whether `items` currently ends at a line start (nothing yet, or a newline). */
function endsAtLineStart(items: StreamItem[]): boolean {
  const last = items[items.length - 1];
  return !last || last.kind !== "text" || last.text === "" || last.text.endsWith("\n");
}

/**
 * Whether at least `n` words are queued but not yet revealed — how the UI
 * tells "behind the agent" from "keeping up". Bounded: stops counting at n.
 */
export function pendingWordsAtLeast(s: PacerState, n: number): boolean {
  let count = 0;
  for (const e of s.queue) {
    if (e.kind !== "text") continue;
    count += takeWords(e.text, n - count, false, true).used;
    if (count >= n) return true;
  }
  return false;
}

/**
 * Reveal up to `words` words this tick (plus any carried fraction). Tool
 * entries pop for free the moment the text ahead of them has drained, so
 * ordering is preserved without a tool consuming the budget. `final` marks
 * the stream finished (turn end): the last queued word is whole, not a
 * fragment awaiting its next delta.
 */
export function step(s: PacerState, words: number, final = false): PacerState {
  if (s.queue.length === 0) return s;
  let budget = words + s.carry;
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
    const whole = final || queue.length > 1;
    const { end, used } = takeWords(head.text, take, endsAtLineStart(items), whole);
    if (end > 0) items = reveal(items, head.text.slice(0, end));
    budget -= used;
    if (end >= head.text.length) {
      queue = queue.slice(1);
      continue;
    }
    queue = [{ kind: "text", text: head.text.slice(end) }, ...queue.slice(1)];
    break; // out of budget, or holding a fragment / an unclosed fence
  }

  // Keep the sub-1 fraction only while text still waits; drop it once drained.
  // Whole words left over because the reveal stopped early (holding a
  // fragment, awaiting a fence) are dropped too, or they'd burst out later.
  // A non-finite budget (bad rate upstream) must not poison carry for later,
  // legitimate calls — fall back to 0 rather than propagating NaN/Infinity.
  const carry =
    queue.some((e) => e.kind === "text") && Number.isFinite(budget) ? Math.max(0, budget - Math.floor(budget)) : 0;
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

/**
 * Revealed live items as transcript rows — how a still-revealing turn joins
 * the history when the next turn starts before its reveal finishes. Thinking
 * rows are dropped: history thinking comes from the session's own thinking
 * log, merged by timestamp, and the turn-end reconcile restores the rest.
 */
export function revealedToMessages(items: StreamItem[]): TranscriptMessage[] {
  const out: TranscriptMessage[] = [];
  for (const it of items) {
    if (it.kind === "text") {
      if (it.text) out.push({ role: "agent", text: it.text });
    } else if (it.kind === "tool") {
      out.push({ role: "tool", tool: it.tool, timestamp: it.timestamp });
    }
  }
  return out;
}
