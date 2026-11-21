import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { eventsReady, subscribeToEvents } from "../../../router/events";
import type { Task } from "../../../router/types";

const TASK_EVENT_TYPES = ["task.created", "task.status_changed", "task.updated", "task.discarded"];

/** Fetches every task (no session filter), then keeps the list live via the task.* SSE domain. */
export function useAllTasks(): Task[] {
  const [tasks, setTasks] = useState<Task[]>([]);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    const list = await router.listAllTasks();
    if (!mounted.current) return;
    setTasks(list);
  }, []);

  useEffect(() => {
    mounted.current = true;
    let live = true;
    // A reconnecting/backfilling SSE stream can dispatch a large burst of
    // task.* events in one go (e.g. everything since the bus's cursor after a long disconnect) —
    // debounce so that burst collapses into a single refetch instead of one
    // GET /tasks per event.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const unsubscribe = subscribeToEvents({ types: TASK_EVENT_TYPES }, () => {
      clearTimeout(timer);
      timer = setTimeout(() => void refetch(), 50);
    });
    // The mount read waits for the bus's ready barrier (router/events.ts
    // eventsReady), subscribed first: the bus never replays, so a read served
    // before it fixes its cursor could miss an event landing in between.
    void eventsReady().then(() => {
      if (live) void refetch();
    });
    return () => {
      live = false;
      mounted.current = false;
      clearTimeout(timer);
      unsubscribe();
    };
  }, [refetch]);

  return tasks;
}
