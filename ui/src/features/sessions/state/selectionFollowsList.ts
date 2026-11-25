import { useCallback, useEffect, useMemo, useRef } from "react";
import type { Session } from "../../../router/types";

/**
 * The rail's cards in the order they're drawn, as the user sees them right
 * now: filter chip and search applied, linked subjects under their holder.
 * Done is only what's showing (its section open). SessionRail reports it.
 */
export interface RailVisibleOrder {
  active: string[];
  done: string[];
}

export const EMPTY_ORDER: RailVisibleOrder = { active: [], done: [] };

/**
 * Where the selection goes when the open session `leaving` goes away (archived,
 * deleted, checked off): the next card in its own section of the rail, else the
 * previous one, else nothing (the Launchpad). `gone` are ids that are no
 * longer candidates (the leaving session itself, others deleted with it).
 *
 * Its own section, because checking a card off moves it to Done: "next" should
 * be the next thing still to do, not a finished session. A session the rail
 * isn't showing (filtered out, searched away) has no neighbours: Launchpad.
 */
export function nextSelection(order: RailVisibleOrder, leaving: string, gone: ReadonlySet<string> = new Set()): string | undefined {
  for (const section of [order.active, order.done]) {
    const at = section.indexOf(leaving);
    if (at < 0) continue;
    const ok = (id: string) => id !== leaving && !gone.has(id);
    return section.slice(at + 1).find(ok) ?? section.slice(0, at).reverse().find(ok);
  }
  return undefined;
}

export interface SelectionFollowsList {
  /** SessionRail's onVisibleOrder. */
  reportOrder: (order: RailVisibleOrder) => void;
  /**
   * The open session is going away by the user's own hand: move now, to its
   * neighbour, rather than wait for the list to catch up.
   */
  leave: (sessionId: string) => void;
  /**
   * A selection the user made while the list scope changed under it (mesh mode:
   * All → This device hides a remote session). It stays open, off-list, until
   * the user picks something else: a pane that changes without being asked is
   * worse than one showing a session the list doesn't.
   */
  keepOffList: (sessionId: string | undefined) => void;
}

/**
 * Selection follows the list (SessionsSurface): when the session on screen
 * leaves the list for good, open its neighbour, falling back to the
 * Launchpad. Driven by the list itself, not by any one event, so it covers
 * every way a session can go: the rail's Archive, the workspace menu's Delete,
 * another window or the CLI, a peer device (whose session.deleted never
 * reaches this window), a ✓ Done (through `leave`).
 *
 * Only ids the list has actually held count: a session just created, or a
 * jump target, is selected before its row arrives and must not bounce.
 * Only a settled list counts: a failed refetch says nothing about what exists.
 */
export function useSelectionFollowsList(opts: {
  sessions: Session[];
  loading: boolean;
  error: string | null;
  /** The session on screen: the selected one, or an open delegate's parent. */
  openSessionId: string | undefined;
  /** Moves the selection (undefined: Launchpad). */
  select: (sessionId: string | undefined) => void;
}): SelectionFollowsList {
  const { sessions, loading, error, openSessionId, select } = opts;
  // The last few distinct orders the rail reported, newest last. The rail's
  // effects run before this hook's, so by the time a vanished session is
  // noticed the newest order may already lack it; its neighbours are read
  // from the newest order that still has it.
  const orders = useRef<RailVisibleOrder[]>([EMPTY_ORDER]);
  const seen = useRef(new Set<string>());
  const offList = useRef<string | undefined>(undefined);
  const selectRef = useRef(select);
  selectRef.current = select;

  const leave = useCallback((sessionId: string) => {
    selectRef.current(nextSelection(orderHolding(orders.current, sessionId), sessionId));
  }, []);

  const reportOrder = useCallback((next: RailVisibleOrder) => {
    const last = orders.current[orders.current.length - 1];
    if (sameOrder(last, next)) return;
    orders.current = [...orders.current.slice(-(ORDER_HISTORY - 1)), next];
  }, []);

  const keepOffList = useCallback((sessionId: string | undefined) => {
    offList.current = sessionId;
  }, []);

  // A user pick ends any off-list grace for the previous one.
  useEffect(() => {
    if (openSessionId !== offList.current) offList.current = undefined;
  }, [openSessionId]);

  useEffect(() => {
    if (loading || error) return;
    const present = new Set(sessions.map((s) => s.id));
    for (const id of present) seen.current.add(id);
    const open = openSessionId;
    if (!open || present.has(open) || !seen.current.has(open) || offList.current === open) return;
    seen.current.delete(open);
    const from = orderHolding(orders.current, open);
    // Others that went in the same refetch (a linked group deleted together)
    // aren't candidates either.
    const gone = new Set([...from.active, ...from.done].filter((id) => !present.has(id)));
    selectRef.current(nextSelection(from, open, gone));
  }, [sessions, loading, error, openSessionId]);

  return useMemo(() => ({ reportOrder, leave, keepOffList }), [reportOrder, leave, keepOffList]);
}

const ORDER_HISTORY = 5;

function orderHolding(history: RailVisibleOrder[], id: string): RailVisibleOrder {
  for (let i = history.length - 1; i >= 0; i--) {
    if (history[i].active.includes(id) || history[i].done.includes(id)) return history[i];
  }
  return history[history.length - 1] ?? EMPTY_ORDER;
}

function sameOrder(a: RailVisibleOrder | undefined, b: RailVisibleOrder): boolean {
  return (
    !!a && a.active.length === b.active.length && a.done.length === b.done.length &&
    a.active.every((id, i) => id === b.active[i]) && a.done.every((id, i) => id === b.done[i])
  );
}

/** A getSession failure that means the session doesn't exist (any more). */
export function isGoneError(err: unknown): boolean {
  return err instanceof Error && /^router request failed: 404\b/.test(err.message);
}
