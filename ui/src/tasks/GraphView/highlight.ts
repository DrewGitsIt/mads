import type { GraphEdgeLayout } from "./layout";

/**
 * Ancestors: nodes reachable by walking edges backward from `nodeId` (what
 * it depends on, transitively). Descendants: forward (what depends on it,
 * transitively — "what does this unblock"). Includes `nodeId` itself.
 * Guarded with a visited set — cycles are legal at rest, this must not
 * infinite-loop on one.
 */
export function computeHighlightSet(nodeId: string, edges: GraphEdgeLayout[]): Set<string> {
  const forward = new Map<string, string[]>();
  const backward = new Map<string, string[]>();
  for (const e of edges) {
    (forward.get(e.from) ?? forward.set(e.from, []).get(e.from)!).push(e.to);
    (backward.get(e.to) ?? backward.set(e.to, []).get(e.to)!).push(e.from);
  }
  const result = new Set<string>([nodeId]);
  function walk(start: string, adj: Map<string, string[]>) {
    const stack = [...(adj.get(start) ?? [])];
    while (stack.length) {
      const n = stack.pop()!;
      if (result.has(n)) continue;
      result.add(n);
      stack.push(...(adj.get(n) ?? []));
    }
  }
  walk(nodeId, forward);
  walk(nodeId, backward);
  return result;
}
