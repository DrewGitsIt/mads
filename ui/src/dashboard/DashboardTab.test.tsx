import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DashboardTab } from "./DashboardTab";
import {
  FakeMetricsClient,
  fixtureAgentLeaderboard,
  fixtureCapabilityUsage,
  fixturePipelineRuns,
  fixtureTaskOutcomes,
} from "./client/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { CostMetrics, PipelineRun } from "../router/types";

describe("DashboardTab", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.spyOn(Date, "now").mockReturnValue(new Date("2026-07-15T00:00:00Z").getTime());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("renders golden aggregates for every view and forwards drill-back", async () => {
    const client = new FakeMetricsClient();
    const onDrillBack = vi.fn();
    render(<DashboardTab client={client} onDrillBack={onDrillBack} />);

    await waitFor(() => expect(screen.getByText("ses_01hq3k")).toBeTruthy());
    expect(screen.getByText("executor")).toBeTruthy();
    expect(screen.getByText("used-skill")).toBeTruthy();
    expect(screen.getByText("Never used")).toBeTruthy();
    expect(screen.getByText(/25% completion rate/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "View run" }));
    expect(onDrillBack).toHaveBeenCalledWith({ sessionId: "ses_01hq3k", runSeq: 3 });
  });

  it("re-slices the cost chart and run history on date preset, leaving the other views unchanged", async () => {
    const client = new FakeMetricsClient();

    const recentDay = "2026-07-01T00:00:00Z"; // 14 days before fake "now": within 30d, outside 7d
    const oldDay = "2026-05-01T00:00:00Z"; // ~75 days before fake "now": outside 30d
    const customCost: CostMetrics = {
      by_day: [
        { day: recentDay, model: "model-a", input_tokens: 10, output_tokens: 5, cost_usd: 0.1 },
        { day: oldDay, model: "model-a", input_tokens: 20, output_tokens: 10, cost_usd: 0.2 },
      ],
      by_session: [],
      by_pipeline_run: [],
    };
    const recentRun: PipelineRun = { ...fixturePipelineRuns[0], session_id: "ses_recent", started_at: "2026-07-01T09:00:00Z" };
    const oldRun: PipelineRun = { ...fixturePipelineRuns[0], session_id: "ses_old", run_seq: 1, started_at: "2026-05-01T09:00:00Z" };
    client.metricsCost = async () => customCost;
    client.metricsPipelineRuns = async () => [recentRun, oldRun];

    render(<DashboardTab client={client} />);

    await waitFor(() => expect(screen.getByText("ses_recent")).toBeTruthy());
    expect(screen.queryByText("ses_old")).toBeNull(); // outside the default 30d window

    const costSection = screen.getByText("Cost by model").closest("section")!;
    fireEvent.click(within(costSection).getByRole("button", { name: "Show table" }));
    const rows = within(costSection).getAllByRole("row");
    expect(rows.length).toBe(2); // header + recentDay only, oldDay outside 30d

    fireEvent.click(screen.getByRole("button", { name: "7d" }));
    await waitFor(() => expect(screen.queryByText("ses_recent")).toBeNull()); // outside 7d too

    // Both cost days are now outside 7d — the chart falls back to its empty state.
    expect(within(costSection).getByText("No cost data for this range.")).toBeTruthy();

    // Views with no time axis are unaffected by the preset change.
    expect(screen.getByText("executor")).toBeTruthy();
    expect(screen.getAllByText(String(fixtureAgentLeaderboard.length)).length).toBeGreaterThan(0);
    expect(screen.getByText(/25% completion rate/)).toBeTruthy();
    expect(screen.getByText(fixtureCapabilityUsage[0].capability)).toBeTruthy();
    expect(screen.getByText(new RegExp(`${fixtureTaskOutcomes.total} tasks`))).toBeTruthy();
  });
});
