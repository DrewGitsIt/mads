import { renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useDashboardMetrics } from "./useDashboardMetrics";
import {
  FakeMetricsClient,
  fixtureAgentLeaderboard,
  fixtureCapabilityUsage,
  fixtureCostMetrics,
  fixturePipelineRuns,
  fixtureTaskOutcomes,
} from "../client/fixtures";

describe("useDashboardMetrics", () => {
  it("fetches all five views on mount and exposes the golden fixtures", async () => {
    const client = new FakeMetricsClient();
    const { result } = renderHook(() => useDashboardMetrics(client));

    await waitFor(() => expect(result.current.cost.loading).toBe(false));
    await waitFor(() => expect(result.current.agents.loading).toBe(false));
    await waitFor(() => expect(result.current.tasks.loading).toBe(false));
    await waitFor(() => expect(result.current.capabilities.loading).toBe(false));
    await waitFor(() => expect(result.current.runs.loading).toBe(false));

    expect(result.current.cost.data).toEqual(fixtureCostMetrics);
    expect(result.current.agents.data).toEqual(fixtureAgentLeaderboard);
    expect(result.current.tasks.data).toEqual(fixtureTaskOutcomes);
    expect(result.current.capabilities.data).toEqual(fixtureCapabilityUsage);
    expect(result.current.runs.data).toEqual(fixturePipelineRuns);
  });

  it("surfaces a per-view error without blocking the other views", async () => {
    const client = new FakeMetricsClient();
    client.metricsCost = async () => {
      throw new Error("boom");
    };
    const { result } = renderHook(() => useDashboardMetrics(client));

    await waitFor(() => expect(result.current.cost.loading).toBe(false));
    expect(result.current.cost.error).toMatch(/boom/);

    await waitFor(() => expect(result.current.agents.loading).toBe(false));
    expect(result.current.agents.error).toBeNull();
    expect(result.current.agents.data).toEqual(fixtureAgentLeaderboard);
  });
});
