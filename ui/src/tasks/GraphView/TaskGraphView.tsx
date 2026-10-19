import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog/ConfirmDialog";
import { Toast } from "../../components/Toast/Toast";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { criticalPath } from "../../pipeline/plan/planScope";
import { runStages, type RunStage } from "../../pipeline/plan/runStages";
import type { RunMode } from "../../pipeline/plan/runStatus";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import { useEscapeLayer } from "../../platform/escapeLayers";
import type { RouterClient } from "../../router/client";
import type { Task } from "../../router/types";
import {
  applyEdgeStrip,
  invertPlan,
  planEdgeCreation,
  planEdgesRemoval,
  planEdgeStrip,
  type EdgeStripPlan,
} from "./edgeOps";
import { FocusPanel } from "./FocusPanel";
import { GraphCanvas } from "./GraphCanvas";
import { GraphRunActionsContext, type GraphRunActions } from "./graphRunActions";
import { graphStatusOf, type GraphStatus } from "./graphStatus";
import { computeGraphLayout, graphNodeHeight, type ComponentSlots } from "./layout";
import { loadPositions, savePositions } from "./positions";
import {
  criticalEdgeIds,
  currentStageOf,
  edgeRunKindOf,
  neighboursOf,
  nodeRunViews,
  type EdgeRunKind,
} from "./runView";
import { StatusFilterStrip } from "./StatusFilterStrip";
import type { GraphLevel } from "./TaskNode";
import { trayOrder } from "./tray";
import { useGraphRunState } from "./useGraphRunState";
import { usePrefersReducedMotion } from "./usePrefersReducedMotion";
import { UnsequencedTray } from "./UnsequencedTray";
import { ZoomLevelControl, type ZoomLevel } from "./ZoomLevelControl";
import styles from "./TaskGraphView.module.css";

export interface TaskGraphViewProps {
  sessionId: string;
  routerClient: RouterClient;
  tasks: Task[];
  tasksById: Record<string, Task>;
  /**
   * Opt-in — no live run-state subscription (and no EventSource) is ever
   * created without it. Only used when the caller doesn't hand over the run
   * itself (`mode` unset): then a live run shows as `live`.
   */
  pipelineClient?: PipelineClient;
  /** Plan clicks, and every click outside a run view, select the task (the detail sheet). */
  onSelectTask?: (task: Task) => void;

  // ── The run to show (the Tasks tab owns it) ──
  /** Set to show a run: Plan (include boxes, waves, critical path) or a live/past run. */
  mode?: RunMode;
  /** Plan: the unticked include boxes. Live/past: the run's excluded_task_ids. */
  excluded?: ReadonlySet<string>;
  /** The run's template stages, in order — ring arcs and track segments. */
  stages?: RunStage[];
  /** The run fold's per-task state. */
  taskRunState?: Record<string, TaskRunState>;
  /** The focus breadcrumb's first crumb, e.g. "Run 4". Defaults to "Run". */
  runLabel?: string;
  onToggleInclude?: (taskId: string) => void;
  /** A blocked task's Edit & retry (opens the detail sheet with the reason pinned). */
  onEditRetry?: (taskId: string) => void;
  onRetry?: (taskId: string) => void;
  /** The task the focus view is zoomed into; controlled by the caller. */
  focusTaskId?: string;
  /** Live/past clicks call this with the task; leaving focus (Esc, the Run crumb, Overview/Cards) with undefined. */
  onFocusTask?: (taskId: string | undefined) => void;
  /** The lens for the focused task, drawn beside the narrowed graph. */
  renderFocus?: (taskId: string) => ReactNode;
}

const EMPTY_RUN_STATE: Record<string, TaskRunState> = {};
const NO_SLOTS: ComponentSlots = new Map();

// Zoom thresholds for switching level as the user zooms (hysteresis between them).
const OVERVIEW_BELOW = 0.6;
const CARDS_ABOVE = 0.9;

