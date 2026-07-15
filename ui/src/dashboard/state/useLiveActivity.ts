import { useCallback, useEffect, useRef, useState } from "react";
import { subscribeToEvents } from "../../router/events";
import type { MetricsClient } from "../client/metricsClient";

// session.* domain, same list as useSessionsList.ts. pipeline.* domain, same
// list as usePipelineRun.ts's METADATA/FOLD union minus stage.*/file.changed
// (this widget only cares about run-level transitions, not per-stage detail).
const LIVE_EVENT_TYPES = [
  "session.created",
  "session.renamed",
  "session.activity_changed",
  "session.updated",
  "session.deleted",
  "session.crashed",
  "pipeline.started",
  "pipeline.completed",
  "pipeline.cancelled",
  "pipeline.failed",
];

export interface UseLiveActivity {
  activeSessionCount: number;
  runningPipelineCount: number;
  loading: boolean;
  error: string | null;
}

/**
 * Live "right now" counts for the Dashboard's LiveWidget — refetch-on-event,
 * useSessionsList.ts's idiom, not date-preset-scoped (HANDOFF.md §7).
 */
export function useLiveActivity(client: MetricsClient): UseLiveActivity {
  const [activeSessionCount, setActiveSessionCount] = useState(0);
  const [runningPipelineCount, setRunningPipelineCount] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    try {
      const [sessions, runs] = await Promise.all([client.listSessions(), client.metricsPipelineRuns()]);
      if (mounted.current) {
        setActiveSessionCount(sessions.filter((s) => s.activity === "active").length);
        setRunningPipelineCount(runs.filter((r) => r.outcome === "running").length);
      }
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, [client]);

  useEffect(() => {
    mounted.current = true;
    void refetch();
    const unsubscribe = subscribeToEvents({ types: LIVE_EVENT_TYPES }, () => {
      void refetch();
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  return { activeSessionCount, runningPipelineCount, loading, error };
}
