import type { RefObject } from "react";
import { Chip } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import { statusPresentation } from "../ListView/statusPresentation";
import { graphStatusOf } from "./graphStatus";
import { StatusGlyph } from "./StatusGlyph";
import { TASK_ID_DRAG_TYPE, trayOrder } from "./tray";
import styles from "./UnsequencedTray.module.css";

export interface UnsequencedTrayProps {
  tasks: Task[];
  tasksById: Record<string, Task>;
  /** Owned by TaskGraphView so it can hit-test canvas-drop points against this box (§7). */
  containerRef: RefObject<HTMLDivElement>;
  reducedMotion?: boolean;
}

export function UnsequencedTray({ tasks, tasksById, containerRef, reducedMotion = false }: UnsequencedTrayProps) {
  const ordered = trayOrder(tasks);

  return (
    <div ref={containerRef} className={styles.tray}>
      <div className={styles.caption}>Runs when a slot opens · in this order.</div>
      <div className={styles.shelf}>
        {ordered.map((task) => {
          const presentation = statusPresentation(task, tasksById);
          const status = graphStatusOf(task);
          return (
            <div
              key={task.id}
              className={[styles.card, status === "done" && styles.done].filter(Boolean).join(" ")}
              draggable
              onDragStart={(event) => {
                event.dataTransfer.setData(TASK_ID_DRAG_TYPE, task.id);
              }}
            >
              <span className={styles.title}>{task.title}</span>
              <Chip tone={presentation.chipTone}>
                <StatusGlyph status={status} animate={status === "in_progress" && !reducedMotion} />
                {presentation.label}
              </Chip>
            </div>
          );
        })}
      </div>
    </div>
  );
}
