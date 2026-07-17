import dagre from "@dagrejs/dagre";
import type { Task } from "../../router/types";

export const NODE_WIDTH = 220;
export const NODE_HEIGHT = 88; // tune visually once TaskNode exists; not load-bearing

const PACK_ROW_WIDTH = 1400;
const PACK_GAP = 60;

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

export interface ComponentLayout {
  key: string; // memberIds.slice().sort().join(",") — stable identity
  nodes: GraphNodeLayout[]; // dagre TB positions, LOCAL to the component's own origin (0,0)
  width: number;
  height: number;
}

export type ComponentSlots = Map<string, { x: number; y: number }>;

export interface GraphLayoutResult {
  trayTaskIds: string[]; // creation-order sort happens in tray.ts, not here — this is just membership
  canvasNodes: GraphNodeLayout[];
  edges: GraphEdgeLayout[];
  slots: ComponentSlots; // hand back to the caller to seed next call's previousSlots
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
 * Same dangling-ref-skip logic as the old single-pass layout: sorted-id
 * iteration for determinism, edges to ids not present in `tasks` are
 * dropped.
 */
export function buildEdges(tasks: Task[], cycles: string[][] = []): GraphEdgeLayout[] {
  const sorted = [...tasks].sort((a, b) => a.id.localeCompare(b.id));
  const ids = new Set(sorted.map((t) => t.id));
  const edges: GraphEdgeLayout[] = [];
  for (const task of sorted) {
    for (const depId of task.dependencies ?? []) {
      if (!ids.has(depId)) continue;
      edges.push({ id: `${depId}->${task.id}`, from: depId, to: task.id, inCycle: isCycleEdge(depId, task.id, cycles) });
    }
  }
  return edges;
}

/**
 * Union-find over undirected adjacency from `edges`. Only ids with degree
 * >= 1 appear in any returned component — degree-0 ids are tray/floating
 * concerns handled by the caller (§2). Deterministic: ids sorted before
 * traversal, groups sorted by their smallest member id.
 */
export function connectedComponents(taskIds: string[], edges: GraphEdgeLayout[]): string[][] {
  const parent = new Map<string, string>();
  for (const id of taskIds) parent.set(id, id);

  function find(x: string): string {
    let root = x;
    while (parent.get(root) !== root) root = parent.get(root)!;
    let cur = x;
    while (parent.get(cur) !== root) {
      const next = parent.get(cur)!;
      parent.set(cur, root);
      cur = next;
    }
    return root;
  }

  function union(a: string, b: string): void {
    const ra = find(a);
    const rb = find(b);
    if (ra !== rb) parent.set(ra, rb);
  }

  for (const edge of edges) union(edge.from, edge.to);

  const degree = new Map<string, number>();
  for (const edge of edges) {
    degree.set(edge.from, (degree.get(edge.from) ?? 0) + 1);
    degree.set(edge.to, (degree.get(edge.to) ?? 0) + 1);
  }

  const groups = new Map<string, string[]>();
  for (const id of [...taskIds].sort()) {
    if (!degree.get(id)) continue;
    const root = find(id);
    const group = groups.get(root);
    if (group) group.push(id);
    else groups.set(root, [id]);
  }

  return [...groups.values()].sort((a, b) => a[0].localeCompare(b[0]));
}

/**
 * Today's dagre call, scoped to one component's subgraph: rankdir TB,
 * nodesep 40, ranksep 60, fixed NODE_WIDTH/NODE_HEIGHT, sorted-id
 * iteration. Output is normalized so the component's own bounding box
 * starts at local (0,0) — packing adds an origin offset on top.
 */
export function layoutComponent(
  memberIds: string[],
  tasksById: Record<string, Task>,
  edges: GraphEdgeLayout[],
): ComponentLayout {
  const sortedIds = [...memberIds].sort();
  const memberSet = new Set(memberIds);

  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "TB", nodesep: 40, ranksep: 60 });
  g.setDefaultEdgeLabel(() => ({}));

  for (const id of sortedIds) {
    g.setNode(id, { width: NODE_WIDTH, height: NODE_HEIGHT });
  }
  for (const edge of edges) {
    if (memberSet.has(edge.from) && memberSet.has(edge.to)) g.setEdge(edge.from, edge.to);
  }

  dagre.layout(g);

  const raw = sortedIds.map((id) => {
    const n = g.node(id);
    return { id, x: n.x, y: n.y, width: n.width, height: n.height, task: tasksById[id] };
  });

  const minX = Math.min(...raw.map((n) => n.x - n.width / 2));
  const minY = Math.min(...raw.map((n) => n.y - n.height / 2));
  const nodes: GraphNodeLayout[] = raw.map((n) => ({ ...n, x: n.x - minX, y: n.y - minY }));

  const width = Math.max(...nodes.map((n) => n.x + n.width / 2));
  const height = Math.max(...nodes.map((n) => n.y + n.height / 2));

  return { key: sortedIds.join(","), nodes, width, height };
}