type Point = { x: number; y: number };
type Rect = { left: number; right: number; top: number; bottom: number };

export function isPointInRect(point: Point, rect: Rect): boolean {
  return point.x >= rect.left && point.x <= rect.right && point.y >= rect.top && point.y <= rect.bottom;
}

interface PendingDragOut {
  taskId: string;
  plan: EdgeStripPlan;
  previousPosition: Point | undefined;
}

/**
 * `Toast.tsx` has no queue — exactly one can show at a time. Drag-out undo,
 * removal undo, and creation/removal errors all compete for that one slot,
 * so they're a single tagged union rather than parallel useStates.
 */
type GraphToast =
  | { kind: "moved-to-tray"; taskId: string; plan: EdgeStripPlan; previousPosition: Point | undefined }
  | { kind: "dependency-removed"; plan: EdgeStripPlan }
  | { kind: "error"; title: string; message: string };

/**
 * Cycle highlighting is authoritative and server-side (§5) — refetched
 * whenever the tasks prop changes (useTaskStore hands us a new array
 * reference on every task/note event), not derived client-side.
 */
export function TaskGraphView({
  sessionId,
  routerClient,
  tasks,
  tasksById,
  pipelineClient,
  onSelectTask,
  mode,
  excluded,
  stages,
  taskRunState,
  runLabel = "Run",
  onToggleInclude,
  onEditRetry,
  onRetry,
  focusTaskId,
  onFocusTask,
  renderFocus,
}: TaskGraphViewProps) {
  const [cycles, setCycles] = useState<string[][]>([]);
  const [positions, setPositions] = useState<Record<string, Point>>(() => loadPositions(sessionId));
  const previousSlotsRef = useRef<{ height: number; slots: ComponentSlots }>({ height: 0, slots: NO_SLOTS });
  const trayRef = useRef<HTMLDivElement>(null);
  const [pendingDragOut, setPendingDragOut] = useState<PendingDragOut | null>(null);
  const [toast, setToast] = useState<GraphToast | null>(null);
  const [autoLayoutSignal, setAutoLayoutSignal] = useState(0);
  // Fallback until every caller hands over the run: follow the newest run
  // ourselves, and show it as live while it runs.
  const runState = useGraphRunState(sessionId, mode === undefined ? pipelineClient : undefined);
  const runMode: RunMode | undefined = mode ?? (runState?.phase === "running" ? "live" : undefined);
  const runTaskState = taskRunState ?? runState?.taskRunState ?? EMPTY_RUN_STATE;
  const foldColumns = runState?.columns;
  const runStageList = useMemo(() => stages ?? runStages(undefined, foldColumns), [stages, foldColumns]);
  const runColumns = useMemo(() => runStageList.map((s) => s.name), [runStageList]);
  const foldExcluded = runState?.excludedTaskIds;
  const runExcluded = useMemo(() => excluded ?? new Set(foldExcluded ?? []), [excluded, foldExcluded]);
  const runViews = useMemo(
    () =>
      runMode
        ? nodeRunViews(tasks, {
            mode: runMode,
            tasksById,
            excluded: runExcluded,
            stages: runStageList,
            taskRunState: runTaskState,
          })
        : undefined,
    [runMode, tasks, tasksById, runExcluded, runStageList, runTaskState],
  );
  const critical = useMemo(
    () => (runMode === "plan" ? criticalPath(tasks, tasksById, runExcluded) : []),
    [runMode, tasks, tasksById, runExcluded],
  );
  const actions = useMemo<GraphRunActions>(
    () => ({ onToggleInclude, onEditRetry, onRetry }),
    [onToggleInclude, onEditRetry, onRetry],
  );

  const [zoom, setZoom] = useState<GraphLevel>("cards");
  const lastZoomRef = useRef<number | undefined>(undefined);
  const focused = runViews && focusTaskId && tasksById[focusTaskId] ? focusTaskId : undefined;
  const level: GraphLevel = focused ? "overview" : zoom;
  useEscapeLayer(focused !== undefined, () => onFocusTask?.(undefined));
  const reducedMotion = usePrefersReducedMotion();
  const [statusFilter, setStatusFilter] = useState<GraphStatus | null>(null);
  const [statusSpotlight, setStatusSpotlight] = useState<GraphStatus | null>(null);
  // A live or past run speaks in run statuses; the task-status strip is for planning only.
  const showStatusStrip = runMode !== "live" && runMode !== "past";
  const activeStatus = showStatusStrip ? (statusSpotlight ?? statusFilter) : null;
  const statusDimmedIds = activeStatus
    ? new Set(tasks.filter((t) => graphStatusOf(t) !== activeStatus).map((t) => t.id))
    : null;

  function handleToggleStatusFilter(status: GraphStatus) {
    setStatusFilter((prev) => (prev === status ? null : status));
  }

  useEffect(() => {
    let cancelled = false;
    routerClient.taskCycles(sessionId).then((c) => {
      if (!cancelled) setCycles(c);
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, tasks]);

  // Re-seed from storage on session change (also fires harmlessly on mount,
  // re-reading the same data the lazy useState initializer already loaded).
  useEffect(() => {
    setPositions(loadPositions(sessionId));
    previousSlotsRef.current = { height: 0, slots: NO_SLOTS };
  }, [sessionId]);

  useEffect(() => {
    savePositions(sessionId, positions);
  }, [sessionId, positions]);

  // One slot height for every level, so zooming between levels never moves a node.
  const nodeHeight = graphNodeHeight(runViews !== undefined);
  // Slots packed at another height would overlap at this one; re-pack instead.
  const reusableSlots = previousSlotsRef.current.height === nodeHeight ? previousSlotsRef.current.slots : NO_SLOTS;
  const result = computeGraphLayout(tasks, cycles, positions, reusableSlots, nodeHeight);

  // Updated after render commits, never mutated during render (§7).
  useEffect(() => {
    previousSlotsRef.current = { height: nodeHeight, slots: result.slots };
  }, [nodeHeight, result.slots]);

  const edgeDegree = new Map<string, number>();
  for (const edge of result.edges) {
    edgeDegree.set(edge.from, (edgeDegree.get(edge.from) ?? 0) + 1);
    edgeDegree.set(edge.to, (edgeDegree.get(edge.to) ?? 0) + 1);
  }
  const unsequencedIds = new Set(
    result.canvasNodes.filter((n) => (edgeDegree.get(n.id) ?? 0) === 0).map((n) => n.id),
  );
  const cycleNodeIds = new Set(cycles.flat());

  const trayTasks = trayOrder(result.trayTaskIds.map((id) => tasksById[id]));

  let edgeKinds: Record<string, EdgeRunKind> | undefined;
  if (runMode && runViews) {
    const criticalIds = criticalEdgeIds(critical);
    edgeKinds = {};
    for (const edge of result.edges) {
      const kind = edgeRunKindOf(edge, runMode, runViews, criticalIds);
      if (kind) edgeKinds[edge.id] = kind;
    }
  }

  // Focus without a click: the task needing attention most (running, then blocked).
  const focusCandidate =
    runViews && runMode !== "plan"
      ? (tasks.find((t) => runViews[t.id]?.status === "running") ??
        tasks.find((t) => runViews[t.id]?.status === "blocked") ??
        tasks.find((t) => runTaskState[t.id]))
      : undefined;

  function handleZoomLevel(next: ZoomLevel) {
    if (next === "focus") {
      if (focusCandidate) onFocusTask?.(focusCandidate.id);
      return;
    }
    setZoom(next);
    if (focused) onFocusTask?.(undefined);
  }

  function handleUserZoom(z: number) {
    const previous = lastZoomRef.current;
    lastZoomRef.current = z;
    if (!runViews || focused || previous === undefined) return;
    if (previous >= OVERVIEW_BELOW && z < OVERVIEW_BELOW) setZoom("overview");
    else if (previous <= CARDS_ABOVE && z > CARDS_ABOVE) setZoom("cards");
  }

  function handleSelectTask(task: Task) {
    if (runViews && runMode !== "plan" && onFocusTask) onFocusTask(task.id);
    else onSelectTask?.(task);
  }

  const legend =
    runMode === "plan" && critical.length >= 2 ? (
      <span className={styles.legend}>
        <i className={styles.legendSwatch} />
        critical path ({critical.length} waves)
      </span>
    ) : undefined;

  const focusTask = focused ? tasksById[focused] : undefined;
  const neighbours = focused ? neighboursOf(focused, tasks, tasksById) : undefined;

  function finishDragOut(taskId: string, plan: EdgeStripPlan, previousPosition: Point | undefined) {
    setPositions((prev) => {
      const next = { ...prev };
      delete next[taskId];
      return next;
    });
    if (plan.updates.length > 0) {
      void applyEdgeStrip(routerClient, plan);
    }
    setToast({ kind: "moved-to-tray", taskId, plan, previousPosition });
  }

  function handleDragNodeOut(taskId: string) {
    const previousPosition = positions[taskId];
    const plan = planEdgeStrip(taskId, tasksById);
    if (plan.updates.length === 0) {
      finishDragOut(taskId, plan, previousPosition);
      return;
    }
    setPendingDragOut({ taskId, plan, previousPosition });
  }

  function handleNodeDragStop(taskId: string, clientPoint: Point, flowPosition: Point) {
    const trayEl = trayRef.current;
    if (trayEl && isPointInRect(clientPoint, trayEl.getBoundingClientRect())) {
      handleDragNodeOut(taskId);
      return;
    }
    setPositions((prev) => ({ ...prev, [taskId]: flowPosition }));
  }

  function handleDropOnCanvas(taskId: string, flowPosition: Point) {
    setPositions((prev) => ({ ...prev, [taskId]: flowPosition }));
  }

  function handleConfirmDragOut() {
    if (!pendingDragOut) return;
    const { taskId, plan, previousPosition } = pendingDragOut;
    setPendingDragOut(null);
    finishDragOut(taskId, plan, previousPosition);
  }

  function handleCancelDragOut() {
    setPendingDragOut(null);
  }

  function handleUndoToast() {
    if (!toast || toast.kind === "error") return;
    if (toast.kind === "moved-to-tray") {
      const { taskId, plan, previousPosition } = toast;
      if (plan.updates.length > 0) void applyEdgeStrip(routerClient, invertPlan(plan));
      if (previousPosition) {
        setPositions((prev) => ({ ...prev, [taskId]: previousPosition }));
      }
    } else if (toast.kind === "dependency-removed") {
      void applyEdgeStrip(routerClient, invertPlan(toast.plan));
    }
    setToast(null);
  }

  async function handleConnect(source: string, target: string) {
    const plan = planEdgeCreation(source, target, tasksById);
    if (!plan) return; // self-loop or duplicate — silent no-op, not an error
    try {
      await applyEdgeStrip(routerClient, plan);
    } catch (e) {
      setToast({
        kind: "error",
        title: "Couldn't create dependency",
        message: e instanceof Error ? e.message : "Unknown error",
      });
    }
  }

  async function handleDeleteEdges(pairs: { from: string; to: string }[]) {
    const plan = planEdgesRemoval(pairs, tasksById);
    if (plan.updates.length === 0) return;
    try {
      await applyEdgeStrip(routerClient, plan);
      setToast({ kind: "dependency-removed", plan });
    } catch (e) {
      setToast({
        kind: "error",
        title: "Couldn't remove dependency",
        message: e instanceof Error ? e.message : "Unknown error",
      });
    }
  }

  function handleAutoLayout() {
    setPositions({});
    previousSlotsRef.current = { height: 0, slots: NO_SLOTS };
    setAutoLayoutSignal((n) => n + 1);
  }

  const toastTitle =
    toast?.kind === "moved-to-tray"
      ? "Moved to the unsequenced tray"
      : toast?.kind === "dependency-removed"
        ? "Dependency removed"
        : (toast?.title ?? "");

  return (
    <div className={[styles.wrapper, focused && styles.focus].filter(Boolean).join(" ")}>
      {!focused && (
        <>
          <UnsequencedTray
            tasks={trayTasks}
            tasksById={tasksById}
            containerRef={trayRef}
            reducedMotion={reducedMotion}
            mode={runMode}
            runViews={runViews}
            onFocusTask={runViews && runMode !== "plan" && onFocusTask ? (id) => onFocusTask(id) : undefined}
          />
          {showStatusStrip && (
            <StatusFilterStrip
              tasks={tasks}
              activeFilter={statusFilter}
              onHoverStatus={setStatusSpotlight}
              onToggleFilter={handleToggleStatusFilter}
            />
          )}
        </>
      )}
      <div className={styles.body}>
        <div className={styles.canvas}>
          {cycles.length > 0 && (
            <div className={styles.cycleBanner} role="status">
              cycle — the pipeline will refuse to launch these
            </div>
          )}
          <GraphRunActionsContext.Provider value={actions}>
            <GraphCanvas
              canvasNodes={result.canvasNodes}
              edges={result.edges}
              tasksById={tasksById}
              unsequencedIds={unsequencedIds}
              cycleNodeIds={cycleNodeIds}
              taskRunState={runTaskState}
              columns={runColumns}
              statusDimmedIds={statusDimmedIds}
              runViews={runViews}
              edgeKinds={edgeKinds}
              level={level}
              focusTaskId={focused}
              legend={legend}
              onUserZoom={handleUserZoom}
              refitSignal={focused ? 1 : 0}
              onSelectTask={handleSelectTask}
              onNodeDragStop={handleNodeDragStop}
              onDropOnCanvas={handleDropOnCanvas}
              onAutoLayout={handleAutoLayout}
              extent={result.extent}
              autoLayoutSignal={autoLayoutSignal}
              onConnect={handleConnect}
              onDeleteEdges={handleDeleteEdges}
            />
          </GraphRunActionsContext.Provider>
          {runViews && (
            <ZoomLevelControl
              value={focused ? "focus" : zoom}
              onChange={handleZoomLevel}
              focusDisabled={!onFocusTask || !focusCandidate}
              compact={!!focused}
            />
          )}
        </div>
        {focusTask && neighbours && (
          <FocusPanel
            task={focusTask}
            runLabel={runLabel}
            currentStage={currentStageOf(runTaskState[focusTask.id])}
            upstream={neighbours.upstream}
            unblocks={neighbours.unblocks}
            statusOf={(id) => runViews?.[id]?.status}
            onBack={() => onFocusTask?.(undefined)}
            onFocusTask={(id) => onFocusTask?.(id)}
          >
            {renderFocus?.(focusTask.id)}
          </FocusPanel>
        )}
      </div>
      <ConfirmDialog
        open={pendingDragOut !== null}
        title="Remove this task's dependencies?"
        description="Moving this task to the tray clears its dependency edges in both directions. You can undo right after."
        confirmLabel="Remove edges"
        onConfirm={handleConfirmDragOut}
        onCancel={handleCancelDragOut}
      />
      {toast && (
        <div className={styles.toast}>
          <Toast
            tone={toast.kind === "error" ? "bad" : "neutral"}
            title={toastTitle}
            description={toast.kind === "error" ? toast.message : undefined}
            action={toast.kind === "error" ? undefined : { label: "Undo", onClick: handleUndoToast }}
            onDismiss={() => setToast(null)}
          />
        </div>
      )}
    </div>
  );
}
