import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import type { MouseEvent } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import { useGraphRunActions } from "./graphRunActions";
import { blockedReason, graphStatusAccessibleLabel, graphStatusOf } from "./graphStatus";
import { RUN_STATUS_LABEL, stageStatesOf, type NodeRunView } from "./runView";
import { StageRing } from "./StageRing";
import { StageTrack } from "./StageTrack";
import { StatusGlyph } from "./StatusGlyph";
import styles from "./TaskNode.module.css";

/** Overview draws rings, Cards draws cards; Focus is Overview with one node ringed. */
export type GraphLevel = "overview" | "cards";

export type TaskNodeData = {
  task: Task;
  tasksById: Record<string, Task>;
  /** This task's own run-fold state, already looked up by the caller. */
  taskRunState?: TaskRunState;
  /** Ordered stage ids for the observed run — the stage track's segments when no run view is given. */
  columns?: string[];
  reducedMotion: boolean;
  /** Computed once in TaskGraphView from edge degree — don't recompute here. */
  unsequenced: boolean;
  /** Computed once in TaskGraphView from GET /tasks/cycles — don't recompute here. */
  inCycle: boolean;
  /** True when a highlight-mode selection or status filter/spotlight excludes this node. */
  dimmed: boolean;
  /** What this task looks like in the run being shown; absent outside a run view. */
  run?: NodeRunView;
  /** Defaults to cards. Only honoured with a run view (a ring needs stages). */
  level?: GraphLevel;
  /** The task the focus view is zoomed into. */
  focused?: boolean;
};

export type TaskNodeType = Node<TaskNodeData, "taskNode">;

// Controls inside a node must not start a drag or count as a node click.
function stop(e: MouseEvent) {
  e.stopPropagation();
}

export function TaskNode({ data }: NodeProps<TaskNodeType>) {
  if (data.run && data.level === "overview") return <RingNode data={data} run={data.run} />;
  if (data.run) return <RunCard data={data} run={data.run} />;
  return <LegacyCard data={data} />;
}

function RingNode({ data, run }: { data: TaskNodeData; run: NodeRunView }) {
  const { task, focused, dimmed } = data;
  return (
    <div
      className={[styles.ringNode, styles[`ring_${run.status}`], dimmed && styles.dimmed].filter(Boolean).join(" ")}
      aria-label={`${task.title} — ${RUN_STATUS_LABEL[run.status]}`}
      title={task.title}
      data-focused={focused || undefined}
    >
      <Handle type="target" position={Position.Top} className={styles.handleIn} />
      <StageRing stages={run.stages} status={run.status} focused={focused} />
      <span className={styles.ringLabel}>{task.title}</span>
      <Handle type="source" position={Position.Bottom} className={styles.handleOut} />
    </div>
  );
}

function RunCard({ data, run }: { data: TaskNodeData; run: NodeRunView }) {
  const { task, reducedMotion, unsequenced, inCycle, dimmed } = data;
  const actions = useGraphRunActions();
  const { status } = run;
  const label = RUN_STATUS_LABEL[status];
  const ariaLabel = run.line ? `${task.title} — ${label}, ${run.line.text}` : `${task.title} — ${label}`;
  const showTrack = status !== "draft" && status !== "skipped" && status !== "excluded";
  const cardClassName = [styles.card, styles[`run_${status}`], inCycle && styles.inCycle, dimmed && styles.dimmed]
    .filter(Boolean)
    .join(" ");

  return (
    <Card className={cardClassName} aria-label={ariaLabel} title={label}>
      <Handle type="target" position={Position.Top} className={styles.handleIn} />
      {unsequenced && (
        <span className={styles.unsequenced} title="No dependencies or dependents">
          unsequenced
        </span>
      )}
      <div className={styles.top}>
        {run.include && (
          <input
            type="checkbox"
            className={`nodrag ${styles.include}`}
            checked={run.include.checked}
            disabled={run.include.disabled}
            title={run.include.reason ?? "Include in this run"}
            aria-label={`Include ${task.title} in this run`}
            onClick={stop}
            onChange={() => actions.onToggleInclude?.(task.id)}
          />
        )}
        <div className={styles.title} title={task.title}>
          {task.title}
        </div>
      </div>
      {showTrack && <StageTrack stages={run.stages} />}
      {run.wave !== undefined && (
        <span className={styles.wave} title={run.skips.length > 0 ? `skips ${run.skips.join(", ")}` : undefined}>
          wave {run.wave}
          {run.skips.length > 0 && ` · skips ${run.skips.join(", ")}`}
        </span>
      )}
      {run.line && (
        <div className={[styles.line, styles[`line_${run.line.tone}`]].join(" ")}>
          {run.line.tone === "running" && <StatusGlyph status="in_progress" animate={!reducedMotion} />}
          <span title={run.line.text}>{run.line.text}</span>
        </div>
      )}
      {run.retryable && (actions.onEditRetry || actions.onRetry) && (
        <div className={styles.actions}>
          {actions.onEditRetry && (
            <Button
              variant="primary"
              size="sm"
              className="nodrag"
              onClick={(e) => {
                stop(e);
                actions.onEditRetry?.(task.id);
              }}
            >
              Edit &amp; retry
            </Button>
          )}
          {actions.onRetry && (
            <Button
              variant="secondary"
              size="sm"
              className="nodrag"
              onClick={(e) => {
                stop(e);
                actions.onRetry?.(task.id);
              }}
            >
              Retry
            </Button>
          )}
        </div>
      )}
      <Handle type="source" position={Position.Bottom} className={styles.handleOut} />
    </Card>
  );
}

/** The card outside a run view: graph status treatments, plus a stage track while in progress. */
function LegacyCard({ data }: { data: TaskNodeData }) {
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
  const showTrack = status === "in_progress" && columns && columns.length > 0;

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
      {showTrack && (
        <div className={styles.legacyTrack}>
          <StageTrack
            stages={stageStatesOf(
              task,
              "running",
              "live",
              columns.map((name) => ({ name, kind: "agent" })),
              taskRunState,
            )}
          />
          {currentStage && <span className={styles.stageLabel}>{currentStage}</span>}
        </div>
      )}
      {status === "blocked" && reason && (
        <div className={styles.reason} title={reason}>
          {reason}
        </div>
      )}
      <div className={styles.footer}>
        <StatusGlyph status={status} animate={status === "in_progress" && !reducedMotion} />
        {task.retry_count > 0 && <span className={styles.retry}>retry ×{task.retry_count}</span>}
      </div>
      <Handle type="source" position={Position.Bottom} className={styles.handleOut} />
    </Card>
  );
}
