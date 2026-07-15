import { useEffect, useRef, useState } from "react";
import type {
  AgentLeaderboardEntry,
  CapabilityUsage,
  CostMetrics,
  PipelineRun,
  TaskOutcomes,
} from "../../router/types";
import type { MetricsClient } from "../client/metricsClient";

interface ViewState<T> {
  data: T;
  loading: boolean;
  error: string | null;
}

function initial<T>(empty: T): ViewState<T> {
  return { data: empty, loading: true, error: null };
}

export interface UseDashboardMetrics {
  cost: ViewState<CostMetrics>;
  agents: ViewState<AgentLeaderboardEntry[]>;
  tasks: ViewState<TaskOutcomes>;
  capabilities: ViewState<CapabilityUsage[]>;
  runs: ViewState<PipelineRun[]>;
}

const emptyCostMetrics: CostMetrics = { by_day: [], by_session: [], by_pipeline_run: [] };
const emptyTaskOutcomes: TaskOutcomes = { counts: {}, total: 0, completion_rate: 0 };

/**
 * Fetches all five metrics views once on mount, each independently — per
 * HANDOFF.md §2 there's no cross-view dependency and no polling ("charts
 * refresh on view" = on mount only).
 */
export function useDashboardMetrics(client: MetricsClient): UseDashboardMetrics {
  const [cost, setCost] = useState<ViewState<CostMetrics>>(initial(emptyCostMetrics));
  const [agents, setAgents] = useState<ViewState<AgentLeaderboardEntry[]>>(initial([]));
  const [tasks, setTasks] = useState<ViewState<TaskOutcomes>>(initial(emptyTaskOutcomes));
  const [capabilities, setCapabilities] = useState<ViewState<CapabilityUsage[]>>(initial([]));
  const [runs, setRuns] = useState<ViewState<PipelineRun[]>>(initial([]));
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;

    client
      .metricsCost()
      .then((data) => {
        if (mounted.current) setCost({ data, loading: false, error: null });
      })
      .catch((err) => {
        if (mounted.current) {
          setCost((s) => ({ ...s, loading: false, error: err instanceof Error ? err.message : String(err) }));
        }
      });

    client
      .metricsAgentLeaderboard()
      .then((data) => {
        if (mounted.current) setAgents({ data, loading: false, error: null });
      })
      .catch((err) => {
        if (mounted.current) {
          setAgents((s) => ({ ...s, loading: false, error: err instanceof Error ? err.message : String(err) }));
        }
      });

    client
      .metricsTaskOutcomes()
      .then((data) => {
        if (mounted.current) setTasks({ data, loading: false, error: null });
      })
      .catch((err) => {
        if (mounted.current) {
          setTasks((s) => ({ ...s, loading: false, error: err instanceof Error ? err.message : String(err) }));
        }
      });

    client
      .metricsCapabilityUsage()
      .then((data) => {
        if (mounted.current) setCapabilities({ data, loading: false, error: null });
      })
      .catch((err) => {
        if (mounted.current) {
          setCapabilities((s) => ({
            ...s,
            loading: false,
            error: err instanceof Error ? err.message : String(err),
          }));
        }
      });

    client
      .metricsPipelineRuns()
      .then((data) => {
        if (mounted.current) setRuns({ data, loading: false, error: null });
      })
      .catch((err) => {
        if (mounted.current) {
          setRuns((s) => ({ ...s, loading: false, error: err instanceof Error ? err.message : String(err) }));
        }
      });

    return () => {
      mounted.current = false;
    };
  }, [client]);

  return { cost, agents, tasks, capabilities, runs };
}
