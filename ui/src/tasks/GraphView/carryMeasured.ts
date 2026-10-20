import type { Node } from "@xyflow/react";

/**
 * Copies each node's xyflow-measured size from `prev` onto the matching
 * (by id) fresh node in `next`. xyflow rebuilds its internal node from the
 * user node, so a node object without `measured` is treated as unmeasured and
 * drawn `visibility: hidden`. If that reset lands in the same render batch as
 * the ResizeObserver's first measurement, xyflow never re-observes the node
 * and it stays hidden for good. A carried size that goes stale (e.g. a zoom
 * level change) is fine: the observer is still attached and re-measures it.
 */
export function carryMeasured<T extends Node>(prev: T[], next: T[]): T[] {
  const measuredById = new Map(prev.map((n) => [n.id, n.measured]));
  return next.map((n) => {
    const measured = measuredById.get(n.id);
    return measured && !n.measured ? { ...n, measured } : n;
  });
}
