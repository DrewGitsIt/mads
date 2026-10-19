import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
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
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import { carryMeasured } from "./carryMeasured";
import { computeHighlightSet } from "./highlight";
import { NODE_HEIGHT, NODE_WIDTH, type GraphEdgeLayout, type GraphNodeLayout } from "./layout";
import type { EdgeRunKind, NodeRunView } from "./runView";
import { RING_SIZE } from "./StageRing";
import { TASK_ID_DRAG_TYPE } from "./tray";
import { TaskNode, type GraphLevel, type TaskNodeType } from "./TaskNode";
import { usePrefersReducedMotion } from "./usePrefersReducedMotion";
import styles from "./GraphCanvas.module.css";

const NODE_TYPES = { taskNode: TaskNode };

// Stable identity for the "no cycles" default — a `= new Set()` parameter
// default would otherwise construct a fresh Set every render, which sits in
// a useEffect dependency array below and would re-fire (and re-render) forever.
const EMPTY_CYCLE_IDS: Set<string> = new Set();

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
  /** Live run-fold state, keyed by task id — undefined when no run is being observed. */
  taskRunState?: Record<string, TaskRunState>;
  /** Ordered stage ids for the live run — undefined when no run is being observed. */
  columns?: string[];
  /** Ids dimmed by the status filter strip's hover-spotlight/click-isolate — combined additively with highlight-mode dimming. */
  statusDimmedIds?: Set<string> | null;
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
  /** The run being shown, per task id. With it, nodes draw the run view; without it, the plain status cards. */
  runViews?: Record<string, NodeRunView>;
  /** Run-view edge treatments by edge id; replaces the plain live/done edge styling when `runViews` is set. */
  edgeKinds?: Record<string, EdgeRunKind>;
  /** Overview draws rings, Cards draws cards. Needs `runViews`. */
  level?: GraphLevel;
  /** The node the focus view is zoomed into — ringed. */
  focusTaskId?: string;
  /** Drawn in the top-right panel, before Auto-layout. */
  legend?: ReactNode;
  /** Fires when the user zooms (wheel, pinch, controls) — not on programmatic fits. */
  onUserZoom?: (zoom: number) => void;
  /** Bumped by the caller to re-fit the view once the canvas has resized (e.g. entering focus). */
  refitSignal?: number;
}

// Node boxes are positioned by their top-left; layout positions are centers
// of a slot `height` tall. Cards and rings both draw from the slot's top, so
// a level switch keeps every node's top in place and a card (bounded by the
// slot height) never reaches the next rank.
function halfSize(level: GraphLevel | undefined, hasRun: boolean, height: number) {
  return { x: hasRun && level === "overview" ? RING_SIZE / 2 : NODE_WIDTH / 2, y: height / 2 };
}

function toXYNodes(
  canvasNodes: GraphNodeLayout[],
  tasksById: Record<string, Task>,
  unsequencedIds: Set<string>,
  cycleNodeIds: Set<string>,
  highlightSet: Set<string> | null,
  taskRunState: Record<string, TaskRunState> | undefined,
  columns: string[] | undefined,
  statusDimmedIds: Set<string> | null | undefined,
  reducedMotion: boolean,
  runViews: Record<string, NodeRunView> | undefined,
  level: GraphLevel | undefined,
  focusTaskId: string | undefined,
): TaskNodeType[] {
  return canvasNodes.map((n) => ({
    id: n.id,
    type: "taskNode",
    position: {
      x: n.x - halfSize(level, runViews !== undefined, n.height).x,
      y: n.y - halfSize(level, runViews !== undefined, n.height).y,
    },
    // Selecting a node for highlight also selects it in xyflow's own
    // built-in sense; without this, Backspace with a node selected would
    // ALSO delete the node via onNodesDelete, not just the intended edges.
    deletable: false,
    data: {
      task: n.task,
      tasksById,
      taskRunState: taskRunState?.[n.id],
      columns,
      reducedMotion,
      unsequenced: unsequencedIds.has(n.id),
      inCycle: cycleNodeIds.has(n.id),
      dimmed: (highlightSet !== null && !highlightSet.has(n.id)) || (statusDimmedIds?.has(n.id) ?? false),
      run: runViews?.[n.id],
      level,
      focused: focusTaskId === n.id,
    },
  }));
}

