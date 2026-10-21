import type { KeyedRow } from "./conversation";
import { takeWords } from "./words";

/**
 * Paced reveal over the keyed list. The text lives in the conversation; the
 * pacer only records how much of each live text row is shown. Only rows that
 * arrived after replay_end (`paced`) are paced — replayed and stored rows
 * show whole. The first paced row not fully shown is the gate: rows after it
 * wait, so a tool card still pops only once the text before it has drained.
 * A user's own message is never held back. A stored row replacing a paced
 * one keeps its key, so its reveal carries on where it was.
 *
 * Pure; the rAF loop lives in useReveal.
 */
export interface RevealState {
  revealed: Record<string, number>;
  /** Fractional word budget carried across ticks (rates below 1 word/frame). */
  carry: number;
}

export function emptyReveal(): RevealState {
  return { revealed: {}, carry: 0 };
}

function isPacedText(r: KeyedRow): boolean {
  return !!r.paced && r.role === "agent" && !r.parent;
}

function shown(p: RevealState, r: KeyedRow): number {
  return p.revealed[r.key] ?? 0;
}

/** The index of the gate row, or -1 when nothing waits. */
function gateIndex(rows: KeyedRow[], p: RevealState): number {
  return rows.findIndex((r) => isPacedText(r) && shown(p, r) < (r.text ?? "").length);
}

/** What to render: paced text cut to what's revealed; rows after the gate held back (but not the user's). */
export function revealedRows(rows: KeyedRow[], p: RevealState): KeyedRow[] {
  const gate = gateIndex(rows, p);
  if (gate < 0) return rows;
  const out = rows.slice(0, gate);
  const g = rows[gate];
  out.push({ ...g, text: (g.text ?? "").slice(0, shown(p, g)) });
  for (const r of rows.slice(gate + 1)) if (r.role === "user") out.push(r);
  return out;
}

export function revealPending(rows: KeyedRow[], p: RevealState): boolean {
  return gateIndex(rows, p) >= 0;
}

/**
 * Reveal up to `words` words (plus the carried fraction). A row is whole —
 * its last word is no fragment — once its item stopped, a later row exists,
 * or `final` (the turn is finishing).
 */
export function revealStep(rows: KeyedRow[], p: RevealState, words: number, final = false): RevealState {
  let gate = gateIndex(rows, p);
  if (gate < 0) return p.carry === 0 ? p : { ...p, carry: 0 };
  let budget = words + p.carry;
  const revealed = { ...p.revealed };
  while (gate >= 0 && gate < rows.length) {
    const r = rows[gate];
    const text = r.text ?? "";
    const at = revealed[r.key] ?? 0;
    if (!isPacedText(r) || at >= text.length) {
      gate++;
      continue;
    }
    const take = Math.floor(budget);
    if (!(take > 0)) break;
    const whole = final || !r.open || gate < rows.length - 1;
    const { end, used } = takeWords(text.slice(at), take, at === 0 || text[at - 1] === "\n", whole);
    revealed[r.key] = at + end;
    budget -= used;
    if (at + end < text.length) break; // out of budget, or holding a fragment / an unclosed fence
    gate++;
  }
  const next = { revealed, carry: 0 };
  // Keep the sub-1 fraction only while text still waits (see streamPacer).
  if (revealPending(rows, next) && Number.isFinite(budget)) next.carry = Math.max(0, budget - Math.floor(budget));
  return next;
}

/** Everything shown now: "Native" speed, Show all, and the fast finish. */
export function revealAll(rows: KeyedRow[], p: RevealState): RevealState {
  const revealed = { ...p.revealed };
  for (const r of rows) if (isPacedText(r)) revealed[r.key] = (r.text ?? "").length;
  return { revealed, carry: 0 };
}

/** Whether at least `n` words wait — "behind the agent" vs keeping up. */
export function pendingWordsAtLeast(rows: KeyedRow[], p: RevealState, n: number): boolean {
  let count = 0;
  for (const r of rows) {
    if (!isPacedText(r)) continue;
    const rest = (r.text ?? "").slice(shown(p, r));
    if (!rest) continue;
    count += takeWords(rest, n - count, false, true).used;
    if (count >= n) return true;
  }
  return false;
}
