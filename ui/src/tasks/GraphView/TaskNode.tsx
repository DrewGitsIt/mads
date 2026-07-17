import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import { statusPresentation } from "../ListView/statusPresentation";
import styles from "./TaskNode.module.css";

export type TaskNodeData = {
  task: Task;
  tasksById: Record<string, Task>;
  /** Computed once in TaskGraphView from edge degree — don't recompute here. */
  unsequenced: boolean;
  /** Computed once in TaskGraphView from GET /tasks/cycles — don't recompute here. */
  inCycle: boolean;
  /** True when a highlight-mode selection is active and this node isn't in the selected node's ancestor/descendant set. */
  dimmed: boolean;
};

export type TaskNodeType = Node<TaskNodeData, "taskNode">;

const TONE_VAR: Record<string, string> = {
  neutral: "var(--line)",
  ok: "var(--ok)",
  warn: "var(--warn)",
  bad: "var(--bad)",
  info: "var(--info)",
  draft: "var(--draft)",
};

export function TaskNode({ data }: NodeProps<TaskNodeType>) {
  const { task, tasksById, unsequenced, inCycle, dimmed } = data;
  const presentation = statusPresentation(task, tasksById);
  const borderColor = TONE_VAR[presentation.chipTone] ?? TONE_VAR.neutral;
  const cardClassName = [styles.card, inCycle && styles.inCycle, dimmed && styles.dimmed]
    .filter(Boolean)
    .join(" ");

  return (
    <Card className={cardClassName} style={{ borderColor }}>
      <Handle type="target" position={Position.Top} className={styles.handleIn} />
      {unsequenced && (
        <span className={styles.unsequenced} title="No dependencies or dependents">
          unsequenced
        </span>
      )}
      <div className={styles.title} title={task.title}>
        {task.title}
      </div>
      <div className={styles.footer}>
        <Chip tone={presentation.chipTone}>{presentation.label}</Chip>
        {task.retry_count > 0 && <span className={styles.retry}>retry ×{task.retry_count}</span>}
      </div>
      <Handle type="source" position={Position.Bottom} className={styles.handleOut} />
    </Card>
  );
}
