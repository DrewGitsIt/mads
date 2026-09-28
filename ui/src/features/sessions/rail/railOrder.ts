import { useRef, useState } from "react";
import type { Session } from "../../../router/types";

export type RailGroupName = "active" | "recent";

/**
 * The order the rail is currently rendering from — a snapshot, not a live
 * derivation. The rail draws Active above Recent, so which group a card settled
 * into is *part of its position*: both blocks are held together, and a card's
 * index is its index in `renderSequence(order)`.
 */
export interface SettledOrder {
  active: string[];
  recent: string[];
}

/** The rail's top-to-bottom card sequence. A card's "index" always means its index here. */
export function renderSequence(order: SettledOrder): string[] {
  return [...order.active, ...order.recent];
}

/**
 * The ordering key: recency, id as a tiebreak. Deliberately status-free —
 * `activity` decides how a card is painted and which block it settles into,
 * never its rank within a block.
 */
export function recencyOrder(sessions: Session[]): string[] {
  return [...sessions]
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id))
    .map((s) => s.id);
}

export function groupOf(session: Session): RailGroupName {
  return session.activity === "active" ? "active" : "recent";
}

/** Which block each id sits in. A missing id is one the order has never seen. */
export function groupIndex(order: SettledOrder): Map<string, RailGroupName> {
  const index = new Map<string, RailGroupName>();
  for (const id of order.active) index.set(id, "active");
  for (const id of order.recent) index.set(id, "recent");
  return index;
}

/** Split a ranked id list into the two blocks, each keeping its rank order. */
export function partition(ids: string[], groupFor: (id: string) => RailGroupName): SettledOrder {
  const active: string[] = [];
  const recent: string[] = [];
  for (const id of ids) (groupFor(id) === "active" ? active : recent).push(id);
  return { active, recent };
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

/** Reconcile both blocks. Each block is reconciled on its own, so the two can never interleave. */
export function reconcileBlocks(previous: SettledOrder, target: SettledOrder): SettledOrder {
  return {
    active: reconcileOrder(previous.active, target.active),
    recent: reconcileOrder(previous.recent, target.recent),
  };
}

function clamp(value: number, max: number): number {
  return Math.min(Math.max(value, 0), max);
}

/**
 * Rule 3: the open session never moves. Whatever row the selected card holds
 * now it keeps — every other card reorders around it, so nobody else's churn
 * can displace it.
 *
 * Its own group still heals: leaving it under "Active" with a dead dot would be
 * a lie about the session the user is looking at. It is re-placed in the block
 * the settle puts it in, at the offset that preserves its absolute row — so the
 * only thing that can cost it a row is the group boundary itself (it was the
 * top card of a block and heals into the block below), and then only the rows
 * that boundary forces.
 */
export function pinSelected(
  pending: SettledOrder,
  held: SettledOrder,
  selectedId?: string,
): SettledOrder {
  if (!selectedId) return pending;
  const at = renderSequence(held).indexOf(selectedId);
  const group = groupIndex(pending).get(selectedId);
  if (at < 0 || group === undefined) return pending;
  const active = pending.active.filter((id) => id !== selectedId);
  const recent = pending.recent.filter((id) => id !== selectedId);
  const block = group === "active" ? active : recent;
  // Rows above the selected card's own block: none for Active, the whole Active
  // block for Recent.
  const above = group === "active" ? 0 : active.length;
  block.splice(clamp(at - above, block.length), 0, selectedId);
  return { active, recent };
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
 * A card counts once if it changes group (its block *is* its position) and
 * otherwise only if it has to be picked up and re-dropped: the count is the
 * fewest such cards, not every index that shifts, so one session rising to the
 * top reads as "1 session moved", not "8". Newcomers are not counted —
 * appearing is not moving.
 */
export function countMoved(held: SettledOrder, pending: SettledOrder): number {
  const heldIndex = new Map(renderSequence(held).map((id, i) => [id, i]));
  const heldGroup = groupIndex(held);
  let regrouped = 0;
  const ranks: number[] = [];
  for (const group of ["active", "recent"] as const) {
    for (const id of pending[group]) {
      const rank = heldIndex.get(id);
      if (rank === undefined) continue;
      if (heldGroup.get(id) !== group) regrouped++;
      else ranks.push(rank);
    }
  }
  return regrouped + (ranks.length - stableRunLength(ranks));
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
 * but only APPLIED at a quiet boundary — the rail is at rest, the pointer
 * leaves it, it loses focus, the search/filter changes, or the user clicks the
 * pending ghost. While the rail is engaged the previous order is held and the
 * caller renders the ghost instead of yanking rows out from under the cursor.
 *
 * Status never sorts. It is not in the ordering key, and a flip cannot relocate
 * a card while the rail is engaged — it only repaints the dot and puts the
 * group heal in the pending order, where it waits its turn behind the same
 * boundary as any other move. A heal *is* a move (the card changes block, and
 * the block is where it sits), so it is gated like one — and, unlike a move, it
 * also has to be applied even when the flat ranking is unchanged, or a card
 * would sit under "Active" with a dead dot forever.
 */
export function useSettledRailOrder(
  sessions: Session[],
  selectedId?: string,
  filterKey?: string,
): SettledRailOrder {
  const orderRef = useRef<SettledOrder>({ active: [], recent: [] });
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

  // Held: every card the rail has already placed keeps the block it settled
  // into, so no live status change can relocate it. Only cards the rail has
  // never seen take their live group — they have no held position to keep.
  const previousGroup = groupIndex(previous);
  const held = reconcileBlocks(
    previous,
    partition(targetIds, (id) => previousGroup.get(id) ?? groupOf(byId.get(id)!)),
  );
  const pending = pinSelected(
    partition(targetIds, (id) => groupOf(byId.get(id)!)),
    held,
    selectedId,
  );
  const moved = countMoved(held, pending);
  const quiet =
    applyRef.current || filterChanged || !(hoveredRef.current || focusedRef.current);
  applyRef.current = false;

  const order = quiet ? pending : held;
  orderRef.current = order;

  return {
    active: order.active.map((id) => byId.get(id)!),
    recent: order.recent.map((id) => byId.get(id)!),
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
