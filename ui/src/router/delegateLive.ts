import type { Delegate, DelegateWaiting } from "./types";

/**
 * Whether a delegate still has work in flight: running, or waiting — its
 * turn ended but subagents or background commands it owns are still going,
 * and it picks up again when they report back (Delegate.waiting_on). Every
 * surface that sorts, counts or fades delegates asks this rather than
 * `state === "running"`: a waiting one is not finished, so it never fades
 * and is never folded away as history.
 */
export function isLiveDelegate(d: Pick<Delegate, "state" | "waiting_on">): boolean {
  return d.state === "running" || d.waiting_on !== undefined;
}

/** Waiting — live, but no tokens being spent until what it waits on reports back. */
export function isWaitingDelegate(d: Pick<Delegate, "state" | "waiting_on">): boolean {
  return d.state !== "running" && d.waiting_on !== undefined;
}

/**
 * What a waiting delegate waits on, in words: "2 subagents and a background
 * command". Counts, never names — the record only carries counts. An empty
 * (or all-zero) value still reads as something, since the record says it is
 * waiting.
 */
export function waitingOnText(w: DelegateWaiting): string {
  const parts: string[] = [];
  const subagents = w.subagents ?? 0;
  const processes = w.processes ?? 0;
  if (subagents > 0) parts.push(`${subagents} ${subagents === 1 ? "subagent" : "subagents"}`);
  if (processes > 0) parts.push(processes === 1 ? "a background command" : `${processes} background commands`);
  return parts.join(" and ") || "its own work";
}
