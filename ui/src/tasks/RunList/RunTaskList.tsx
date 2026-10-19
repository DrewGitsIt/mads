import type { KeyboardEvent, MouseEvent } from "react";
import type { Task } from "../../router/types";
import {
  interruptedIds,
  pickupLine,
  PLAN_GROUPS,
  RUN_GROUPS,
  runStatusOf,
  type PickupInfo,
  type RunMode,
  type RunStatus,
} from "../../pipeline/plan/runStatus";
import { leftOutBy } from "../../pipeline/plan/planScope";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import { Button } from "../../components/Button/Button";
import { Chip, type ChipTone } from "../../components/Chip/Chip";
import { currentAttempt } from "../Metro/metroLayout";
import { RunStatusGlyph } from "./RunStatusGlyph";
import styles from "./RunTaskList.module.css";

export interface RunTaskListProps {
  tasks: Task[];
  tasksById: Record<string, Task>;
  mode: RunMode;
  /** Plan: the include boxes left unticked. Live/past: the run's excluded_task_ids. */
  excluded: ReadonlySet<string>;
  taskRunState: Record<string, TaskRunState>;
  /** Plan and live: the session's Interrupted tasks and where a run picks each up. */
  pickup?: PickupInfo;
  focusTaskId?: string;
  onSelectTask: (id: string) => void;
  /** Why each blocked task is blocked, by task id (the blocker note or stage.failed reason). */
  blockReasons?: Record<string, string>;
  /** Plan: tick or untick a task's include box. */
  onToggleInclude?: (id: string) => void;
  /** Plan: confirm a draft so it can run. */
  onConfirmDraft?: (id: string) => void;
  onRetry?: (id: string) => void;
  onEditTask?: (id: string) => void;
  onAddTask?: () => void;
  /** A secondary action beside + Add task; hidden when absent. */
  onBulkImport?: () => void;
}

const CHIP: Record<RunStatus, { label: string; tone: ChipTone }> = {
  ready: { label: "Ready", tone: "neutral" },
  queued: { label: "Queued", tone: "neutral" },
  running: { label: "Running", tone: "info" },
  interrupted: { label: "Interrupted", tone: "warn" },
  blocked: { label: "Blocked", tone: "bad" },
  done: { label: "Done", tone: "ok" },
  skipped: { label: "Not in this run", tone: "neutral" },
  excluded: { label: "Left out", tone: "neutral" },
  draft: { label: "Draft", tone: "draft" },
};

type SubTone = "bad" | "warn" | undefined;

function subLine(
  task: Task,
  status: RunStatus,
  props: Pick<RunTaskListProps, "mode" | "tasksById" | "excluded" | "taskRunState" | "blockReasons" | "pickup">,
): { text: string; tone?: SubTone } {
  const { mode, tasksById, excluded, taskRunState, blockReasons, pickup } = props;
  const rs = taskRunState[task.id];
  const interrupted = interruptedIds(pickup);
  switch (status) {
    case "running": {
      if (!rs?.currentStage) return { text: rs?.pickup ? "picked up" : "starting" };
      return { text: `${rs.currentStage} · attempt ${Math.max(1, currentAttempt(rs))}` };
    }
    case "interrupted": {
      // Past: where it was when the run ended. Plan and live: where a run picks it up.
      if (mode === "past") {
        return { text: rs?.currentStage ? `${rs.currentStage} · attempt ${Math.max(1, currentAttempt(rs))}` : "" };
      }
      return { text: pickupLine(task.id, pickup) ?? "" };
    }
    case "blocked": {
      const lastFailed = [...(rs?.attempts ?? [])].reverse().find((a) => a.status === "failed");
      return { text: blockReasons?.[task.id] ?? lastFailed?.reason ?? "blocked", tone: "bad" };
    }
    case "queued": {
      const waitingOn = (task.dependencies ?? []).map((id) => tasksById[id]).find((d) => d && d.status !== "done");
      return { text: waitingOn ? `waits on ${waitingOn.title}` : "queued" };
    }
    case "draft":
      return { text: "draft: confirm it before it can run" };
    case "excluded":
      return { text: "left out of this run" };
    case "skipped": {
      const cut = leftOutBy(task, tasksById, excluded, interrupted);
      if (cut) {
        const why = cut.draft || excluded.has(cut.id) ? "which is left out" : "which can't run";
        return { text: `${mode === "past" ? "didn't run" : "won't run"}: depends on ${cut.title}, ${why}`, tone: "warn" };
      }
      if (mode === "past") return { text: "didn't run" };
      if (task.status === "in_progress") return { text: "won't run: already in progress" };
      if (task.status === "needs_review") return { text: "won't run: needs review" };
      if (task.status === "wont_do") return { text: "won't do" };
      return { text: "won't run" };
    }
    default:
      // ready, done
      return { text: (task.labels ?? []).join(" · ") };
  }
}

