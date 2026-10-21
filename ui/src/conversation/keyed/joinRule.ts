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
  let bubble: KeyedRow | null = null;
  for (const r of rows) {
    if (isHidden(r)) continue;
    if (!isAgentText(r)) {
      bubble = null;
      out.push(r);
      continue;
    }
    if (bubble) {
      bubble.text = joinText(bubble.text ?? "", r.text ?? "");
      bubble.joined = [...(bubble.joined ?? [bubble.key]), r.key];
      bubble.live = bubble.live || r.live;
      bubble.open = r.open;
      bubble.unsaved = bubble.unsaved || r.unsaved;
      continue;
    }
    bubble = { ...r };
    out.push(bubble);
  }
  return out;
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
