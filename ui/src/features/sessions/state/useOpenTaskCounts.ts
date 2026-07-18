import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";

const OPEN_STATUSES = new Set(["ready", "in_progress", "blocked", "needs_review"]);

const TASK_EVENT_TYPES = ["task.created", "task.status_changed", "task.updated", "task.discarded"];

/** Fetches every task, then keeps per-session open-task counts live via the task.* SSE domain. */
export function useOpenTaskCounts(): Record<string, number> {
  const [counts, setCounts] = useState<Record<string, number>>({});
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    const tasks = await router.listAllTasks();
    if (!mounted.current) return;
    const next: Record<string, number> = {};
    for (const task of tasks) {
      if (!OPEN_STATUSES.has(task.status)) continue;
      next[task.session_id] = (next[task.session_id] ?? 0) + 1;
    }
    setCounts(next);
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refetch();
    // A reconnecting/backfilling SSE stream can dispatch a large burst of
    // task.* events in one go (e.g. full history replay on connect) —
    // debounce so that burst collapses into a single refetch instead of one
    // GET /tasks per event.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const unsubscribe = subscribeToEvents({ types: TASK_EVENT_TYPES }, () => {
      clearTimeout(timer);
      timer = setTimeout(() => void refetch(), 50);
    });
    return () => {
      mounted.current = false;
      clearTimeout(timer);
      unsubscribe();
    };
  }, [refetch]);

  return counts;
}
