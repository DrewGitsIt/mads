import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatTiles } from "./StatTiles";
import {
  fixtureAgentLeaderboard,
  fixtureCapabilityUsage,
  fixtureCostMetrics,
  fixturePipelineRuns,
  fixtureTaskOutcomes,
} from "../client/fixtures";

describe("StatTiles", () => {
  it("derives headline numbers from the golden fixtures", () => {
    render(
      <StatTiles
        cost={fixtureCostMetrics}
        agents={fixtureAgentLeaderboard}
        tasks={fixtureTaskOutcomes}
        capabilities={fixtureCapabilityUsage}
        runs={fixturePipelineRuns}
      />,
    );

    // by_session: 0.30 + 0.35 = 0.65
    expect(screen.getByText("$0.65")).toBeTruthy();
    expect(screen.getByText("25%")).toBeTruthy();
    expect(screen.getByText("2")).toBeTruthy(); // agents.length
    // never-used capabilities & runs.length are both 1
    expect(screen.getAllByText("1").length).toBe(2);
  });
});
