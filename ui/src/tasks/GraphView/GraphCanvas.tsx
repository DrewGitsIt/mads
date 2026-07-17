import { useCallback, useEffect, useState } from "react";
import {
  Background,
  Controls,
  MiniMap,
  Panel as FlowPanel,
  ReactFlow,
  ReactFlowProvider,
  applyNodeChanges,
  useReactFlow,
  type Edge,
  type NodeMouseHandler,
  type OnNodeDrag,
  type OnNodesChange,
  type OnSelectionChangeFunc,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Button } from "../../components/Button/Button";
import type { Task } from "../../router/types";
import type { GraphEdgeLayout, GraphNodeLayout } from "./layout";
import { TASK_ID_DRAG_TYPE } from "./tray";
import { TaskNode, type TaskNodeType } from "./TaskNode";
import styles from "./GraphCanvas.module.css";

const NODE_TYPES = { taskNode: TaskNode };

export type TaskEdgeType = Edge<{ inCycle: boolean }>;

export interface GraphCanvasProps {
  canvasNodes: GraphNodeLayout[];
  edges: GraphEdgeLayout[];
  tasksById: Record<string, Task>;
  /** Degree-0 canvas node ids — computed once by the caller from edge degree (§8), not recomputed here. */
  unsequencedIds: Set<string>;
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
}

function toXYNodes(
  canvasNodes: GraphNodeLayout[],
  tasksById: Record<string, Task>,
  unsequencedIds: Set<string>,
): TaskNodeType[] {
  return canvasNodes.map((n) => ({
    id: n.id,
    type: "taskNode",
    position: { x: n.x - n.width / 2, y: n.y - n.height / 2 },
    data: { task: n.task, tasksById, unsequenced: unsequencedIds.has(n.id) },
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
): TaskEdgeType[] {
  return edges.map((e) => ({
    id: e.id,
    source: e.from,
    target: e.to,
    markerEnd: markerFor(e, selectedEdgeIds),
    animated: tasksById[e.from]?.status === "in_progress",
    data: { inCycle: e.inCycle },
  }));
}

/**
 * Nested inside `<ReactFlowProvider>` so `useReactFlow` (needed for
 * `screenToFlowPosition` on tray->canvas drops, §7) is available.
 */
function GraphCanvasInner({
  canvasNodes,
  edges,
  tasksById,
  unsequencedIds,
  onSelectTask,
  onNodeDragStop,
  onDropOnCanvas,
  onAutoLayout,
}: GraphCanvasProps) {
  const [selectedEdgeIds, setSelectedEdgeIds] = useState<Set<string>>(new Set());
  const [nodes, setNodes] = useState<TaskNodeType[]>(() =>
    toXYNodes(canvasNodes, tasksById, unsequencedIds),
  );
  const { screenToFlowPosition } = useReactFlow();

  // Re-sync from the parent's authoritative layout whenever it changes — the
  // parent is the source of truth for node membership/position (§7); local
  // state below only exists to make in-progress drags feel responsive.
  useEffect(() => {
    setNodes(toXYNodes(canvasNodes, tasksById, unsequencedIds));
  }, [canvasNodes, tasksById, unsequencedIds]);

  const xyEdges = toXYEdges(edges, tasksById, selectedEdgeIds);

  const handleNodesChange: OnNodesChange<TaskNodeType> = useCallback((changes) => {
    setNodes((nds) => applyNodeChanges(changes, nds));
  }, []);

  const handleNodeClick: NodeMouseHandler<TaskNodeType> = useCallback(
    (_event, node) => onSelectTask?.(node.data.task),
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

  return (
    <ReactFlow
      nodes={nodes}
      edges={xyEdges}
      nodeTypes={NODE_TYPES}
      onNodesChange={handleNodesChange}
      onNodeDragStop={handleNodeDragStop}
      onDrop={handleDrop}
      onDragOver={(event) => event.preventDefault()}
      onNodeClick={handleNodeClick}
      onSelectionChange={handleSelectionChange}
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
