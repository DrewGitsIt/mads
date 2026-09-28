import { useRef, useState } from "react";
import { lastReplyKey } from "./railGroups";
import type { Session } from "../../../router/types";

/**
 * The order the rail is currently rendering from — a snapshot, not a live
 * derivation. The rail is ONE flat recency list (mockup §2, option B), so a
 * card's "index" is simply its index here: there is no group membership left
 * for a live state change to relocate it through.
 */
export type SettledOrder = string[];

/**
 * The ordering key: recency of the last REPLY (lastReplyKey — last_turn_at,
 * not updated_at, so a rename or a summary write can't move a card), id as a
 * tiebreak. Deliberately status-free — `activity` and the status line decide
 * how a card is painted, never where it sits.
 */
export function recencyOrder(sessions: Session[]): string[] {
  return [...sessions]
    .sort((a, b) => lastReplyKey(b).localeCompare(lastReplyKey(a)) || a.id.localeCompare(b.id))
    .map((s) => s.id);
}

/**
 * Hold `heldIds` still: drop the ids that are gone (a deleted session leaves at
 * once) and splice genuinely-new ids in at their ranked position (appearing is
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

function clamp(value: number, max: number): number {
  return Math.min(Math.max(value, 0), max);
}

/**
 * Rule 3: the open session never moves. Whatever row the selected card holds
 * now it keeps — every other card reorders around it, so nobody else's churn
 * can displace it.
 */
export function pinSelected(
  pending: SettledOrder,
  held: SettledOrder,
  selectedId?: string,
): SettledOrder {
  if (!selectedId) return pending;
  const at = held.indexOf(selectedId);
  if (at < 0 || !pending.includes(selectedId)) return pending;
  const next = pending.filter((id) => id !== selectedId);
  next.splice(clamp(at, next.length), 0, selectedId);
  return next;
}

/** Length of the longest non-decreasing subsequence (patience sort over the tails). */
function stableRunLength(ranks: number[]): number {
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
  return tails.length;
}

/**
 * How many sessions the pending order would relocate — the ghost row's count.
 * A card counts only if it has to be picked up and re-dropped: the count is the
 * fewest such cards, not every index that shifts, so one session rising to the
 * top reads as "1 session moved", not "8". Newcomers are not counted —
 * appearing is not moving.
 */
export function countMoved(held: SettledOrder, pending: SettledOrder): number {
  const heldIndex = new Map(held.map((id, i) => [id, i]));
  const ranks: number[] = [];
  for (const id of pending) {
    const rank = heldIndex.get(id);
    if (rank !== undefined) ranks.push(rank);
  }
  return ranks.length - stableRunLength(ranks);
}

export interface SettledRailOrder {
  /** The rail's cards, top to bottom — one list. */
  sessions: Session[];
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
 * but only APPLIED at a quiet boundary — the rail is at rest, the pointer
 * leaves it, it loses focus, the search changes, or the user clicks the pending
 * ghost. While the rail is engaged the previous order is held and the caller
 * renders the ghost instead of yanking rows out from under the cursor.
 *
 * Status never sorts. It is not in the ordering key and there is no longer any
 * group for it to move a card between, so a flip can only ever repaint the card
 * where it already is. The filter chips narrow this order; they never re-rank
 * it, which is why they are not a settle boundary.
 */
export function useSettledRailOrder(
  sessions: Session[],
  selectedId?: string,
  filterKey?: string,
): SettledRailOrder {
  const orderRef = useRef<SettledOrder>([]);
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

  // A changed search is a quiet boundary in itself: the list the user is
  // looking at is the one they just asked for, so there is nothing to hold.
  const filterChanged = filterRef.current !== filterKey;
  filterRef.current = filterKey;

  const held = reconcileOrder(orderRef.current, targetIds);
  const pending = pinSelected(targetIds, held, selectedId);
  const moved = countMoved(held, pending);
  const quiet = applyRef.current || filterChanged || !(hoveredRef.current || focusedRef.current);
  applyRef.current = false;

  const order = quiet ? pending : held;
  orderRef.current = order;

  return {
    sessions: order.map((id) => byId.get(id)!),
    pendingMoves: quiet ? 0 : moved,
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
