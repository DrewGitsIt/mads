import { useCallback, useEffect, useRef, useState } from "react";
import type { PipelineClient } from "../../../pipeline/client/pipelineClient";
import { useResumingRunFold, type TaskRunState } from "../../../pipeline/state/usePipelineRun";
import type { Envelope, PipelineRun, Task } from "../../../router/types";

const TASK_EVENT_TYPES = ["task.created", "task.updated", "task.status_changed", "task.discarded"];

// The newest run still marked running, as usePipelineRun picks it: a stale
// "running" record from an older run must not shadow a newer one.
const newestRunning = (runs: PipelineRun[]) =>
  runs.filter((r) => r.outcome === "running").sort((a, b) => b.run_seq - a.run_seq)[0];

/**
 * The tasks a live run is working on right now: in progress and the run's
 * own (it claimed or picked them up: in its fold), which is what the Tasks
 * tab reads Running. Someone's own in-progress work, which the run never
 * touches, doesn't count.
 */
export function runningInRun(tasks: Task[], taskRunState: Record<string, TaskRunState>): number {
  return tasks.filter((t) => t.status === "in_progress" && !!taskRunState[t.id]).length;
}

/**
 * While `live`, how many tasks the session's live run is working on (see
 * runningInRun); undefined when not live or until the fold and task list
 * have caught up. Idle when not live: no fetches, no fold.
 */
export function useLiveRunTaskCount(sessionId: string, client: PipelineClient, live: boolean): number | undefined {
  const [tasks, setTasks] = useState<Task[]>();
  const liveRef = useRef(live);
  liveRef.current = live;

  const refetch = useCallback(async () => {
    try {
      const list = await client.listTasks(sessionId);
      if (liveRef.current) setTasks(list);
    } catch {
      // Best-effort: the badge keeps its last count.
    }
  }, [client, sessionId]);

  useEffect(() => {
    if (!live) {
      setTasks(undefined);
      return;
    }
    void refetch();
  }, [live, refetch]);

  const onEnvelope = useCallback(
    (env: Envelope) => {
      if (liveRef.current && TASK_EVENT_TYPES.includes(env.type)) void refetch();
    },
    [refetch],
  );

  const { state, resuming } = useResumingRunFold(sessionId, live ? client : undefined, newestRunning, onEnvelope);

  if (!live || resuming || !tasks || state.phase !== "running") return undefined;
  return runningInRun(tasks, state.taskRunState);
}
