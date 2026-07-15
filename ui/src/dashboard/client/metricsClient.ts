import { router, type RouterClient } from "../../router/client";
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

/** RouterClient-backed MetricsClient. Only exercised directly by metricsClient.test.ts. */
export class FetchMetricsClient implements MetricsClient {
  constructor(private readonly client: RouterClient = router) {}

  async metricsCost(): Promise<CostMetrics> {
    return this.client.metricsCost();
  }

  async metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]> {
    return this.client.metricsAgentLeaderboard();
  }

  async metricsTaskOutcomes(): Promise<TaskOutcomes> {
    return this.client.metricsTaskOutcomes();
  }

  async metricsCapabilityUsage(): Promise<CapabilityUsage[]> {
    return this.client.metricsCapabilityUsage();
  }

  async metricsPipelineRuns(): Promise<PipelineRun[]> {
    return this.client.metricsPipelineRuns();
  }

  async listSessions(): Promise<Session[]> {
    return this.client.listSessions();
  }
}
