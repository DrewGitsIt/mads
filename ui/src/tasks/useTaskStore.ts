import { useEffect, useMemo, useState } from "react";
import type { RouterClient } from "../router/client";
import { subscribeToEvents } from "../router/events";
import type { Task } from "../router/types";

export interface TaskStore {
  tasks: Task[];
  tasksById: Record<string, Task>;
}

// Full concrete set from pkg/contracts/event.go — the bus dispatches by
// literal event name (matching the Router's `event: <type>` wire format), not
// the "task.*,note.*" wildcard hint a server-side filter would accept.
const TASK_STORE_EVENT_TYPES = [
  "task.created",
  "task.updated",
  "task.status_changed",
  "task.confirmed",
  "task.discarded",
  "note.created",
  "note.confirmed",
  "note.discarded",
];

/**
 * Fetches GET /tasks then subscribes to the shared event bus. Event payloads
 * are sparse (§6) — every task/note envelope triggers a refetch of that one
 * task via getTask, patched into state; task.discarded removes it. Never
 * trusts envelope.payload to reconstruct state.
 */
export function useTaskStore(routerClient: RouterClient, sessionId: string): TaskStore {
  const [tasks, setTasks] = useState<Task[]>([]);

  useEffect(() => {
    let cancelled = false;
    routerClient.listTasks(sessionId).then((t) => {
      if (!cancelled) setTasks(t);
    });

    const unsubscribe = subscribeToEvents({ sessionId, types: TASK_STORE_EVENT_TYPES }, (envelope) => {
      if (!envelope.task_id) return;

      if (envelope.type === "task.discarded") {
        setTasks((prev) => prev.filter((t) => t.id !== envelope.task_id));
        return;
      }

      routerClient.getTask(envelope.task_id).then((t) => {
        setTasks((prev) => {
          const idx = prev.findIndex((p) => p.id === t.id);
          if (idx === -1) return [...prev, t];
          const next = [...prev];
          next[idx] = t;
          return next;
        });
      });
    });

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient, sessionId]);

  const tasksById = useMemo(
    () => Object.fromEntries(tasks.map((t) => [t.id, t])),
    [tasks],
  );

  return { tasks, tasksById };
}
