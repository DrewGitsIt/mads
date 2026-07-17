import { useCallback, useEffect, useRef, useState } from "react";
import {
  Background,
  BaseEdge,
  Controls,
  EdgeLabelRenderer,
  MiniMap,
  Panel as FlowPanel,
  ReactFlow,
  ReactFlowProvider,
  applyNodeChanges,
  getBezierPath,
  useReactFlow,
  type Edge,
  type EdgeProps,
  type NodeMouseHandler,
  type OnConnect,
  type OnEdgesDelete,
  type OnNodeDrag,
  type OnNodesChange,
  type OnSelectionChangeFunc,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Button } from "../../components/Button/Button";
import type { Task } from "../../router/types";
import { computeHighlightSet } from "./highlight";
import type { GraphEdgeLayout, GraphNodeLayout } from "./layout";
import { TASK_ID_DRAG_TYPE } from "./tray";
import { TaskNode, type TaskNodeType } from "./TaskNode";
import styles from "./GraphCanvas.module.css";

const NODE_TYPES = { taskNode: TaskNode };

export type TaskEdgeData = { inCycle: boolean; onDelete?: () => void };
export type TaskEdgeType = Edge<TaskEdgeData, "depEdge">;

export interface GraphCanvasProps {
  canvasNodes: GraphNodeLayout[];
  edges: GraphEdgeLayout[];
  tasksById: Record<string, Task>;
  /** Degree-0 canvas node ids — computed once by the caller from edge degree (§8), not recomputed here. */
  unsequencedIds: Set<string>;
  /** Ids of every node participating in a cycle (union of GET /tasks/cycles' result) — computed once by the caller, not recomputed here. */
  cycleNodeIds?: Set<string>;
  onSelectTask?: (task: Task) => void;
  /** Fires after a canvas-internal drag settles, both the raw client point (for tray hit-testing) and the resolved flow position. */
  onNodeDragStop?: (
    taskId: string,
    clientPoint: { x: number; y: number },
    flowPosition: { x: number; y: number },
  ) => void;
  /** Fires when a tray card is natively dropped onto the canvas pane. */
  onDropOnCanvas?: (taskId: string, flowPosition: { x: number; y: number }) => void;
  onAutoLayout?: () => void;
  /** Bounding box of `canvasNodes`, in flow coordinates — used to fit-to-view after auto-layout. */
  extent?: { minX: number; minY: number; maxX: number; maxY: number };
  /** Bumped by the caller each time auto-layout runs; a change (not the value itself) triggers the re-fit. */
  autoLayoutSignal?: number;
  /** Fires when a drag from a node's bottom (source) handle completes on another node's top (target) handle. */
  onConnect?: (source: string, target: string) => void;
  /** Fires on Backspace/Delete with one or more edges selected, and from the per-edge X affordance. */
  onDeleteEdges?: (pairs: { from: string; to: string }[]) => void;
}

function toXYNodes(
  canvasNodes: GraphNodeLayout[],
  tasksById: Record<string, Task>,
  unsequencedIds: Set<string>,
  cycleNodeIds: Set<string>,
  highlightSet: Set<string> | null,
): TaskNodeType[] {
  return canvasNodes.map((n) => ({
    id: n.id,
    type: "taskNode",
    position: { x: n.x - n.width / 2, y: n.y - n.height / 2 },
    // Selecting a node for highlight also selects it in xyflow's own
    // built-in sense; without this, Backspace with a node selected would
    // ALSO delete the node via onNodesDelete, not just the intended edges.
    deletable: false,
    data: {
      task: n.task,
      tasksById,
      unsequenced: unsequencedIds.has(n.id),
      inCycle: cycleNodeIds.has(n.id),
      dimmed: highlightSet !== null && !highlightSet.has(n.id),
    },
  }));
}

/*
 * Bare marker id, not a "url(#id)" string — xyflow's own markerEnd
 * resolution wraps whatever string you give it in `url('#...')` itself
 * (`getMarkerId` in @xyflow/system), so passing an already-wrapped value
 * here would double-wrap into an invalid `url('#url(#id)')`.
 */
function markerFor(edge: GraphEdgeLayout, selectedEdgeIds: Set<string>): string {
  if (selectedEdgeIds.has(edge.id)) return "mads-arrow-selected";
  if (edge.inCycle) return "mads-arrow-cycle";
  return "mads-arrow";
}

