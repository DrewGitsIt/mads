import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CostByModelChart } from "./CostByModelChart";
import { fixtureCostMetrics } from "../client/fixtures";

describe("CostByModelChart", () => {
  it("renders a legend swatch per first-seen model using only chart tokens, no raw hex", () => {
    const { container } = render(<CostByModelChart byDay={fixtureCostMetrics.by_day} />);
    expect(screen.getByText("model-a")).toBeTruthy();
    expect(screen.getByText("model-b")).toBeTruthy();
    expect(container.innerHTML).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(container.innerHTML).toContain("var(--chart-1)");
    expect(container.innerHTML).toContain("var(--chart-2)");
  });

  it("shows a per-column tooltip with that day's model breakdown on hover", () => {
    render(<CostByModelChart byDay={fixtureCostMetrics.by_day} />);
    expect(screen.queryByRole("tooltip")).toBeNull();

    const [firstColumn] = document.querySelectorAll('[class*="column"]');
    fireEvent.mouseEnter(firstColumn);

    expect(screen.getByRole("tooltip").textContent).toContain("2026-07-10");
    expect(screen.getByText("$0.10")).toBeTruthy();
    expect(screen.getByText("$0.30")).toBeTruthy();
  });

  it("table toggle shows one row per day with per-model and total columns matching the golden aggregates", () => {
    render(<CostByModelChart byDay={fixtureCostMetrics.by_day} />);
    fireEvent.click(screen.getByRole("button", { name: "Show table" }));

    const rows = screen.getAllByRole("row");
    expect(rows.length).toBe(3); // header + 2 days

    const day1Row = screen.getByText("2026-07-10").closest("tr")!;
    expect(day1Row.textContent).toContain("0.10"); // model-a
    expect(day1Row.textContent).toContain("0.30"); // model-b
    expect(day1Row.textContent).toContain("0.40"); // total

    const day2Row = screen.getByText("2026-07-11").closest("tr")!;
    expect(day2Row.textContent).toContain("0.25"); // model-a
    expect(day2Row.textContent).toContain("0.00"); // model-b
    expect(day2Row.textContent).toContain("0.25"); // total
  });
});
