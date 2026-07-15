import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
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
    void refetch();
    const unsubscribe = subscribeToEvents({ types: TASK_EVENT_TYPES }, () => {
      void refetch();
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  return tasks;
}
