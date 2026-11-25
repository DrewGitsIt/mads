// Cross-tree signal for the rail's "N updates waiting": the rail opens the
// session (onSelect) and asks its conversation to bring the held list into
// view. The conversation tab may not be mounted yet — it mounts on the
// selection, and the list renders only once its fetch answers — so the ask
// is parked here for the list to take when it renders, as well as fired as
// a window event for a tab already showing that session. Scoped by session
// id, and short-lived: an ask the list never took (the updates went in the
// meantime) mustn't scroll some later visit.
export const HELD_REVEAL_EVENT = "mads:reveal-held";

/** How long a parked ask stays good: a mount, a transcript page and the held-updates fetch. */
const PARKED_MS = 10_000;

export interface HeldRevealDetail {
  sessionId: string;
}

let parked: { sessionId: string; at: number } | undefined;

export function requestHeldReveal(sessionId: string) {
  parked = { sessionId, at: Date.now() };
  window.dispatchEvent(new CustomEvent<HeldRevealDetail>(HELD_REVEAL_EVENT, { detail: { sessionId } }));
}

/** True (once) when the rail asked to reveal this session's held list recently. */
export function takeHeldReveal(sessionId: string): boolean {
  if (!parked || parked.sessionId !== sessionId || Date.now() - parked.at > PARKED_MS) return false;
  parked = undefined;
  return true;
}
