import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AgentLeaderboardTable } from "./AgentLeaderboardTable";
import { fixtureAgentLeaderboard } from "../client/fixtures";

describe("AgentLeaderboardTable", () => {
  it("renders one row per agent with the golden aggregates", () => {
    render(<AgentLeaderboardTable entries={fixtureAgentLeaderboard} />);

    expect(screen.getByText("executor")).toBeTruthy();
    expect(screen.getByText("architect")).toBeTruthy();
    // executor: usage_count 3, successes 2, failures 1, success_rate 2/3 -> 67%, retries 1
    const executorRow = screen.getByText("executor").closest("tr")!;
    expect(executorRow.textContent).toContain("3");
    expect(executorRow.textContent).toContain("67%");
    // architect: usage_count 1, successes 1, failures 0, success_rate 1 -> 100%, retries 0
    const architectRow = screen.getByText("architect").closest("tr")!;
    expect(architectRow.textContent).toContain("100%");
  });

  it("renders an empty state with no entries", () => {
    render(<AgentLeaderboardTable entries={[]} />);
    expect(screen.getByText("No agent activity yet.")).toBeTruthy();
  });
});
