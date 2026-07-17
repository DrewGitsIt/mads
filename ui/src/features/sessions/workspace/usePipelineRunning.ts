import { useEffect, useState } from "react";
import { subscribeToEvents } from "../../../router/events";

const RUN_EVENT_TYPES = ["pipeline.started", "pipeline.completed", "pipeline.cancelled", "pipeline.failed"];

/**
 * Live "is a pipeline running" signal for the Pipeline tab's run-dot. Only
 * reacts to events observed after mount — subscribeToEvents has no `since`,
 * so a run already in flight before this hook subscribes won't show a dot
 * (same limitation usePipelineRun has).
 */
export function usePipelineRunning(sessionId: string): boolean {
  const [running, setRunning] = useState(false);

  useEffect(() => {
    setRunning(false);
    return subscribeToEvents({ sessionId, types: RUN_EVENT_TYPES }, (env) => {
      setRunning(env.type === "pipeline.started");
    });
  }, [sessionId]);

  return running;
}
