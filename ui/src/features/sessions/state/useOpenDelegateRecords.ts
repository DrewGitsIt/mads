import { useEffect, useMemo, useState } from "react";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import type { Delegate } from "../../../router/types";

/**
 * The records the rail needs around the delegate open in the main pane, past
 * whatever the fade window kept: the open one itself, the delegates above it
 * (parent_harness_id), and its direct subagents, finished ones included —
 * the rail lists those in full while it is open (DelegateRows' delegateTree).
 *
 * Deliberately not folded into useSessionAgentState: these are a view of
 * one open delegate, so they go when you navigate away, and the long-lived
 * fold keeps answering only to the fade setting. One read of the session's
 * delegates when the open one changes, then the session's
 * session.delegate_changed events laid over it — so a child that finishes
 * after the read doesn't keep spinning from a stale snapshot once the fold
 * has let go of it.
 */
export function useOpenDelegateRecords(
  routerClient: RouterClient,
  open?: { sessionId: string; harnessId: string },
): Delegate[] | undefined {
  const sessionId = open?.sessionId;
  const harnessId = open?.harnessId;
  const [all, setAll] = useState<{ key: string; byId: ReadonlyMap<string, Delegate> }>();
  const key = sessionId && harnessId ? `${sessionId}/${harnessId}` : "";

  useEffect(() => {
    if (!sessionId || !harnessId) return;
    let cancelled = false;
    const k = `${sessionId}/${harnessId}`;
    const put = (list: Delegate[], under: boolean) =>
      setAll((cur) => {
        const next = new Map(cur?.key === k ? cur.byId : []);
        for (const d of list) if (!under || !next.has(d.harness_id)) next.set(d.harness_id, d);
        return { key: k, byId: next };
      });
    // Subscribed before the read, so nothing falls in the gap; the read lands
    // under anything live.
    const unsubscribe = subscribeToEvents({ sessionId, types: ["session.delegate_changed"] }, (env) => {
      const d = env.payload as Delegate | undefined;
      if (d?.harness_id) put([d], false);
    });
    routerClient
      .getSessionDelegates(sessionId)
      .then((list) => {
        if (!cancelled) put(list, true);
      })
      // Best effort: the rail still has what the fade window kept.
      .catch(() => undefined);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient, sessionId, harnessId]);

  return useMemo(
    () => (key && all?.key === key && harnessId ? aroundOpen(all.byId, harnessId) : undefined),
    [key, all, harnessId],
  );
}

/** The open delegate, its ancestors and its direct children, out of the session's records. */
export function aroundOpen(byId: ReadonlyMap<string, Delegate>, harnessId: string): Delegate[] {
  const out = new Map<string, Delegate>();
  let cur = byId.get(harnessId);
  while (cur && !out.has(cur.harness_id)) {
    out.set(cur.harness_id, cur);
    cur = cur.parent_harness_id ? byId.get(cur.parent_harness_id) : undefined;
  }
  for (const d of byId.values()) if (d.parent_harness_id === harnessId) out.set(d.harness_id, d);
  return [...out.values()];
}
