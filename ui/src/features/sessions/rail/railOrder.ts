import { useRef, useState } from "react";
import type { Session } from "../../../router/types";

export type RailGroupName = "active" | "recent";

/**
 * The order the rail is currently rendering from — a snapshot, not a live
 * derivation. `ids` is the flat sequence; `groups` records which group each id
 * was settled into, so a session flipping active/inactive repaints its card
 * without relocating it.
 */
export interface SettledOrder {
  ids: string[];
  groups: Record<string, RailGroupName>;
}

/**
 * The ordering key: recency, id as a tiebreak. Deliberately status-free —
 * `activity` decides how a card is painted and which group it settles into,
 * never where it sits.
 */
export function recencyOrder(sessions: Session[]): string[] {
  return [...sessions]
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id))
    .map((s) => s.id);
}

export function groupOf(session: Session): RailGroupName {
  return session.activity === "active" ? "active" : "recent";
}

/**
 * Hold `heldIds` still: drop the ids that are gone (a deleted session leaves at
 * once) and splice genuinely-new ids in at their sorted position (appearing is
 * not moving). Every surviving id keeps the neighbour it already had.
 */
export function reconcileOrder(heldIds: string[], targetIds: string[]): string[] {
  const target = new Set(targetIds);
  const held = new Set(heldIds);
  const next = heldIds.filter((id) => target.has(id));
  targetIds.forEach((id, i) => {
    if (held.has(id)) return;
    // Sit directly under the nearest session that already outranks it, so
    // nothing above the newcomer shifts and nothing below it shifts by more
    // than the one row it occupies.
    let at = 0;
    for (let above = i - 1; above >= 0; above--) {
      const anchor = next.indexOf(targetIds[above]);
      if (anchor >= 0) {
        at = anchor + 1;
        break;
      }
    }
    next.splice(at, 0, id);
  });
  return next;
}

/** Rule 3: the open session never moves. Whatever index it holds now it keeps, settle or no settle, until the selection changes. */
export function pinSelected(ids: string[], heldIds: string[], selectedId?: string): string[] {
  if (!selectedId) return ids;
  const pinned = heldIds.indexOf(selectedId);
  if (pinned < 0 || ids.indexOf(selectedId) === pinned) return ids;
  const next = ids.filter((id) => id !== selectedId);
  next.splice(Math.min(pinned, next.length), 0, selectedId);
  return next;
}

/**
 * How many sessions the pending order would relocate — the ghost row's count.
 * The fewest cards that have to be picked up and re-dropped (length minus the
 * longest run that is already in the right relative order), not every index
 * that shifts: one session rising to the top is "1 session moved", not "8".
 */
export function countMoved(heldIds: string[], nextIds: string[]): number {
  const held = new Map(heldIds.map((id, i) => [id, i]));
  const ranks = nextIds.map((id) => held.get(id)).filter((rank) => rank !== undefined);
  // Patience sort: tails[k] is the smallest rank ending an increasing run of k + 1.
  const tails: number[] = [];
  for (const rank of ranks) {
    let lo = 0;
    let hi = tails.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (tails[mid] <= rank) lo = mid + 1;
      else hi = mid;
    }
    tails[lo] = rank;
  }
  return ranks.length - tails.length;
}

export interface SettledRailOrder {
  active: Session[];
  recent: Session[];
  /** > 0 while a recomputed order is waiting for a quiet boundary. */
  pendingMoves: number;
  /** Apply the pending order now — the ghost row's click handler. */
  settle: () => void;
  railHandlers: {
    onMouseEnter: () => void;
    onMouseLeave: () => void;
    onFocus: () => void;
    onBlur: (event: React.FocusEvent<HTMLElement>) => void;
  };
}

/**
 * Settled rail order. A new ordering is computed on every session-list change
 * but only APPLIED at a quiet boundary — the pointer leaves the rail, the rail
 * loses focus, the search/filter changes, or the user clicks the pending ghost.
 * While the rail is engaged the previous order is held and the caller renders
 * the ghost instead of yanking rows out from under the cursor.
 */
export function useSettledRailOrder(
  sessions: Session[],
  selectedId?: string,
  filterKey?: string,
): SettledRailOrder {
  const orderRef = useRef<SettledOrder>({ ids: [], groups: {} });
  const hoveredRef = useRef(false);
  const focusedRef = useRef(false);
  const applyRef = useRef(false);
  const filterRef = useRef(filterKey);
  const [, bump] = useState(0);

  function settle() {
    applyRef.current = true;
    bump((n) => n + 1);
  }

  const byId = new Map(sessions.map((s) => [s.id, s]));
  const targetIds = recencyOrder(sessions);
  const previous = orderRef.current;

  // A changed search/filter is a quiet boundary in itself: the list the user is
  // looking at is the one they just asked for, so there is nothing to hold.
  const filterChanged = filterRef.current !== filterKey;
  filterRef.current = filterKey;

  const held = reconcileOrder(previous.ids, targetIds);
  const pending = pinSelected(targetIds, held, selectedId);
  const moved = countMoved(held, pending);
  const quiet =
    applyRef.current || filterChanged || !(hoveredRef.current || focusedRef.current);
  applyRef.current = false;

  // Group membership is part of the order (it is where the card sits), so it is
  // snapshotted with it: a settle refreshes every group, a held render keeps the
  // ones it had and only assigns a group to sessions it has not seen before.
  // Status alone therefore never relocates a card — and because the runner bumps
  // updated_at on an activity flip, the group heals at that change's own settle.
  const settling = quiet && moved > 0;
  const ids = settling ? pending : held;
  const groups: Record<string, RailGroupName> = {};
  for (const id of ids) {
    const previousGroup = previous.groups[id];
    groups[id] = settling || previousGroup === undefined ? groupOf(byId.get(id)!) : previousGroup;
  }
  orderRef.current = { ids, groups };

  const inGroup = (group: RailGroupName) =>
    ids.filter((id) => groups[id] === group).map((id) => byId.get(id)!);

  return {
    active: inGroup("active"),
    recent: inGroup("recent"),
    pendingMoves: settling ? 0 : moved,
    settle,
    railHandlers: {
      onMouseEnter: () => {
        hoveredRef.current = true;
      },
      onMouseLeave: () => {
        hoveredRef.current = false;
        settle();
      },
      onFocus: () => {
        focusedRef.current = true;
      },
      onBlur: (event) => {
        // Focus moving between two cards is not the rail losing focus.
        if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
        focusedRef.current = false;
        settle();
      },
    },
  };
}
