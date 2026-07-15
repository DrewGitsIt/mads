import type { Task } from "../../router/types";
import { Chip } from "../../components/Chip/Chip";
import { statusPresentation } from "./statusPresentation";
import styles from "./TaskRow.module.css";

export interface TaskRowProps {
  task: Task;
  tasksById: Record<string, Task>;
  /** Denser layout for the Context Rail's compact row (§4) — same component, not a fork. */
  compact?: boolean;
  onClick?: (task: Task) => void;
}

export function TaskRow({ task, tasksById, compact, onClick }: TaskRowProps) {
  const presentation = statusPresentation(task, tasksById);
  const classes = [
    styles.row,
    compact && styles.compact,
    presentation.dashedRing && styles.dashedRing,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div
      className={classes}
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick ? () => onClick(task) : undefined}
      onKeyDown={
        onClick
          ? (e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onClick(task);
              }
            }
          : undefined
      }
    >
      <span className={styles.title}>{task.title}</span>
      <div className={styles.chips}>
        <Chip tone={presentation.chipTone}>{presentation.label}</Chip>
        {presentation.depChips.map((depId) => (
          <Chip key={depId} tone="neutral">
            {tasksById[depId]?.title ?? depId}
          </Chip>
        ))}
        {!compact &&
          (task.labels ?? []).map((label) => (
            <Chip key={label} tone="neutral">
              {label}
            </Chip>
          ))}
      </div>
    </div>
  );
}
