import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DPACard } from "./DPACard";
import type { DPA } from "../../../router/types";

function makeDpa(overrides: Partial<DPA>): DPA {
  return {
    id: "dpa_1",
    problem: "Problem text",
    evidence: "Evidence text",
    root_cause: "Root cause text",
    remediation_layer: "tool_config",
    suggested_action: "Do the thing",
    status: "accepted",
    ...overrides,
  };
}

describe("DPACard", () => {
  it("renders the DPA fields", () => {
    render(<DPACard dpa={makeDpa({})} onToggle={() => {}} />);
    expect(screen.getByText("Problem text")).toBeTruthy();
    expect(screen.getByText("Evidence text")).toBeTruthy();
    expect(screen.getByText("Root cause text")).toBeTruthy();
    expect(screen.getByText("Do the thing")).toBeTruthy();
    expect(screen.getByText("tool_config")).toBeTruthy();
  });

  it("shows Accepted for an accepted DPA and Rejected for a rejected one", () => {
    const { rerender } = render(<DPACard dpa={makeDpa({ status: "accepted" })} onToggle={() => {}} />);
    expect(screen.getByText("Accepted")).toBeTruthy();

    rerender(<DPACard dpa={makeDpa({ status: "rejected" })} onToggle={() => {}} />);
    expect(screen.getByText("Rejected")).toBeTruthy();
  });

  it("calls onToggle with the DPA id when the status chip is clicked", () => {
    const onToggle = vi.fn();
    render(<DPACard dpa={makeDpa({ id: "dpa_7", status: "accepted" })} onToggle={onToggle} />);
    fireEvent.click(screen.getByText("Accepted"));
    expect(onToggle).toHaveBeenCalledWith("dpa_7");
  });
});
