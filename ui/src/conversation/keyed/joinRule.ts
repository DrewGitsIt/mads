import type { KeyedRow } from "./conversation";

/**
 * The join rule (docs/CHAT-STREAM-DESIGN.md §6): adjacent visible text items
 * render as one bubble, whatever message or item they came from. They join
 * as-is when the earlier text ends with a newline, else with a blank line.
 * Any visible item between them (a tool call, thinking with text, a notice,
 * a user message) ends the bubble; hidden items (empty thinking) don't, and
 * are dropped. The same function runs on live and stored rows, so a turn
 * reads the same streaming and settled.
 */
export function joinRows(rows: KeyedRow[]): KeyedRow[] {
  const out: KeyedRow[] = [];
  let parts: KeyedRow[] = [];
  const flush = () => {
    if (parts.length > 0) out.push(joinBubble(parts));
    parts = [];
  };
  for (const r of rows) {
    if (isHidden(r)) continue;
    if (!isAgentText(r)) {
      flush();
      out.push(r);
      continue;
    }
    parts.push(r);
  }
  flush();
  return out;
}

/**
 * The bubbles joined last, by their first row: a bubble whose rows are the
 * same objects as last time is the same object as last time, so what
 * renders it can tell it didn't change (docs/CHAT-STREAM-PER-BATCH-COST.md).
 * Rows are immutable (the keyed reducer replaces a row it changes), so the
 * same rows always join to the same bubble.
 */
const joined = new WeakMap<KeyedRow, { parts: KeyedRow[]; bubble: KeyedRow }>();

/** One bubble from a run of adjacent text rows: the row itself when it's alone. */
function joinBubble(parts: KeyedRow[]): KeyedRow {
  const [first] = parts;
  if (parts.length === 1) return first;
  const hit = joined.get(first);
  if (hit && hit.parts.length === parts.length && hit.parts.every((r, i) => r === parts[i])) return hit.bubble;
  const bubble: KeyedRow = { ...first, joined: [first.key] };
  for (const r of parts.slice(1)) {
    bubble.text = joinText(bubble.text ?? "", r.text ?? "");
    bubble.joined!.push(r.key);
    bubble.live = bubble.live || r.live;
    bubble.open = r.open;
    bubble.unsaved = bubble.unsaved || r.unsaved;
  }
  joined.set(first, { parts, bubble });
  return bubble;
}

/** The agent's own prose: not a notice, not the harness's error, not a subagent's. */
export function isAgentText(r: KeyedRow): boolean {
  return r.role === "agent" && !r.notice && !r.is_error && !r.parent;
}

/** Rows nothing renders: thinking with no text (redacted, signature-only, not yet streamed). */
export function isHidden(r: KeyedRow): boolean {
  return r.role === "thinking" && !(r.text ?? "").trim();
}

export function joinText(a: string, b: string): string {
  if (!a) return b;
  if (!b) return a;
  return a.endsWith("\n") ? a + b : `${a}\n\n${b}`;
}
