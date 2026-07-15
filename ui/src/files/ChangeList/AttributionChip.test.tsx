import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { AttributionChip } from "./AttributionChip";

describe("AttributionChip", () => {
  it("labels an actor change with the actor name", () => {
    render(<AttributionChip kind="actor" actor="autonomous-executor" />);
    expect(screen.getByText("autonomous-executor").className).toMatch(/neutral/);
  });

  it("falls back to 'agent' when no actor name is given", () => {
    render(<AttributionChip kind="actor" />);
    expect(screen.getByText("agent")).toBeTruthy();
  });

  it.each([
    ["trailer", "Trailer", "info"],
    ["session", "Session", "info"],
    ["heuristic", "Heuristic", "warn"],
    ["orphan", "Orphan", "warn"],
    ["unattributed", "Unattributed", "draft"],
  ] as const)("renders %s as %s with tone %s", (kind, label, tone) => {
    render(<AttributionChip kind={kind} />);
    expect(screen.getByText(label).className).toMatch(new RegExp(tone));
  });
});
