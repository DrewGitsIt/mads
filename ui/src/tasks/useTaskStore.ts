import { useEffect, useMemo, useState } from "react";
import type { RouterClient } from "../router/client";
import type { Envelope, Task } from "../router/types";

export interface TaskStore {
  tasks: Task[];
  tasksById: Record<string, Task>;
}

/**
 * Fetches GET /tasks then subscribes to streamTaskEvents. Event payloads are
 * sparse (§6) — every "task." or "note." envelope triggers a refetch of that one
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

    const source = routerClient.streamTaskEvents(sessionId);
    function handleMessage(ev: MessageEvent<string>) {
      const envelope = JSON.parse(ev.data) as Envelope;
      if (!envelope.task_id) return;

      if (envelope.type === "task.discarded") {
        setTasks((prev) => prev.filter((t) => t.id !== envelope.task_id));
        return;
      }

      if (envelope.type.startsWith("task.") || envelope.type.startsWith("note.")) {
        routerClient.getTask(envelope.task_id).then((t) => {
          setTasks((prev) => {
            const idx = prev.findIndex((p) => p.id === t.id);
            if (idx === -1) return [...prev, t];
            const next = [...prev];
            next[idx] = t;
            return next;
          });
        });
      }
    }
    source.addEventListener("message", handleMessage);

    return () => {
      cancelled = true;
      source.removeEventListener("message", handleMessage);
      source.close();
    };
  }, [routerClient, sessionId]);

  const tasksById = useMemo(
    () => Object.fromEntries(tasks.map((t) => [t.id, t])),
    [tasks],
  );

  return { tasks, tasksById };
}
