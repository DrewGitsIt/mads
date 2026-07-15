import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CapabilityUsageTable } from "./CapabilityUsageTable";
import { fixtureCapabilityUsage } from "../client/fixtures";

describe("CapabilityUsageTable", () => {
  it("renders golden invocation counts and flags the never-used capability", () => {
    render(<CapabilityUsageTable usage={fixtureCapabilityUsage} />);

    expect(screen.getByText("used-skill")).toBeTruthy();
    expect(screen.getByText("unused-skill")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
    expect(screen.getByText("Never used")).toBeTruthy();

    const usedRow = screen.getByText("used-skill").closest("tr")!;
    expect(usedRow.textContent).not.toContain("Never used");
  });
});
