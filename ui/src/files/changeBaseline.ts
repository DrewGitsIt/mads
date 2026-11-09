import { useCallback, useSyncExternalStore } from "react";
import type { ChangeBaselineId } from "../router/types";

// The Changes tab's "Compare to" choice, per session, shared with the
// Context Rail's Changes list so the two never disagree about what changed.
// In memory only: a remembered baseline can stop applying (a worktree's base
// branch goes away), and Session start is the safe default on a fresh load.

const chosen = new Map<string, ChangeBaselineId>();
const listeners = new Set<() => void>();

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/** Sets a session's baseline for every view that shows its changes. */
export function setChangeBaseline(sessionId: string, vs: ChangeBaselineId) {
  if ((chosen.get(sessionId) ?? "start") === vs) return;
  if (vs === "start") chosen.delete(sessionId);
  else chosen.set(sessionId, vs);
  listeners.forEach((fn) => fn());
}

/** The session's shared baseline (Session start until a view picks another) and its setter. */
export function useChangeBaseline(sessionId: string): [ChangeBaselineId, (vs: ChangeBaselineId) => void] {
  const vs = useSyncExternalStore(subscribe, () => chosen.get(sessionId) ?? "start");
  const set = useCallback((next: ChangeBaselineId) => setChangeBaseline(sessionId, next), [sessionId]);
  return [vs, set];
}
