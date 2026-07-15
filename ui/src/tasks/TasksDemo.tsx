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
 * Mounts the Tasks tab and Context Rail side by side, per HANDOFF.md §8 —
 * this is what surfaces the §7 sharing question. TasksTab owns its own
 * useTaskStore (§4); the rail is fed by a second instance here so it can be
 * reused standalone by whatever real container ends up mounting it. Against
 * FakeRouterClient this is free (streamTaskEvents returns one shared source),
 * but a real Session Workspace container should lift a single store and pass
 * tasks/tasksById into both instead of letting each open its own SSE
 * connection — see HANDOFF.md's executor notes.
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
