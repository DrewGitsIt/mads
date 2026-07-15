import type { RouterClient } from "../router/client";
import type { Task, TaskStatus } from "../router/types";
import { TaskRow } from "../tasks/ListView/TaskRow";
import { TaskDraftCard } from "../tasks/DraftCard/TaskDraftCard";
import styles from "./RailTaskList.module.css";

export interface RailTaskListProps {
  tasks: Task[];
  tasksById: Record<string, Task>;
  routerClient: RouterClient;
  statusFilter?: TaskStatus;
  onSelectTask?: (task: Task) => void;
  onEditDraftTask?: (task: Task) => void;
}

export function RailTaskList({
  tasks,
  tasksById,
  routerClient,
  statusFilter,
  onSelectTask,
  onEditDraftTask,
}: RailTaskListProps) {
  const drafts = tasks.filter((t) => t.draft);
  const rest = tasks
    .filter((t) => !t.draft)
    .filter((t) => !statusFilter || t.status === statusFilter);

  return (
    <div className={styles.list}>
      {drafts.map((task) => (
        <TaskDraftCard
          key={task.id}
          task={task}
          routerClient={routerClient}
          onEdit={(t) => onEditDraftTask?.(t)}
        />
      ))}
      {rest.map((task) => (
        <TaskRow key={task.id} task={task} tasksById={tasksById} compact onClick={onSelectTask} />
      ))}
    </div>
  );
}
