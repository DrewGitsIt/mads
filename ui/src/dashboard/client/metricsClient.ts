import { routerGet } from "../../router/client";
import type {
  AgentLeaderboardEntry,
  CapabilityUsage,
  CostMetrics,
  PipelineRun,
  Session,
  TaskOutcomes,
} from "../../router/types";

/**
 * Router API surface the Dashboard tab depends on — a dedicated interface
 * scoped to this tab, per HANDOFF.md §4 (mirrors PipelineClient's precedent,
 * not an extension of the shared RouterClient). Every metrics call is
 * system-wide (no session_id) — HANDOFF.md §2: Dashboard is system-level,
 * not session-scoped.
 */
export interface MetricsClient {
  metricsCost(): Promise<CostMetrics>;
  metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]>;
  metricsTaskOutcomes(): Promise<TaskOutcomes>;
  metricsCapabilityUsage(): Promise<CapabilityUsage[]>;
  metricsPipelineRuns(): Promise<PipelineRun[]>;
  listSessions(): Promise<Session[]>;
}

/** fetch-backed MetricsClient. Only exercised directly by metricsClient.test.ts. */
export class FetchMetricsClient implements MetricsClient {
  async metricsCost(): Promise<CostMetrics> {
    return routerGet<CostMetrics>("/metrics/cost");
  }

  async metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]> {
    return routerGet<AgentLeaderboardEntry[]>("/metrics/agents");
  }

  async metricsTaskOutcomes(): Promise<TaskOutcomes> {
    return routerGet<TaskOutcomes>("/metrics/tasks");
  }

  async metricsCapabilityUsage(): Promise<CapabilityUsage[]> {
    return routerGet<CapabilityUsage[]>("/metrics/capabilities");
  }

  async metricsPipelineRuns(): Promise<PipelineRun[]> {
    return routerGet<PipelineRun[]>("/metrics/pipelines");
  }

  async listSessions(): Promise<Session[]> {
    return routerGet<Session[]>("/sessions");
  }
}
