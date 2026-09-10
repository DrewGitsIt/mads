import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { PlanWidget } from "./PlanWidget";

describe("PlanWidget", () => {
  it("renders done ✓ / current ▸ / pending ○ items with a progress count", () => {
    render(
      <PlanWidget
        todos={[
          { content: "Trace ledger classification path", status: "completed" },
          { content: "Cover added / modified / deleted in tests", status: "in_progress" },
          { content: "Verify end-to-end in the UI", status: "pending" },
        ]}
      />,
    );
    expect(screen.getByText("1 of 3 · updates in place")).toBeTruthy();
    const items = document.querySelectorAll("[data-status]");
    expect(items[0].getAttribute("data-status")).toBe("completed");
    expect(items[0].textContent).toContain("✓");
    expect(items[1].getAttribute("data-status")).toBe("in_progress");
    expect(items[1].textContent).toContain("▸");
    expect(items[2].getAttribute("data-status")).toBe("pending");
    expect(items[2].textContent).toContain("○");
  });

  it("re-render with fresh todos updates the same widget in place", () => {
    const { rerender } = render(
      <PlanWidget todos={[{ content: "a", status: "in_progress" }]} />,
    );
    rerender(<PlanWidget todos={[{ content: "a", status: "completed" }, { content: "b", status: "in_progress" }]} />);
    expect(screen.getAllByTestId("plan-widget")).toHaveLength(1);
    expect(screen.getByText("1 of 2 · updates in place")).toBeTruthy();
  });
});
