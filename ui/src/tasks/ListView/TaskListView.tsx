import type { Task, TaskStatus } from "../../router/types";
import { TaskRow } from "./TaskRow";
import styles from "./TaskListView.module.css";

export interface TaskListViewProps {
  tasks: Task[];
  tasksById: Record<string, Task>;
  /** A single status collapses the grouped view to a flat filtered list (MADS-UX §3.3: "grouping returns on All"). */
  statusFilter?: TaskStatus;
  onSelectTask?: (task: Task) => void;
}

const GROUP_ORDER: { status: TaskStatus; label: string }[] = [
  { status: "ready", label: "Ready" },
  { status: "in_progress", label: "In progress" },
  { status: "blocked", label: "Blocked" },
  { status: "needs_review", label: "Needs review" },
  { status: "done", label: "Done" },
  { status: "wont_do", label: "Won't do" },
];

export function TaskListView({
  tasks,
  tasksById,
  statusFilter,
  onSelectTask,
}: TaskListViewProps) {
  if (statusFilter) {
    const filtered = tasks.filter((t) => t.status === statusFilter);
    return (
      <div className={styles.list}>
        {filtered.map((task) => (
          <TaskRow
            key={task.id}
            task={task}
            tasksById={tasksById}
            onClick={onSelectTask}
          />
        ))}
      </div>
    );
  }

  return (
    <div className={styles.list}>
      {GROUP_ORDER.map(({ status, label }) => {
        const group = tasks.filter((t) => t.status === status);
        if (group.length === 0) return null;
        return (
          <section key={status} className={styles.group}>
            <h3 className={styles.groupHeading}>{label}</h3>
            {group.map((task) => (
              <TaskRow
                key={task.id}
                task={task}
                tasksById={tasksById}
                onClick={onSelectTask}
              />
            ))}
          </section>
        );
      })}
    </div>
  );
}
