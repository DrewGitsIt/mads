import { useEffect, useMemo, useState } from "react";
import type { RouterClient } from "../router/client";
import { coalesce } from "../router/coalesce";
import { eventsReady, subscribeToEvents } from "../router/events";
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
 * Fetches GET /tasks then follows the shared event bus. Event payloads are
 * sparse (§6) — every task/note envelope triggers a refetch of the session's
 * list, never trusting envelope.payload to reconstruct state; task.discarded
 * also removes the task at once.
 *
 * Order safety: the mount read and every event refetch go through one
 * single-flight coalescer (router/coalesce.ts), so two list reads are never
 * in flight together and an event heard mid-read buys one more read after it
 * — the last response applied is always the newest one requested. Discards
 * are terminal and applied locally, so a snapshot requested before a discard
 * (and landing after it) could carry the task back; snapshots are filtered
 * by every discard this store has heard.
 */
export function useTaskStore(routerClient: RouterClient, sessionId: string): TaskStore {
  const [tasks, setTasks] = useState<Task[]>([]);

  useEffect(() => {
    let cancelled = false;
    const discarded = new Set<string>();
    const fetcher = coalesce(() =>
      routerClient.listTasks(sessionId).then((list) => {
        if (cancelled) return;
        setTasks(discarded.size === 0 ? list : list.filter((t) => !discarded.has(t.id)));
      }),
    );
    const unsubscribe = subscribeToEvents({ sessionId, types: TASK_STORE_EVENT_TYPES }, (envelope) => {
      if (!envelope.task_id) return;
      if (envelope.type === "task.discarded") {
        const id = envelope.task_id;
        discarded.add(id);
        setTasks((prev) => prev.filter((t) => t.id !== id));
      }
      fetcher.request();
    });
    // The mount read waits for the bus's ready barrier (router/events.ts
    // eventsReady), subscribed first: the bus never replays, so a read served
    // before it fixes its cursor could miss an event landing in between.
    void eventsReady().then(() => {
      if (!cancelled) fetcher.now();
    });

    return () => {
      cancelled = true;
      fetcher.cancel();
      unsubscribe();
    };
  }, [routerClient, sessionId]);

  const tasksById = useMemo(
    () => Object.fromEntries(tasks.map((t) => [t.id, t])),
    [tasks],
  );

  return { tasks, tasksById };
}
