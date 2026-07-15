import { useState } from "react";
import type { RouterClient } from "../router/client";
import { Button } from "../components/Button/Button";
import { useTaskStore } from "./useTaskStore";
import { TaskListView } from "./ListView/TaskListView";
import { TaskGraphView } from "./GraphView/TaskGraphView";
import { TaskDetailSheet } from "./DetailSheet/TaskDetailSheet";
import { BulkImportPanel } from "./BulkImport/BulkImportPanel";
import styles from "./TasksTab.module.css";

export interface TasksTabProps {
  sessionId: string;
  routerClient: RouterClient;
}

type ViewMode = "list" | "graph";
type OpenSheet = "detail" | "import" | null;

/**
 * Detail and bulk-import sheets are mutually exclusive — one "which sheet is
 * open" piece of state, not two independent booleans (§5, kit has no Modal,
 * bulk import reuses SideSheet).
 */
export function TasksTab({ sessionId, routerClient }: TasksTabProps) {
  const { tasks, tasksById } = useTaskStore(routerClient, sessionId);
  const [viewMode, setViewMode] = useState<ViewMode>("list");
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null);
  const [openSheet, setOpenSheet] = useState<OpenSheet>(null);

  const selectedTask = selectedTaskId ? tasksById[selectedTaskId] : undefined;

  function selectTask(taskId: string) {
    setSelectedTaskId(taskId);
    setOpenSheet("detail");
  }

  return (
    <div className={styles.tab}>
      <div className={styles.toolbar}>
        <div className={styles.toggle}>
          <Button
            variant={viewMode === "list" ? "primary" : "secondary"}
            size="sm"
            onClick={() => setViewMode("list")}
          >
            List
          </Button>
          <Button
            variant={viewMode === "graph" ? "primary" : "secondary"}
            size="sm"
            onClick={() => setViewMode("graph")}
          >
            Graph
          </Button>
        </div>
        <Button variant="secondary" size="sm" onClick={() => setOpenSheet("import")}>
          Bulk import
        </Button>
      </div>

      <div className={styles.content}>
        {viewMode === "list" ? (
          <TaskListView
            tasks={tasks}
            tasksById={tasksById}
            onSelectTask={(task) => selectTask(task.id)}
          />
        ) : (
          <TaskGraphView
            sessionId={sessionId}
            routerClient={routerClient}
            tasks={tasks}
            tasksById={tasksById}
            onSelectTask={(task) => selectTask(task.id)}
          />
        )}
      </div>

      {openSheet === "detail" && selectedTask && (
        <TaskDetailSheet
          task={selectedTask}
          tasksById={tasksById}
          sessionTasks={tasks}
          routerClient={routerClient}
          onClose={() => setOpenSheet(null)}
        />
      )}

      {openSheet === "import" && (
        <BulkImportPanel
          sessionId={sessionId}
          routerClient={routerClient}
          onClose={() => setOpenSheet(null)}
        />
      )}
    </div>
  );
}
