import { useCallback, useEffect, useRef, useState } from "react";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { HeldUpdate } from "../../router/types";

const HELD_EVENT_TYPES = ["session.updated"];
const NONE: HeldUpdate[] = [];

export interface HeldUpdatesState {
  /** Held for the next send, oldest first. */
  held: HeldUpdate[];
  /** Discarded since the last drain, oldest first — Undo can bring them back. */
  discarded: HeldUpdate[];
  /** Discard updates by id: off the list at once, then the Router's answer refetches. */
  discard: (ids: number[]) => Promise<void>;
  restore: (ids: number[]) => Promise<void>;
  refetch: () => void;
}

/**
 * The open session's held link updates (docs/HELD-UPDATES-DESIGN.md),
 * GET /sessions/{id}/held-updates. Asked for only while there is something
 * to show: the record's `waiting_updates` says some are held, or the last
 * answer still listed some (a discarded update leaves the count but stays
 * on the list for Undo). Every change to the mailbox — an update arriving,
 * a discard, a restore, the drain a send does — fires the session's
 * session.updated, which refetches; an empty answer drops the list.
 */
export function useHeldUpdates(
  routerClient: RouterClient,
  sessionId: string,
  { waiting }: { waiting: number },
): HeldUpdatesState {
  const [lists, setLists] = useState<{ held: HeldUpdate[]; discarded: HeldUpdate[] }>({ held: NONE, discarded: NONE });
  const [token, setToken] = useState(0);
  const refetch = useCallback(() => setToken((t) => t + 1), []);
  // What the subscription and the mutators read without re-subscribing.
  const latest = useRef({ waiting, listed: false });
  latest.current = { waiting, listed: lists.held.length + lists.discarded.length > 0 };
  // Only the newest request's answer lands: a slow one can't undo a newer.
  const seq = useRef(0);

  const load = useCallback(() => {
    const n = ++seq.current;
    routerClient.getHeldUpdates(sessionId).then(
      (next) => {
        if (n === seq.current) setLists({ held: next.held, discarded: next.discarded });
      },
      () => {}, // a Router without the route (or a blip) leaves the last answer standing
    );
  }, [routerClient, sessionId]);

  useEffect(() => {
    seq.current++;
    setLists({ held: NONE, discarded: NONE });
  }, [routerClient, sessionId]);

  useEffect(() => {
    if (latest.current.waiting > 0 || latest.current.listed) load();
  }, [load, waiting, token]);

  useEffect(
    () =>
      subscribeToEvents({ sessionId, types: HELD_EVENT_TYPES }, () => {
        if (latest.current.waiting > 0 || latest.current.listed) load();
      }),
    [sessionId, load],
  );

  const move = useCallback(
    async (ids: number[], discard: boolean) => {
      const pick = new Set(ids);
      seq.current++; // an answer already in flight predates this
      setLists((cur) => {
        const [from, to] = discard ? [cur.held, cur.discarded] : [cur.discarded, cur.held];
        const moved = from.filter((u) => pick.has(u.id));
        const rest = from.filter((u) => !pick.has(u.id));
        const next = [...to, ...moved].sort(byAge);
        return discard ? { held: rest, discarded: next } : { held: next, discarded: rest };
      });
      try {
        await (discard ? routerClient.discardHeldUpdates(ids) : routerClient.restoreHeldUpdates(ids));
      } finally {
        load();
      }
    },
    [routerClient, load],
  );
  const discard = useCallback((ids: number[]) => move(ids, true), [move]);
  const restore = useCallback((ids: number[]) => move(ids, false), [move]);

  return { held: lists.held, discarded: lists.discarded, discard, restore, refetch };
}

function byAge(a: HeldUpdate, b: HeldUpdate): number {
  return a.created_at.localeCompare(b.created_at) || a.id - b.id;
}