// Clicks on a row's controls act on the control only, not the row.
const stop = (e: MouseEvent | KeyboardEvent) => e.stopPropagation();

/** The run's tasks, grouped by where they stand, under the metro. */
export function RunTaskList(props: RunTaskListProps) {
  const { tasks, tasksById, mode, excluded, taskRunState, pickup, onAddTask, onBulkImport } = props;
  const groups = mode === "plan" ? PLAN_GROUPS : RUN_GROUPS;
  const ctx = { mode, tasksById, excluded, taskRunState, interrupted: interruptedIds(pickup) };
  const statusById = new Map(tasks.map((t) => [t.id, runStatusOf(t, ctx)]));

  return (
    <div className={styles.list}>
      {groups.map((group) => {
        const members = tasks.filter((t) => group.statuses.includes(statusById.get(t.id)!));
        if (members.length === 0) return null;
        const headingId = `run-group-${group.label.replace(/\s+/g, "-").toLowerCase()}`;
        return (
          <section key={group.label} aria-labelledby={headingId}>
            <h3 id={headingId} className={[styles.groupHead, group.label === "Blocked" && styles.bad].filter(Boolean).join(" ")}>
              {group.label}
              <span className={styles.count}>{members.length}</span>
            </h3>
            <ul className={styles.rows}>
              {members.map((task) => (
                <Row key={task.id} task={task} status={statusById.get(task.id)!} {...props} />
              ))}
            </ul>
          </section>
        );
      })}
      {(onAddTask || onBulkImport) && (
        <div className={styles.addLine}>
          {onAddTask && (
            <button type="button" className={styles.addRow} onClick={onAddTask}>
              <span>+ Add task</span>
              {mode === "live" && <i className={styles.hint}>A new ready task is picked up by this run</i>}
            </button>
          )}
          {onBulkImport && (
            <Button size="sm" variant="ghost" onClick={onBulkImport}>
              Bulk import
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

function Row({ task, status, ...props }: RunTaskListProps & { task: Task; status: RunStatus }) {
  const { mode, excluded, focusTaskId, onSelectTask, onToggleInclude, onConfirmDraft, onRetry, onEditTask } = props;
  const plan = mode === "plan";
  const sub = subLine(task, status, props);
  const leftOut = status === "excluded" || status === "draft" || status === "skipped";
  const included = !task.draft && !excluded.has(task.id);

  return (
    <li
      className={[
        styles.row,
        plan && styles.plan,
        leftOut && styles.dimmed,
        focusTaskId === task.id && styles.focus,
      ]
        .filter(Boolean)
        .join(" ")}
      data-task={task.id}
      data-status={status}
      onClick={() => onSelectTask(task.id)}
    >
      {plan &&
        (task.draft ? (
          <span
            role="checkbox"
            aria-checked={false}
            aria-disabled={true}
            aria-label={`Include ${task.title}`}
            className={[styles.include, styles.na].join(" ")}
            title="Drafts can't run until confirmed"
            onClick={stop}
          />
        ) : (
          <button
            type="button"
            role="checkbox"
            aria-checked={included}
            aria-label={`Include ${task.title}`}
            title="Include in this run"
            className={[styles.include, included && styles.on].filter(Boolean).join(" ")}
            onClick={(e) => {
              stop(e);
              onToggleInclude?.(task.id);
            }}
          >
            {included ? "✓" : ""}
          </button>
        ))}
      <RunStatusGlyph status={status} />
      <div className={styles.main}>
        <button
          type="button"
          className={styles.title}
          onClick={(e) => {
            stop(e);
            onSelectTask(task.id);
          }}
        >
          {task.title}
        </button>
        {sub.text && (
          <span className={[styles.sub, sub.tone && styles[sub.tone]].filter(Boolean).join(" ")}>{sub.text}</span>
        )}
      </div>
      {!plan && (
        <Chip tone={CHIP[status].tone}>
          {status === "running" && <span className={styles.spin} aria-hidden="true" />}
          {CHIP[status].label}
        </Chip>
      )}
      <span className={styles.actions}>
        {plan && task.draft && onConfirmDraft && (
          <Button
            size="sm"
            onClick={(e) => {
              stop(e);
              onConfirmDraft(task.id);
            }}
          >
            Confirm
          </Button>
        )}
        {status === "running" ? (
          <span className={styles.lock} title="Locked while the pipeline works on it" role="img" aria-label="Locked while the pipeline works on it">
            🔒
          </span>
        ) : status === "blocked" && onRetry ? (
          <Button
            size="sm"
            variant="primary"
            onClick={(e) => {
              stop(e);
              onRetry(task.id);
            }}
          >
            Retry
          </Button>
        ) : (
          onEditTask && (
            <Button
              size="sm"
              variant="ghost"
              aria-label={`Edit ${task.title}`}
              title="Edit"
              onClick={(e) => {
                stop(e);
                onEditTask(task.id);
              }}
            >
              ✎
            </Button>
          )
        )}
      </span>
    </li>
  );
}
