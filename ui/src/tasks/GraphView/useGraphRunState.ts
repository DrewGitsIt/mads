import { useEffect, useReducer, useRef } from "react";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import {
  FOLD_EVENT_TYPES,
  foldReducer,
  initialRunFoldState,
  type RunFoldState,
} from "../../pipeline/state/usePipelineRun";
import { subscribeToEvents } from "../../router/events";

/**
 * Opt-in mirror of usePipelineRun's fold logic without its duplicate task
 * fetch — GraphView already has tasks from useTaskStore, no refetch needed.
 * Returns undefined when client is absent so callers can render without
 * ever constructing an EventSource.
 */
export function useGraphRunState(
  sessionId: string,
  client: PipelineClient | undefined,
): RunFoldState | undefined {
  const [state, dispatch] = useReducer(foldReducer, initialRunFoldState);
  const clientRef = useRef(client);
  clientRef.current = client;

  useEffect(() => {
    if (!clientRef.current) return;
    const unsubscribe = subscribeToEvents({ sessionId, types: FOLD_EVENT_TYPES }, (env) => dispatch(env));
    return unsubscribe;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, !!client]);

  return client ? state : undefined;
}
