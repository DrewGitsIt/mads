import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TaskOutcomesPanel } from "./TaskOutcomesPanel";
import { fixtureTaskOutcomes } from "../client/fixtures";

describe("TaskOutcomesPanel", () => {
  it("renders the golden status distribution using semantic tokens, not chart tokens", () => {
    const { container } = render(<TaskOutcomesPanel outcomes={fixtureTaskOutcomes} />);

    expect(screen.getByText("Done (1)")).toBeTruthy();
    expect(screen.getByText("Ready (1)")).toBeTruthy();
    expect(screen.getByText("Blocked (1)")).toBeTruthy();
    expect(screen.getByText("Won't do (1)")).toBeTruthy();
    expect(screen.getByText("4 tasks · 25% completion rate")).toBeTruthy();
    expect(container.innerHTML).not.toContain("--chart-");
  });

  it("table toggle shows status, count, and share columns", () => {
    render(<TaskOutcomesPanel outcomes={fixtureTaskOutcomes} />);
    fireEvent.click(screen.getByRole("button", { name: "Show table" }));

    const rows = screen.getAllByRole("row");
    expect(rows.length).toBe(5); // header + 4 statuses
    expect(screen.getAllByText("25%").length).toBeGreaterThan(0);
  });
});
