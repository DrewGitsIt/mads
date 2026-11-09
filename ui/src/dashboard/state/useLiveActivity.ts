import { useEffect, useState } from "react";
import { coalesce } from "../../router/coalesce";
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
  useEffect(() => {
    let cancelled = false;
    const refetch = async () => {
      try {
        const [sessions, runs] = await Promise.all([client.listSessions(), client.metricsPipelineRuns()]);
        if (cancelled) return;
        setActiveSessionCount(sessions.filter((s) => s.activity === "active").length);
        setRunningPipelineCount(runs.filter((r) => r.outcome === "running").length);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    // The bus replays its durable log on connect: coalesce that burst (and
    // any live one) into a run or two rather than a pair of fetches per
    // event. The mount fetch is immediate.
    const refresh = coalesce(refetch);
    refresh.now();
    const unsubscribe = subscribeToEvents({ types: LIVE_EVENT_TYPES }, () => refresh.request());
    return () => {
      cancelled = true;
      refresh.cancel();
      unsubscribe();
    };
  }, [client]);

  return { activeSessionCount, runningPipelineCount, loading, error };
}
