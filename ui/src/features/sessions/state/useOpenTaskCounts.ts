import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import type { OpenTaskBreakdown } from "../rail/railGroups";
import type { TaskStatus } from "../../../router/types";

/** needs_review folds into `ready` — same simplification as GraphView's graphStatus.ts. */
const STATUS_BUCKET: Partial<Record<TaskStatus, keyof OpenTaskBreakdown>> = {
  in_progress: "running",
  ready: "ready",
  needs_review: "ready",
  blocked: "blocked",
};

const TASK_EVENT_TYPES = ["task.created", "task.status_changed", "task.updated", "task.discarded"];

/** Fetches every task, then keeps per-session open-task breakdowns live via the task.* SSE domain. */
export function useOpenTaskCounts(): Record<string, OpenTaskBreakdown> {
  const [counts, setCounts] = useState<Record<string, OpenTaskBreakdown>>({});
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    const tasks = await router.listAllTasks();
    if (!mounted.current) return;
    const next: Record<string, OpenTaskBreakdown> = {};
    for (const task of tasks) {
      const bucket = STATUS_BUCKET[task.status];
      if (!bucket) continue;
      const entry = next[task.session_id] ?? { running: 0, ready: 0, blocked: 0 };
      entry[bucket] += 1;
      next[task.session_id] = entry;
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
