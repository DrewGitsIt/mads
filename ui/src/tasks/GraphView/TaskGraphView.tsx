import { useEffect, useRef, useState } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog/ConfirmDialog";
import { Toast } from "../../components/Toast/Toast";
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
import { GraphCanvas } from "./GraphCanvas";
import { computeGraphLayout, type ComponentSlots } from "./layout";
import { loadPositions, savePositions } from "./positions";
import { trayOrder } from "./tray";
import { UnsequencedTray } from "./UnsequencedTray";
import styles from "./TaskGraphView.module.css";

export interface TaskGraphViewProps {
  sessionId: string;
  routerClient: RouterClient;
  tasks: Task[];
  tasksById: Record<string, Task>;
  onSelectTask?: (task: Task) => void;
}

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
  onSelectTask,
}: TaskGraphViewProps) {
  const [cycles, setCycles] = useState<string[][]>([]);
  const [positions, setPositions] = useState<Record<string, Point>>(() => loadPositions(sessionId));
  const previousSlotsRef = useRef<ComponentSlots>(new Map());
  const trayRef = useRef<HTMLDivElement>(null);
  const [pendingDragOut, setPendingDragOut] = useState<PendingDragOut | null>(null);
  const [toast, setToast] = useState<GraphToast | null>(null);
  const [autoLayoutSignal, setAutoLayoutSignal] = useState(0);

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
    previousSlotsRef.current = new Map();
  }, [sessionId]);

  useEffect(() => {
    savePositions(sessionId, positions);
  }, [sessionId, positions]);

  const result = computeGraphLayout(tasks, cycles, positions, previousSlotsRef.current);

  // Updated after render commits, never mutated during render (§7).
  useEffect(() => {
    previousSlotsRef.current = result.slots;
  }, [result.slots]);

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
    previousSlotsRef.current = new Map();
    setAutoLayoutSignal((n) => n + 1);
  }

  const toastTitle =
    toast?.kind === "moved-to-tray"
      ? "Moved to the unsequenced tray"
      : toast?.kind === "dependency-removed"
        ? "Dependency removed"
        : (toast?.title ?? "");

  return (
    <div className={styles.wrapper}>
      <UnsequencedTray tasks={trayTasks} tasksById={tasksById} containerRef={trayRef} />
      <div className={styles.canvas}>
        {cycles.length > 0 && (
          <div className={styles.cycleBanner} role="status">
            cycle — the pipeline will refuse to launch these
          </div>
        )}
        <GraphCanvas
          canvasNodes={result.canvasNodes}
          edges={result.edges}
          tasksById={tasksById}
          unsequencedIds={unsequencedIds}
          cycleNodeIds={cycleNodeIds}
          onSelectTask={onSelectTask}
          onNodeDragStop={handleNodeDragStop}
          onDropOnCanvas={handleDropOnCanvas}
          onAutoLayout={handleAutoLayout}
          extent={result.extent}
          autoLayoutSignal={autoLayoutSignal}
          onConnect={handleConnect}
          onDeleteEdges={handleDeleteEdges}
        />
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