function toXYEdges(
  edges: GraphEdgeLayout[],
  tasksById: Record<string, Task>,
  selectedEdgeIds: Set<string>,
  highlightSet: Set<string> | null,
  onDeleteEdge?: (from: string, to: string) => void,
): TaskEdgeType[] {
  return edges.map((e) => {
    const dimmed = highlightSet !== null && !(highlightSet.has(e.from) && highlightSet.has(e.to));
    return {
      id: e.id,
      source: e.from,
      target: e.to,
      type: "depEdge",
      markerEnd: markerFor(e, selectedEdgeIds),
      animated: tasksById[e.from]?.status === "in_progress",
      // Token-driven line color (markers alone aren't enough — see GraphCanvas.module.css).
      className: [e.inCycle ? styles.edgeCycle : "", dimmed ? styles.dimmed : ""].filter(Boolean).join(" ") || undefined,
      data: { inCycle: e.inCycle, onDelete: onDeleteEdge ? () => onDeleteEdge(e.from, e.to) : undefined },
    };
  });
}

/** Custom edge type: renders the base bezier path plus an X-button affordance when selected (§5). */
function DepEdge({ id, sourceX, sourceY, targetX, targetY, markerEnd, style, selected, data }: EdgeProps<TaskEdgeType>) {
  const [path, labelX, labelY] = getBezierPath({ sourceX, sourceY, targetX, targetY });
  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style} />
      {selected && (
        <EdgeLabelRenderer>
          <button
            type="button"
            className={styles.edgeDeleteButton}
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
            onClick={data?.onDelete}
            aria-label="Remove dependency"
          >
            ✕
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  );
}
const EDGE_TYPES = { depEdge: DepEdge };

/**
 * Nested inside `<ReactFlowProvider>` so `useReactFlow` (needed for
 * `screenToFlowPosition` on tray->canvas drops, §7) is available.
 */
