import type { ReactNode } from "react";
import { Chip } from "../../components/Chip/Chip";
import type { RunStatus } from "../../pipeline/plan/runStatus";
import type { Task } from "../../router/types";
import { RUN_STATUS_TONE } from "./runView";
import styles from "./FocusPanel.module.css";

export interface FocusPanelProps {
  task: Task;
  /** The breadcrumb's first crumb, e.g. "Run 4". */
  runLabel: string;
  currentStage?: string;
  upstream: Task[];
  unblocks: Task[];
  statusOf: (taskId: string) => RunStatus | undefined;
  onBack: () => void;
  onFocusTask: (taskId: string) => void;
  /** The lens for the focused task. */
  children?: ReactNode;
}

function Neighbours({
  tasks,
  statusOf,
  onFocusTask,
}: Pick<FocusPanelProps, "statusOf" | "onFocusTask"> & { tasks: Task[] }) {
  if (tasks.length === 0) return <span>none</span>;
  return (
    <>
      {tasks.map((t) => (
        <Chip key={t.id} tone={RUN_STATUS_TONE[statusOf(t.id) ?? "queued"]} onClick={() => onFocusTask(t.id)}>
          {t.title}
        </Chip>
      ))}
    </>
  );
}

/** The side of the focus view: breadcrumb, the lens, and chips to the task's neighbours. */
export function FocusPanel({
  task,
  runLabel,
  currentStage,
  upstream,
  unblocks,
  statusOf,
  onBack,
  onFocusTask,
  children,
}: FocusPanelProps) {
  return (
    <section className={styles.panel} aria-label={`Focus: ${task.title}`}>
      <nav className={styles.crumbs} aria-label="Breadcrumb">
        <button type="button" className={styles.crumbLink} onClick={onBack}>
          {runLabel}
        </button>
        <span aria-hidden="true">›</span>
        <b className={styles.crumbTask}>{task.title}</b>
        {currentStage && (
          <>
            <span aria-hidden="true">›</span>
            <span>{currentStage}</span>
          </>
        )}
        <span className={styles.spacer} />
        <span className={styles.escHint}>
          <kbd className={styles.kbd}>Esc</kbd> back to graph
        </span>
      </nav>
      <div className={styles.lens}>{children}</div>
      <div className={styles.neighbours}>
        <span>Upstream:</span>
        <Neighbours tasks={upstream} statusOf={statusOf} onFocusTask={onFocusTask} />
        <span aria-hidden="true">·</span>
        <span>Unblocks:</span>
        <Neighbours tasks={unblocks} statusOf={statusOf} onFocusTask={onFocusTask} />
      </div>
    </section>
  );
}
