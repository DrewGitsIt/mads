/**
 * Display helpers shared by the held list, the composer's held chip and the
 * attached strip on a sent message (docs/HELD-UPDATES-DESIGN.md).
 */

/**
 * The "[turn N] " / "[turns a-b] " lead-in the Router puts on each update
 * when several go in one turn (job.anchorPrefix), so the agent can tell them
 * apart. The UI already shows each update on its own row, so it is dropped
 * for display everywhere — held or attached — and the text reads the same
 * before and after the send.
 */
const ANCHOR = /^\[turns? \d+(?:-\d+)?\] /;

export function displayText(text: string): string {
  return text.replace(ANCHOR, "");
}

/** The update's first line of text, for its collapsed row: markdown's heading and list marks dropped. */
export function summaryLine(text: string): string {
  const first = displayText(text)
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l.length > 0);
  return (first ?? "").replace(/^(#{1,6}\s+|[-*+]\s+|>\s*)/, "");
}

/** The sessions updates came from, in order of first appearance. */
export function sourceNames(updates: { source_name: string }[]): string[] {
  return [...new Set(updates.map((u) => u.source_name || "a linked session"))];
}

export function updatesNoun(n: number): string {
  return `${n} ${n === 1 ? "update" : "updates"}`;
}

/** "· Pipeline Design Study" for one source, "· 3 sessions" past that — the composer chip's tail. */
export function fromLabel(names: string[]): string {
  return names.length === 1 ? names[0] : `${names.length} sessions`;
}