function GraphCanvasInner({
  canvasNodes,
  edges,
  tasksById,
  unsequencedIds,
  cycleNodeIds = new Set(),
  onSelectTask,
  onNodeDragStop,
  onDropOnCanvas,
  onAutoLayout,
  extent,
  autoLayoutSignal,
  onConnect,
  onDeleteEdges,
}: GraphCanvasProps) {
  const [selectedEdgeIds, setSelectedEdgeIds] = useState<Set<string>>(new Set());
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const highlightSet = selectedNodeId ? computeHighlightSet(selectedNodeId, edges) : null;
  const [nodes, setNodes] = useState<TaskNodeType[]>(() =>
    toXYNodes(canvasNodes, tasksById, unsequencedIds, cycleNodeIds, highlightSet),
  );
  const { screenToFlowPosition, fitBounds } = useReactFlow();

  useEffect(() => {
    if (selectedNodeId === null) return;
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setSelectedNodeId(null);
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [selectedNodeId]);

  // fitView's `fitView` boolean prop only fires on mount. Re-fit after
  // auto-layout by watching for a signal bump (not node/extent identity,
  // which also change on every ordinary drag) and fitting to the fresh
  // extent directly via fitBounds — this sidesteps waiting on xyflow's own
  // async sync of the controlled `nodes` prop into its internal store.
  const previousAutoLayoutSignal = useRef(autoLayoutSignal);
  useEffect(() => {
    if (autoLayoutSignal === undefined || autoLayoutSignal === previousAutoLayoutSignal.current) return;
    previousAutoLayoutSignal.current = autoLayoutSignal;
    if (!extent) return;
    fitBounds({
      x: extent.minX,
      y: extent.minY,
      width: Math.max(extent.maxX - extent.minX, 1),
      height: Math.max(extent.maxY - extent.minY, 1),
    });
  }, [autoLayoutSignal, extent, fitBounds]);

  // Re-sync from the parent's authoritative layout whenever it changes — the
  // parent is the source of truth for node membership/position (§7); local
  // state below only exists to make in-progress drags feel responsive.
  useEffect(() => {
    setNodes(toXYNodes(canvasNodes, tasksById, unsequencedIds, cycleNodeIds, highlightSet));
    // highlightSet is intentionally omitted: it's derived from selectedNodeId
    // + edges (already deps below) every render, re-listing it here would
    // just be a redundant identity-changes-every-render trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canvasNodes, tasksById, unsequencedIds, cycleNodeIds, selectedNodeId, edges]);

  const xyEdges = toXYEdges(edges, tasksById, selectedEdgeIds, highlightSet, onDeleteEdges && ((from, to) => onDeleteEdges([{ from, to }])));

  const handleNodesChange: OnNodesChange<TaskNodeType> = useCallback((changes) => {
    setNodes((nds) => applyNodeChanges(changes, nds));
  }, []);

  const handleNodeClick: NodeMouseHandler<TaskNodeType> = useCallback(
    (_event, node) => {
      onSelectTask?.(node.data.task);
      setSelectedNodeId(node.id);
    },
    [onSelectTask],
  );

  const handleSelectionChange: OnSelectionChangeFunc = useCallback(({ edges: selected }) => {
    setSelectedEdgeIds(new Set(selected.map((e) => e.id)));
  }, []);

  const handleNodeDragStop: OnNodeDrag<TaskNodeType> = useCallback(
    (event, node) => {
      const point =
        "clientX" in event
          ? { x: event.clientX, y: event.clientY }
          : { x: event.touches[0]?.clientX ?? 0, y: event.touches[0]?.clientY ?? 0 };
      onNodeDragStop?.(node.id, point, node.position);
    },
    [onNodeDragStop],
  );

  const handleDrop = useCallback(
    (event: React.DragEvent<HTMLDivElement>) => {
      event.preventDefault();
      const taskId = event.dataTransfer.getData(TASK_ID_DRAG_TYPE);
      if (!taskId) return;
      const flowPosition = screenToFlowPosition({ x: event.clientX, y: event.clientY });
      onDropOnCanvas?.(taskId, flowPosition);
    },
    [onDropOnCanvas, screenToFlowPosition],
  );

  const handleConnect: OnConnect = useCallback(
    (connection) => {
      if (connection.source && connection.target) onConnect?.(connection.source, connection.target);
    },
    [onConnect],
  );

  const handleEdgesDelete: OnEdgesDelete<TaskEdgeType> = useCallback(
    (deleted) => {
      onDeleteEdges?.(deleted.map((e) => ({ from: e.source, to: e.target })));
    },
    [onDeleteEdges],
  );

  return (
    <ReactFlow
      nodes={nodes}
      edges={xyEdges}
      nodeTypes={NODE_TYPES}
      edgeTypes={EDGE_TYPES}
      onNodesChange={handleNodesChange}
      onNodeDragStop={handleNodeDragStop}
      onDrop={handleDrop}
      onDragOver={(event) => event.preventDefault()}
      onNodeClick={handleNodeClick}
      onSelectionChange={handleSelectionChange}
      onConnect={handleConnect}
      onEdgesDelete={handleEdgesDelete}
      onPaneClick={() => setSelectedNodeId(null)}
      fitView
    >
      <Background />
      <Controls />
      <MiniMap />
      <FlowPanel position="top-right">
        <Button variant="secondary" size="sm" onClick={onAutoLayout}>
          Auto-layout
        </Button>
      </FlowPanel>
      <svg style={{ position: "absolute", width: 0, height: 0 }}>
        <defs>
          <marker
            id="mads-arrow"
            className={styles.arrowDefault}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="7"
            markerHeight="7"
            orient="auto-start-reverse"
          >
            <path d="M0,0 L10,5 L0,10 z" />
          </marker>
          <marker
            id="mads-arrow-selected"
            className={styles.arrowSelected}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="7"
            markerHeight="7"
            orient="auto-start-reverse"
          >
            <path d="M0,0 L10,5 L0,10 z" />
          </marker>
          <marker
            id="mads-arrow-cycle"
            className={styles.arrowCycle}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="7"
            markerHeight="7"
            orient="auto-start-reverse"
          >
            <path d="M0,0 L10,5 L0,10 z" />
          </marker>
        </defs>
      </svg>
    </ReactFlow>
  );
}

export function GraphCanvas(props: GraphCanvasProps) {
  return (
    <div className={styles.wrapper}>
      <ReactFlowProvider>
        <GraphCanvasInner {...props} />
      </ReactFlowProvider>
    </div>
  );
}
