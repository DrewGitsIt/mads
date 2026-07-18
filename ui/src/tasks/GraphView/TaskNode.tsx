import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { Card } from "../../components/Card/Card";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import { blockedReason, graphStatusAccessibleLabel, graphStatusOf } from "./graphStatus";
import { StatusGlyph } from "./StatusGlyph";
import styles from "./TaskNode.module.css";

export type TaskNodeData = {
  task: Task;
  tasksById: Record<string, Task>;
  /** This task's own run-fold state, already looked up by the caller. */
  taskRunState?: TaskRunState;
  /** Ordered stage ids for the live run — present only while a run is live. */
  columns?: string[];
  reducedMotion: boolean;
  /** Computed once in TaskGraphView from edge degree — don't recompute here. */
  unsequenced: boolean;
  /** Computed once in TaskGraphView from GET /tasks/cycles — don't recompute here. */
  inCycle: boolean;
  /** True when a highlight-mode selection or status filter/spotlight excludes this node. */
  dimmed: boolean;
};

export type TaskNodeType = Node<TaskNodeData, "taskNode">;

export function TaskNode({ data }: NodeProps<TaskNodeType>) {
  const { task, tasksById, taskRunState, columns, reducedMotion, unsequenced, inCycle, dimmed } = data;
  const status = graphStatusOf(task);
  const label = graphStatusAccessibleLabel(task, tasksById);
  const reason =
    status === "blocked"
      ? blockedReason(task, tasksById, taskRunState ? { [task.id]: taskRunState } : undefined)
      : undefined;
  const ariaLabel = reason ? `${task.title} — ${label}, ${reason}` : `${task.title} — ${label}`;

  const cardClassName = [styles.card, styles[`status_${status}`], inCycle && styles.inCycle, dimmed && styles.dimmed]
    .filter(Boolean)
    .join(" ");

  const currentStage = taskRunState?.currentStage;
  const currentIndex = currentStage ? (columns?.indexOf(currentStage) ?? -1) : -1;

  return (
    <Card className={cardClassName} aria-label={ariaLabel} title={label}>
      <Handle type="target" position={Position.Top} className={styles.handleIn} />
      {unsequenced && (
        <span className={styles.unsequenced} title="No dependencies or dependents">
          unsequenced
        </span>
      )}
      <div className={styles.title} title={task.title}>
        {task.title}
      </div>
      {status === "in_progress" && columns && columns.length > 0 && (
        <div className={styles.pips}>
          {columns.map((stage, index) => {
            const isNow = stage === currentStage;
            const isPast = !isNow && currentIndex !== -1 && index < currentIndex;
            return (
              <span
                key={stage}
                className={[styles.pip, isPast && styles.pipPast, isNow && styles.pipNow]
                  .filter(Boolean)
                  .join(" ")}
              />
            );
          })}
          {currentStage && <span className={styles.pipLabel}>{currentStage}</span>}
        </div>
      )}
      {status === "blocked" && reason && <div className={styles.reason}>{reason}</div>}
      <div className={styles.footer}>
        <StatusGlyph status={status} animate={status === "in_progress" && !reducedMotion} />
        {task.retry_count > 0 && <span className={styles.retry}>retry ×{task.retry_count}</span>}
      </div>
      <Handle type="source" position={Position.Bottom} className={styles.handleOut} />
    </Card>
  );
}
