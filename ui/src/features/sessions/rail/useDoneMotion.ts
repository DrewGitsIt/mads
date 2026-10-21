import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Session } from "../../../router/types";
import { FOLD_MS } from "../../../components/Motion";
import { usePrefersReducedMotion } from "../../../tasks/GraphView/usePrefersReducedMotion";
import { ASIDE_TAG, isSetAside } from "./railGroups";

/** How long a checked-off card shows its strike before it may fold to Done. */
export const STRIKE_MS = 560;
/** How long a brought-back card shows its lift before it may fold to Active. */
export const LIFT_MS = 320;

/**
 * What a card shows while its ✓ or bring-back is on its way: a line drawn
 * through the name (checking off), or the card lifting out of Done's dim.
 */
export type DoneMark = "strike" | "lift";

interface Move {
  /** Where the user is sending the card: true = Done. */
  done: boolean;
  /** Where the card was showing when they did. */
  fromDone: boolean;
  sentAt: number;
  /**
   * marked: the mark shows and the card holds its place — the server hasn't
   * answered, or it has and the mark's beat isn't over. moving: folding from
   * one list into the other. landed: moved, holding the server's answer
   * until the list's own refetch catches up.
   */
  phase: "marked" | "moving" | "landed";
  /** The record the server answered with, once it accepted the change. */
  confirmed?: Session;
}

export interface DoneMotion {
  /** A ✓ or bring-back request just went out: mark the card, hold it in place. */
  sent: (session: Session, done: boolean) => void;
  /** The server accepted it: the card moves once its mark has been seen. */
  confirmed: (updated: Session, done: boolean) => void;
  /** The server refused it: the mark clears and the card stays where it was. */
  failed: (sessionId: string) => void;
  /** The session list as the rail should place it right now. */
  apply: (sessions: Session[]) => Session[];
  /** Drop landed moves the session list has caught up with. */
  settle: (sessions: Session[]) => void;
  markOf: (sessionId: string) => DoneMark | undefined;
  /** While a card folds across: whether it is leaving Done (else Active). */
  leavingDone: (sessionId: string) => boolean | undefined;
}

function withAside(session: Session, aside: boolean): Session {
  if (isSetAside(session) === aside) return session;
  const tags = (session.tags ?? []).filter((t) => t !== ASIDE_TAG);
  return { ...session, tags: aside ? [...tags, ASIDE_TAG] : tags };
}

/**
 * The ✓ and bring-back as the user sees them: the mark answers the click at
 * once, but the card only changes lists after the server has accepted the
 * change, so the rail never shows a session somewhere the server doesn't
 * have it. The mark's beat usually outlasts the round trip, so the card folds
 * across as soon as the mark has been seen; a slow answer holds the mark
 * longer, and a refusal takes it away with the card still in place.
 *
 * Only these user moves animate. A card that changes lists for any other
 * reason (another window, a Done card that needs attention) moves as it
 * always has.
 */
export function useDoneMotion(): DoneMotion {
  const [moves, setMoves] = useState<ReadonlyMap<string, Move>>(new Map());
  const movesRef = useRef(moves);
  movesRef.current = moves;
  const reduceMotion = usePrefersReducedMotion();
  const timers = useRef(new Map<string, number>());

  const update = useCallback((id: string, fn: (m: Move | undefined) => Move | undefined) => {
    setMoves((prev) => {
      const m = fn(prev.get(id));
      if (m === prev.get(id)) return prev;
      const next = new Map(prev);
      if (m) next.set(id, m);
      else next.delete(id);
      return next;
    });
  }, []);

  const schedule = useCallback((id: string, ms: number, fn: () => void) => {
    window.clearTimeout(timers.current.get(id));
    timers.current.set(id, window.setTimeout(fn, ms));
  }, []);
  const cancel = useCallback((id: string) => {
    window.clearTimeout(timers.current.get(id));
    timers.current.delete(id);
  }, []);

  useEffect(() => {
    const all = timers.current;
    return () => all.forEach((t) => window.clearTimeout(t));
  }, []);

  const sent = useCallback(
    (session: Session, done: boolean) => {
      cancel(session.id);
      const prior = movesRef.current.get(session.id);
      // Where the card shows now: a change that hasn't moved it yet holds it
      // where it was; one that has shows it where it went.
      const fromDone = prior ? (prior.phase === "marked" ? prior.fromDone : prior.done) : isSetAside(session);
      update(session.id, () => ({ done, fromDone, sentAt: Date.now(), phase: "marked" }));
    },
    [cancel, update],
  );

  const confirmed = useCallback(
    (updated: Session, done: boolean) => {
      const m = movesRef.current.get(updated.id);
      // A later click has superseded this answer.
      if (!m || m.done !== done || m.phase !== "marked") return;
      if (m.fromDone === done) {
        update(updated.id, (cur) => cur && { ...cur, phase: "landed", confirmed: updated });
        return;
      }
      update(updated.id, (cur) => cur && { ...cur, confirmed: updated });
      const beat = reduceMotion ? 0 : done ? STRIKE_MS : LIFT_MS;
      schedule(updated.id, Math.max(0, m.sentAt + beat - Date.now()), () => {
        update(updated.id, (cur) => cur && { ...cur, phase: "moving" });
        schedule(updated.id, reduceMotion ? 0 : FOLD_MS, () =>
          update(updated.id, (cur) => cur && { ...cur, phase: "landed" }),
        );
      });
    },
    [reduceMotion, schedule, update],
  );

  const failed = useCallback(
    (sessionId: string) => {
      cancel(sessionId);
      update(sessionId, () => undefined);
    },
    [cancel, update],
  );

  const settle = useCallback(
    (sessions: Session[]) => {
      if (movesRef.current.size === 0) return;
      const byId = new Map(sessions.map((s) => [s.id, s]));
      for (const [id, m] of movesRef.current) {
        if (m.phase !== "landed" || !m.confirmed) continue;
        const listed = byId.get(id);
        const caughtUp =
          !listed ||
          Date.parse(listed.updated_at) > Date.parse(m.confirmed.updated_at) ||
          (isSetAside(listed) === m.done && listed.rail_rank === m.confirmed.rail_rank);
        if (caughtUp) {
          cancel(id);
          update(id, (cur) => (cur === m ? undefined : cur));
        }
      }
    },
    [cancel, update],
  );

  return useMemo(() => {
    const apply = (sessions: Session[]) => {
      if (moves.size === 0) return sessions;
      return sessions.map((s) => {
        const m = moves.get(s.id);
        if (!m) return s;
        if (m.phase === "marked") return withAside(s, m.fromDone);
        // Moved: the server's answer (rank included — bring-back puts the
        // card at the top) until the list's refetch has it too.
        return { ...withAside(s, m.done), rail_rank: m.confirmed?.rail_rank ?? s.rail_rank };
      });
    };
    return {
      sent,
      confirmed,
      failed,
      apply,
      settle,
      markOf: (id: string) => {
        const m = moves.get(id);
        if (!m || m.fromDone === m.done || m.phase === "landed") return undefined;
        return m.done ? "strike" : "lift";
      },
      leavingDone: (id: string) => {
        const m = moves.get(id);
        return m?.phase === "moving" ? m.fromDone : undefined;
      },
    };
  }, [moves, sent, confirmed, failed, settle]);
}
