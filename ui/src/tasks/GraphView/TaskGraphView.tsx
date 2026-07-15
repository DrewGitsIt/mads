import { useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";
import type { Task } from "../../router/types";
import { layout } from "./layout";
import { GraphCanvas } from "./GraphCanvas";
import { Minimap, type MinimapView } from "./Minimap";
import styles from "./TaskGraphView.module.css";

const VIEWPORT_SIZE = { width: 960, height: 600 };

export interface TaskGraphViewProps {
  sessionId: string;
  routerClient: RouterClient;
  tasks: Task[];
  tasksById: Record<string, Task>;
  onSelectTask?: (task: Task) => void;
}

/**
 * Cycle highlighting is authoritative and server-side (§5) — refetched
 * whenever the tasks prop changes (useTaskStore hands us a new array
 * reference on every task/note event), not derived client-side.
 */
export function TaskGraphView({
  sessionId,
  routerClient,
  tasks,
  tasksById,
  onSelectTask,
}: TaskGraphViewProps) {
  const [cycles, setCycles] = useState<string[][]>([]);
  const [view, setView] = useState<MinimapView>({ translateX: 0, translateY: 0, scale: 1 });

  useEffect(() => {
    let cancelled = false;
    routerClient.taskCycles(sessionId).then((c) => {
      if (!cancelled) setCycles(c);
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, tasks]);

  const graphLayout = layout(tasks, cycles);

  return (
    <div className={styles.wrapper}>
      <GraphCanvas
        layout={graphLayout}
        tasksById={tasksById}
        onSelectTask={onSelectTask}
        onViewChange={setView}
      />
      <div className={styles.minimap}>
        <Minimap
          layout={graphLayout}
          view={view}
          viewportSize={VIEWPORT_SIZE}
          onRecenter={setView}
        />
      </div>
    </div>
  );
}
