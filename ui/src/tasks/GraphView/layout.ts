import dagre from "@dagrejs/dagre";
import type { Task } from "../../router/types";

export const NODE_HEIGHT = 48;

export function nodeWidth(title: string): number {
  return Math.min(260, Math.max(120, 8 * title.length));
}

export interface GraphNodeLayout {
  id: string;
  x: number;
  y: number;
  width: number;
  height: number;
  task: Task;
}

export interface GraphEdgeLayout {
  id: string;
  from: string;
  to: string;
  inCycle: boolean;
}

export interface GraphLayout {
  nodes: GraphNodeLayout[];
  edges: GraphEdgeLayout[];
  /** Logical bounding box across all nodes — Minimap/GraphCanvas derive geometry from this, never the DOM (§5). */
  extent: { minX: number; minY: number; maxX: number; maxY: number };
}

function isCycleEdge(from: string, to: string, cycles: string[][]): boolean {
  return cycles.some((cycle) =>
    cycle.some(
      (id, i) =>
        (id === from && cycle[(i + 1) % cycle.length] === to) ||
        (id === to && cycle[(i + 1) % cycle.length] === from),
    ),
  );
}

/**
 * Pure layout function — no DOM access, deterministic given the same
 * (tasks, cycles) input. Nodes are added to dagre in id-sorted order since
 * dagre's tie-breaking depends on insertion order (§5).
 */
export function layout(tasks: Task[], cycles: string[][] = []): GraphLayout {
  const sorted = [...tasks].sort((a, b) => a.id.localeCompare(b.id));

  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "TB", nodesep: 40, ranksep: 60 });
  g.setDefaultEdgeLabel(() => ({}));

  for (const task of sorted) {
    g.setNode(task.id, { width: nodeWidth(task.title), height: NODE_HEIGHT });
  }

  const ids = new Set(sorted.map((t) => t.id));
  const edgePairs: { from: string; to: string }[] = [];
  for (const task of sorted) {
    for (const depId of task.dependencies ?? []) {
      if (!ids.has(depId)) continue;
      g.setEdge(depId, task.id);
      edgePairs.push({ from: depId, to: task.id });
    }
  }

  dagre.layout(g);

  const nodes: GraphNodeLayout[] = sorted.map((task) => {
    const n = g.node(task.id);
    return { id: task.id, x: n.x, y: n.y, width: n.width, height: n.height, task };
  });

  const edges: GraphEdgeLayout[] = edgePairs.map(({ from, to }) => ({
    id: `${from}->${to}`,
    from,
    to,
    inCycle: isCycleEdge(from, to, cycles),
  }));

  const xs = nodes.map((n) => n.x - n.width / 2).concat(nodes.map((n) => n.x + n.width / 2));
  const ys = nodes.map((n) => n.y - n.height / 2).concat(nodes.map((n) => n.y + n.height / 2));
  const extent = {
    minX: xs.length ? Math.min(...xs) : 0,
    minY: ys.length ? Math.min(...ys) : 0,
    maxX: xs.length ? Math.max(...xs) : 0,
    maxY: ys.length ? Math.max(...ys) : 0,
  };

  return { nodes, edges, extent };
}
