import { useEffect, useState } from "react";
import type { Session } from "../../../router/types";
import { lastReplyKey, railRankOf } from "./railGroups";

/**
 * The rail is a checklist: an active list and a Done list, and neither order
 * depends on anything a session does by itself. The active list is the user's
 * rank — start time until they drag a card (railRankOf) — so a reply, a
 * status flip or a rename can only ever repaint a card where it already is.
 * That is what retired the old settle-and-hold machinery (a recency order
 * held still while the pointer was on the rail, and a "N sessions moved" row
 * to apply it): with nothing moving cards on their own, there is nothing to
 * hold.
 */

/** The active list: highest rank first, id breaking ties so the order is total. */
export function activeOrder(
  sessions: Session[],
  rankOf: (session: Session) => number = railRankOf,
): Session[] {
  return [...sessions].sort((a, b) => rankOf(b) - rankOf(a) || a.id.localeCompare(b.id));
}

/**
 * The Done list: most recently finished first. The last reply stands in for
 * "finished at" — ✓ puts the session to sleep, so the stamp stops moving the
 * moment it lands here, and a session started weeks ago but finished today
 * sits at the top of the list rather than behind its "Show more".
 */
export function doneOrder<T>(items: T[], sessionOf: (item: T) => Session): T[] {
  const key = (item: T) => Date.parse(lastReplyKey(sessionOf(item))) || 0;
  return [...items].sort(
    (a, b) => key(b) - key(a) || sessionOf(a).id.localeCompare(sessionOf(b).id),
  );
}

/**
 * The rank that drops a card between two neighbours of the active list
 * (either may be absent: the top or the bottom). The midpoint, so a move
 * rewrites only the card that moved. Ranks are Unix-ms sized, so a float64
 * halves the gap between two sessions started a minute apart some 28 times
 * before it runs out of precision.
 */
export function rankBetween(above?: number, below?: number): number {
  if (above !== undefined && below !== undefined) return (above + below) / 2;
  if (below !== undefined) return below + 1;
  if (above !== undefined) return above - 1;
  return Date.now();
}

export interface RailRanks {
  /** The rank to sort by: a move still in flight, else the record's. */
  rankOf: (session: Session) => number;
  /** Move a card: shows at once, persists through `persist`, reverts if that fails. */
  setRank: (session: Session, rank: number) => void;
}

/**
 * Ranks with optimistic moves. A dropped card has to stay where it was
 * dropped while the write is in flight — otherwise it snaps back for a beat
 * and then jumps again when session.updated arrives — so the new rank is held
 * locally until the record agrees, and dropped if the write fails.
 */
export function useRailRanks(
  sessions: Session[],
  persist?: (id: string, rank: number) => Promise<unknown>,
): RailRanks {
  const [pending, setPending] = useState<ReadonlyMap<string, number>>(new Map());

  useEffect(() => {
    if (pending.size === 0) return;
    const byId = new Map(sessions.map((s) => [s.id, s]));
    const settled = [...pending].filter(([id, rank]) => byId.get(id)?.rail_rank === rank || !byId.has(id));
    if (settled.length === 0) return;
    setPending((prev) => {
      const next = new Map(prev);
      for (const [id, rank] of settled) if (next.get(id) === rank) next.delete(id);
      return next;
    });
  }, [sessions, pending]);

  return {
    rankOf: (session) => pending.get(session.id) ?? railRankOf(session),
    setRank: (session, rank) => {
      if (!persist) return;
      setPending((prev) => new Map(prev).set(session.id, rank));
      persist(session.id, rank).catch(() => {
        setPending((prev) => {
          if (prev.get(session.id) !== rank) return prev;
          const next = new Map(prev);
          next.delete(session.id);
          return next;
        });
      });
    },
  };
}
