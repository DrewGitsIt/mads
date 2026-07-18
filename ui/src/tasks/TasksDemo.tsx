import { useState } from "react";
import { TasksTab } from "./TasksTab";
import { useTaskStore } from "./useTaskStore";
import { TaskDetailSheet } from "./DetailSheet/TaskDetailSheet";
import { ContextRail } from "../context-rail/ContextRail";
import { FakeRouterClient } from "../router/fixtures";
import type { Task } from "../router/types";
import styles from "./TasksDemo.module.css";

const client = new FakeRouterClient();
const SESSION_ID = "session-active";

/**
 * Mounts the Tasks tab and Context Rail side by side. TasksTab owns its own
 * useTaskStore; the rail is fed by a second instance here so it can be reused
 * standalone by whatever real container ends up mounting it. Both instances
 * share identical {sessionId, types}, so they collapse onto the same bus
 * connection — a real Session Workspace container could still lift a single
 * store and pass tasks/tasksById into both to avoid the duplicate fetch/state.
 */
export function TasksDemo() {
  const { tasks, tasksById } = useTaskStore(client, SESSION_ID);
  const [railSelection, setRailSelection] = useState<Task | null>(null);

  return (
    <div className={styles.demo}>
      <div className={styles.main}>
        <TasksTab sessionId={SESSION_ID} routerClient={client} />
      </div>
      <div className={styles.rail}>
        <ContextRail
          sessionId={SESSION_ID}
          routerClient={client}
          tasks={tasks}
          tasksById={tasksById}
          onSelectTask={setRailSelection}
          onEditDraftTask={setRailSelection}
        />
      </div>
      {railSelection && (
        <TaskDetailSheet
          task={railSelection}
          tasksById={tasksById}
          sessionTasks={tasks}
          routerClient={client}
          onClose={() => setRailSelection(null)}
        />
      )}
    </div>
  );
}