/**
 * Row-wrapping pack, aspect-aware (wraps at a fixed logical row width).
 * Exact key match against `previousSlots` reuses that origin verbatim; new
 * or membership-changed components (new key by construction) append below
 * the lowest reused slot in row-wrapping order. This is the intentional v1
 * — no overlap-based "which old component do I most resemble" matching.
 */
export function packComponents(components: ComponentLayout[], previousSlots: ComponentSlots): ComponentSlots {
  const slots: ComponentSlots = new Map();
  const unmatched: ComponentLayout[] = [];
  let lowestReusedBottom = 0;

  for (const component of components) {
    const prev = previousSlots.get(component.key);
    if (prev) {
      slots.set(component.key, prev);
      lowestReusedBottom = Math.max(lowestReusedBottom, prev.y + component.height);
    } else {
      unmatched.push(component);
    }
  }

  let cursorX = 0;
  let cursorY = lowestReusedBottom > 0 ? lowestReusedBottom + PACK_GAP : 0;
  let rowHeight = 0;
  for (const component of unmatched) {
    if (cursorX > 0 && cursorX + component.width > PACK_ROW_WIDTH) {
      cursorX = 0;
      cursorY += rowHeight + PACK_GAP;
      rowHeight = 0;
    }
    slots.set(component.key, { x: cursorX, y: cursorY });
    cursorX += component.width + PACK_GAP;
    rowHeight = Math.max(rowHeight, component.height);
  }

  return slots;
}

function componentSortKey(memberIds: string[], tasksById: Record<string, Task>): [string, string] {
  let minCreated = "";
  let minId = "";
  for (const id of memberIds) {
    const createdAt = tasksById[id].created_at;
    if (minCreated === "" || createdAt < minCreated || (createdAt === minCreated && id < minId)) {
      minCreated = createdAt;
      minId = id;
    }
  }
  return [minCreated, minId];
}

/**
 * Pure layout function — no DOM access, deterministic given the same
 * inputs. `manualPositions[id]` wins outright when present (dragged
 * connected node, or a tray node dragged in with no edges yet); otherwise
 * degree >= 1 nodes place via their component's packed slot, and degree-0
 * nodes with no manual position never reach `canvasNodes` — they're tray
 * membership instead (§2).
 */
export function computeGraphLayout(
  tasks: Task[],
  cycles: string[][],
  manualPositions: Record<string, { x: number; y: number }>,
  previousSlots: ComponentSlots,
): GraphLayoutResult {
  const tasksById: Record<string, Task> = {};
  for (const task of tasks) tasksById[task.id] = task;

  const edges = buildEdges(tasks, cycles);

  const edgeDegree = new Map<string, number>();
  for (const edge of edges) {
    edgeDegree.set(edge.from, (edgeDegree.get(edge.from) ?? 0) + 1);
    edgeDegree.set(edge.to, (edgeDegree.get(edge.to) ?? 0) + 1);
  }

  const taskIds = tasks.map((t) => t.id);
  const groups = connectedComponents(taskIds, edges);
  const orderedGroups = [...groups].sort((a, b) => {
    const [aCreated, aId] = componentSortKey(a, tasksById);
    const [bCreated, bId] = componentSortKey(b, tasksById);
    return aCreated !== bCreated ? aCreated.localeCompare(bCreated) : aId.localeCompare(bId);
  });

  const componentLayouts = orderedGroups.map((memberIds) => layoutComponent(memberIds, tasksById, edges));
  const slots = packComponents(componentLayouts, previousSlots);

  const canvasNodes: GraphNodeLayout[] = [];
  for (const component of componentLayouts) {
    const slot = slots.get(component.key)!;
    for (const localNode of component.nodes) {
      const manual = manualPositions[localNode.id];
      const pos = manual ?? { x: localNode.x + slot.x, y: localNode.y + slot.y };
      canvasNodes.push({ id: localNode.id, x: pos.x, y: pos.y, width: localNode.width, height: localNode.height, task: localNode.task });
    }
  }

  const trayTaskIds: string[] = [];
  for (const task of tasks) {
    const degree = edgeDegree.get(task.id) ?? 0;
    const manual = manualPositions[task.id];
    if (degree === 0 && manual === undefined) {
      trayTaskIds.push(task.id);
    } else if (degree === 0 && manual !== undefined) {
      canvasNodes.push({ id: task.id, x: manual.x, y: manual.y, width: NODE_WIDTH, height: NODE_HEIGHT, task });
    }
  }

  const xs = canvasNodes.map((n) => n.x - n.width / 2).concat(canvasNodes.map((n) => n.x + n.width / 2));
  const ys = canvasNodes.map((n) => n.y - n.height / 2).concat(canvasNodes.map((n) => n.y + n.height / 2));
  const extent = {
    minX: xs.length ? Math.min(...xs) : 0,
    minY: ys.length ? Math.min(...ys) : 0,
    maxX: xs.length ? Math.max(...xs) : 0,
    maxY: ys.length ? Math.max(...ys) : 0,
  };

  return { trayTaskIds, canvasNodes, edges, slots, extent };
}
