import type {
  AgentLeaderboardEntry,
  CapabilityUsage,
  CostMetrics,
  PipelineRun,
  Session,
  TaskOutcomes,
} from "../../router/types";
import type { MetricsClient } from "./metricsClient";

/**
 * Golden aggregates copied verbatim from the real Go golden tests, per
 * HANDOFF.md §5 ("do not invent numbers") — not hand-invented data that
 * merely looks plausible. Session/run IDs that the Go tests generate
 * dynamically (ULIDs) are stand-ins; every numeric aggregate is literal.
 */

const sess1 = "ses_c1a0f9b3d8e21456";
const sess2 = "ses_d2b1e0a4c9f32567";

// pkg/dataclient/metrics_test.go:TestMetricsCost (lines 27-124): 2 sessions,
// 2 models, 2 days, one pipeline run wrapping one usage event.
export const fixtureCostMetrics: CostMetrics = {
  by_day: [
    { day: "2026-07-10T00:00:00Z", model: "model-a", input_tokens: 100, output_tokens: 50, cost_usd: 0.1 },
    { day: "2026-07-10T00:00:00Z", model: "model-b", input_tokens: 300, output_tokens: 150, cost_usd: 0.3 },
    { day: "2026-07-11T00:00:00Z", model: "model-a", input_tokens: 250, output_tokens: 125, cost_usd: 0.25 },
  ],
  by_session: [
    { session_id: sess1, cost_usd: 0.3 },
    { session_id: sess2, cost_usd: 0.35 },
  ],
  by_pipeline_run: [{ session_id: sess2, run_seq: 5, cost_usd: 0.05 }],
};

// pkg/dataclient/metrics_test.go:TestMetricsAgentLeaderboard (line 128): 3
// executor selections (2 successes, 1 failure), 1 architect selection
// (1 success).
export const fixtureAgentLeaderboard: AgentLeaderboardEntry[] = [
  {
    agent: "executor",
    usage_count: 3,
    successes: 2,
    failures: 1,
    success_rate: 2 / 3,
    retries: 1,
  },
  {
    agent: "architect",
    usage_count: 1,
    successes: 1,
    failures: 0,
    success_rate: 1,
    retries: 0,
  },
];

// pkg/dataclient/metrics_test.go:TestMetricsTaskOutcomes (line 186): 4
// confirmed tasks (done/blocked/wont_do/ready, one each); a draft task is
// excluded from the distribution entirely.
export const fixtureTaskOutcomes: TaskOutcomes = {
  counts: { done: 1, blocked: 1, wont_do: 1, ready: 1 },
  total: 4,
  completion_rate: 0.25,
};

// pkg/dataclient/metrics_test.go:TestMetricsCapabilityUsage (line 241): one
// capability invoked 3 times, one never invoked — the never-used flag's
// concrete case.
export const fixtureCapabilityUsage: CapabilityUsage[] = [
  { capability: "used-skill", type: "skill", invocations: 3 },
  { capability: "unused-skill", type: "skill", invocations: 0 },
];

// pkg/contracts/testdata/pipeline_run.json — the single golden PipelineRun
// instance referenced by HANDOFF.md §5 (also exercised by
// pkg/dataclient/metrics_test.go:TestMetricsPipelineRuns, line 290).
export const fixturePipelineRuns: PipelineRun[] = [
  {
    session_id: "ses_01hq3k",
    run_seq: 3,
    started_at: "2026-07-12T09:00:00Z",
    ended_at: "2026-07-12T09:10:00Z",
    outcome: "completed",
    tasks_done: 2,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0.42,
    agents: ["architect", "executor"],
  },
];

// Not a golden-metrics fixture — the live widget's session list, following
// the same shape as recordedRun.ts's session-adjacent fixtures elsewhere in
// this repo (HANDOFF.md §4/§7: reuses the real GET /sessions route).
export const fixtureSessions: Session[] = [
  {
    id: sess1,
    name: "core lib",
    activity: "active",
    created_at: "2026-07-12T09:00:00Z",
    updated_at: "2026-07-12T09:00:00Z",
  },
  {
    id: sess2,
    name: "flaky api",
    activity: "inactive",
    created_at: "2026-07-12T09:00:00Z",
    updated_at: "2026-07-12T09:00:00Z",
  },
];

/**
 * In-memory MetricsClient returning the golden fixtures above synchronously
 * (resolved promises), same shape as PipelineDemo.tsx's FakePipelineClient
 * (HANDOFF.md §5).
 */
export class FakeMetricsClient implements MetricsClient {
  async metricsCost(): Promise<CostMetrics> {
    return fixtureCostMetrics;
  }

  async metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]> {
    return fixtureAgentLeaderboard;
  }

  async metricsTaskOutcomes(): Promise<TaskOutcomes> {
    return fixtureTaskOutcomes;
  }

  async metricsCapabilityUsage(): Promise<CapabilityUsage[]> {
    return fixtureCapabilityUsage;
  }

  async metricsPipelineRuns(): Promise<PipelineRun[]> {
    return fixturePipelineRuns;
  }

  async listSessions(): Promise<Session[]> {
    return fixtureSessions;
  }
}