const EDGE_KIND_CLASS: Record<EdgeRunKind, string | undefined> = {
  critical: styles.edgeCritical,
  cut: styles.edgeCut,
  hot: styles.edgeHot,
  done: styles.edgeChainDone,
};

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
  statusDimmedIds: Set<string> | null | undefined,
  reducedMotion: boolean,
  edgeKinds: Record<string, EdgeRunKind> | undefined,
  onDeleteEdge?: (from: string, to: string) => void,
): TaskEdgeType[] {
  return edges.map((e) => {
    const dimmed =
      (highlightSet !== null && !(highlightSet.has(e.from) && highlightSet.has(e.to))) ||
      (statusDimmedIds?.has(e.from) ?? false) ||
      (statusDimmedIds?.has(e.to) ?? false);
    const fromStatus = tasksById[e.from]?.status;
    const toStatus = tasksById[e.to]?.status;
    const isLive = fromStatus === "in_progress";
    if (edgeKinds) {
      // Run view: an edge into a running task flows; the rest is by kind.
      const kind = edgeKinds[e.id];
      const hot = kind === "hot";
      return {
        id: e.id,
        source: e.from,
        target: e.to,
        type: "depEdge",
        markerEnd: markerFor(e, selectedEdgeIds),
        animated: hot && !reducedMotion,
        className:
          [
            kind && EDGE_KIND_CLASS[kind],
            hot && reducedMotion && styles.edgeHotStatic,
            e.inCycle && styles.edgeCycle,
            dimmed && styles.dimmed,
          ]
            .filter(Boolean)
            .join(" ") || undefined,
        data: { inCycle: e.inCycle, onDelete: onDeleteEdge ? () => onDeleteEdge(e.from, e.to) : undefined },
      };
    }
    return {
      id: e.id,
      source: e.from,
      target: e.to,
      type: "depEdge",
      markerEnd: markerFor(e, selectedEdgeIds),
      animated: isLive && !reducedMotion,
      // Token-driven line color (markers alone aren't enough — see GraphCanvas.module.css).
      className:
        [
          fromStatus === "done" && styles.edgeDone,
          isLive && !reducedMotion && styles.edgeLive,
          isLive && reducedMotion && styles.edgeLiveStatic,
          toStatus === "blocked" && styles.edgeIntoBlocked,
          e.inCycle && styles.edgeCycle,
          dimmed && styles.dimmed,
        ]
          .filter(Boolean)
          .join(" ") || undefined,
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
  cycleNodeIds = EMPTY_CYCLE_IDS,
  taskRunState,
  columns,
  statusDimmedIds,
  onSelectTask,
  onNodeDragStop,
  onDropOnCanvas,
  onAutoLayout,
  extent,
  autoLayoutSignal,
  onConnect,
  onDeleteEdges,
  runViews,
  edgeKinds,
  level,
  focusTaskId,
  legend,
  onUserZoom,
  refitSignal,
}: GraphCanvasProps) {
  const [selectedEdgeIds, setSelectedEdgeIds] = useState<Set<string>>(new Set());
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const highlightSet = selectedNodeId ? computeHighlightSet(selectedNodeId, edges) : null;
  const reducedMotion = usePrefersReducedMotion();
  const [nodes, setNodes] = useState<TaskNodeType[]>(() =>
    toXYNodes(
      canvasNodes,
      tasksById,
      unsequencedIds,
      cycleNodeIds,
      highlightSet,
      taskRunState,
      columns,
      statusDimmedIds,
      reducedMotion,
      runViews,
      level,
      focusTaskId,
    ),
  );
  const { screenToFlowPosition, fitBounds, fitView } = useReactFlow();

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

  // The canvas resizes over the focus transition; fit once it has settled.
  const previousRefitSignal = useRef(refitSignal);
  useEffect(() => {
    if (refitSignal === undefined || refitSignal === previousRefitSignal.current) return;
    previousRefitSignal.current = refitSignal;
    const timer = setTimeout(() => void fitView({ padding: 0.2 }), reducedMotion ? 0 : 320);
    return () => clearTimeout(timer);
  }, [refitSignal, fitView, reducedMotion]);

  // Re-sync from the parent's authoritative layout whenever it changes — the
  // parent is the source of truth for node membership/position (§7); local
  // state below only exists to make in-progress drags feel responsive.
  useEffect(() => {
    // Keep xyflow's measured sizes; fresh objects without them hide the nodes (see carryMeasured).
    setNodes((prev) =>
      carryMeasured(
        prev,
        toXYNodes(
          canvasNodes,
          tasksById,
          unsequencedIds,
          cycleNodeIds,
          highlightSet,
          taskRunState,
          columns,
          statusDimmedIds,
          reducedMotion,
          runViews,
          level,
          focusTaskId,
        ),
      ),
    );
    // highlightSet is intentionally omitted: it's derived from selectedNodeId
    // + edges (already deps below) every render, re-listing it here would
    // just be a redundant identity-changes-every-render trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    canvasNodes,
    tasksById,
    unsequencedIds,
    cycleNodeIds,
    selectedNodeId,
    edges,
    taskRunState,
    columns,
    statusDimmedIds,
    reducedMotion,
    runViews,
    level,
    focusTaskId,
  ]);

  const xyEdges = toXYEdges(
    edges,
    tasksById,
    selectedEdgeIds,
    highlightSet,
    statusDimmedIds,
    reducedMotion,
    runViews ? (edgeKinds ?? {}) : undefined,
    onDeleteEdges && ((from, to) => onDeleteEdges([{ from, to }])),
  );

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
      // Layout positions are node centers; xyflow's are top-left corners.
      const height = canvasNodes.find((n) => n.id === node.id)?.height ?? NODE_HEIGHT;
      const half = halfSize(node.data.level, node.data.run !== undefined, height);
      onNodeDragStop?.(node.id, point, { x: node.position.x + half.x, y: node.position.y + half.y });
    },
    [onNodeDragStop, canvasNodes],
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
      onMove={(event, viewport) => {
        if (event) onUserZoom?.(viewport.zoom);
      }}
      fitView
    >
      <Background />
      {/* Bottom-left belongs to the zoom-level control when a run is shown. */}
      <Controls position={runViews ? "top-left" : "bottom-left"} />
      {/* The focus column is too narrow for a minimap; it would cover the nodes. */}
      {!focusTaskId && <MiniMap />}
      <FlowPanel position="top-right" className={styles.topRight}>
        {legend}
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
