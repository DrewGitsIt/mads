import type { KeyboardEvent, RefObject } from "react";
import { Chip } from "../../components/Chip/Chip";
import type { RunMode, RunStatus } from "../../pipeline/plan/runStatus";
import type { Task } from "../../router/types";
import { statusPresentation } from "../ListView/statusPresentation";
import { graphStatusOf } from "./graphStatus";
import { RUN_STATUS_LABEL, RUN_STATUS_TONE, type NodeRunView } from "./runView";
import { StatusGlyph } from "./StatusGlyph";
import { TASK_ID_DRAG_TYPE, trayOrder } from "./tray";
import styles from "./UnsequencedTray.module.css";

export interface UnsequencedTrayProps {
  tasks: Task[];
  tasksById: Record<string, Task>;
  /** Owned by TaskGraphView so it can hit-test canvas-drop points against this box (§7). */
  containerRef: RefObject<HTMLDivElement>;
  reducedMotion?: boolean;
  /** Set while the graph shows a run. A past run's caption no longer promises a slot. */
  mode?: RunMode;
  /** Set with `mode`: cards speak in run statuses (runStatusOf), like the nodes. */
  runViews?: Record<string, NodeRunView>;
  /** Live/past: a card click (or Enter) focuses the task, like a node click. */
  onFocusTask?: (taskId: string) => void;
}

// Finished work and work outside the run recede, as on the canvas.
const RECEDES: ReadonlySet<RunStatus> = new Set(["done", "skipped", "excluded", "draft"]);

export function UnsequencedTray({
  tasks,
  tasksById,
  containerRef,
  reducedMotion = false,
  mode,
  runViews,
  onFocusTask,
}: UnsequencedTrayProps) {
  const ordered = trayOrder(tasks);

  return (
    <div ref={containerRef} className={styles.tray}>
      <div className={styles.caption}>{mode === "past" ? "No dependencies." : "Runs when a slot opens · in this order."}</div>
      <div className={styles.shelf}>
        {ordered.map((task) => {
          const run = runViews?.[task.id];
          const presentation = statusPresentation(task, tasksById);
          const status = graphStatusOf(task);
          const recedes = run ? RECEDES.has(run.status) : status === "done";
          const focus = onFocusTask ? () => onFocusTask(task.id) : undefined;
          return (
            <div
              key={task.id}
              className={[styles.card, recedes && styles.done, focus && styles.clickable].filter(Boolean).join(" ")}
              draggable
              onDragStart={(event) => {
                event.dataTransfer.setData(TASK_ID_DRAG_TYPE, task.id);
              }}
              {...(focus && {
                role: "button",
                tabIndex: 0,
                "aria-label": `${task.title}: ${RUN_STATUS_LABEL[run?.status ?? "queued"]}`,
                onClick: focus,
                onKeyDown: (event: KeyboardEvent) => {
                  if (event.key !== "Enter" && event.key !== " ") return;
                  event.preventDefault();
                  focus();
                },
              })}
            >
              <span className={styles.title} title={task.title}>
                {task.title}
              </span>
              {run ? (
                <Chip tone={RUN_STATUS_TONE[run.status]}>{RUN_STATUS_LABEL[run.status]}</Chip>
              ) : (
                <Chip tone={presentation.chipTone}>
                  <StatusGlyph status={status} animate={status === "in_progress" && !reducedMotion} />
                  {presentation.label}
                </Chip>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
