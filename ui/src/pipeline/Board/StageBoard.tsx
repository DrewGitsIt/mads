import type { Task } from "../../router/types";
import type { TaskRunState } from "../state/usePipelineRun";
import { TaskCard } from "./TaskCard";
import styles from "./StageBoard.module.css";

export interface StageBoardProps {
  /** Dynamic stage columns, in first-seen order (usePipelineRun's `columns`). */
  columns: string[];
  tasks: Task[];
  taskRunState: Record<string, TaskRunState>;
  onSelectTask?: (taskId: string) => void;
}

interface BoardColumn {
  id: string;
  label: string;
  tasks: Task[];
}

// Blocked tasks are deliberately excluded here — they surface in BlockedPanel
// instead, not as a quiet card in a column (HANDOFF.md §2).
function buildColumns(
  columns: string[],
  tasks: Task[],
  taskRunState: Record<string, TaskRunState>,
): BoardColumn[] {
  const board: BoardColumn[] = [
    { id: "__queued", label: "Queued", tasks: [] },
    ...columns.map((stage) => ({ id: stage, label: stage, tasks: [] as Task[] })),
    { id: "__done", label: "Done", tasks: [] },
  ];
  const byId = new Map(board.map((column) => [column.id, column]));

  for (const task of tasks) {
    if (task.status === "blocked") continue;
    if (task.status === "done") {
      byId.get("__done")!.tasks.push(task);
      continue;
    }
    const stage = taskRunState[task.id]?.currentStage;
    const column = stage ? byId.get(stage) : undefined;
    (column ?? byId.get("__queued")!).tasks.push(task);
  }

  return board;
}

export function StageBoard({ columns, tasks, taskRunState, onSelectTask }: StageBoardProps) {
  const board = buildColumns(columns, tasks, taskRunState);

  return (
    <div className={styles.board}>
      {board.map((column) => (
        <div className={styles.column} key={column.id} data-testid={`stage-column-${column.id}`}>
          <div className={styles.columnHeader}>
            <span className={styles.columnLabel}>{column.label}</span>
            <span className={styles.columnCount}>{column.tasks.length}</span>
          </div>
          <div className={styles.columnBody}>
            {column.tasks.map((task) => (
              <TaskCard
                key={task.id}
                task={task}
                runState={taskRunState[task.id]}
                onClick={onSelectTask ? () => onSelectTask(task.id) : undefined}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
