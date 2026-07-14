import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { IconRail } from "./IconRail";

const items = [
  { id: "runs", label: "Runs", icon: <span>R</span> },
  { id: "queue", label: "Queue", icon: <span>Q</span> },
];

describe("IconRail", () => {
  it("renders every item's label", () => {
    render(<IconRail items={items} />);
    expect(screen.getByLabelText("Runs")).toBeTruthy();
    expect(screen.getByLabelText("Queue")).toBeTruthy();
  });

  it("marks the active item", () => {
    render(<IconRail items={items} activeId="queue" />);
    expect(screen.getByLabelText("Queue").getAttribute("data-active")).toBe(
      "true",
    );
    expect(screen.getByLabelText("Runs").getAttribute("data-active")).toBe(
      "false",
    );
  });

  it("calls onSelect with the item id", () => {
    const onSelect = vi.fn();
    render(<IconRail items={items} onSelect={onSelect} />);
    fireEvent.click(screen.getByLabelText("Runs"));
    expect(onSelect).toHaveBeenCalledWith("runs");
  });

  it("toggles collapsed state via the toggle button", () => {
    render(<IconRail items={items} defaultCollapsed />);
    const nav = screen.getByLabelText("Expand rail").closest("nav")!;
    expect(nav.getAttribute("data-collapsed")).toBe("true");

    fireEvent.click(screen.getByLabelText("Expand rail"));
    expect(nav.getAttribute("data-collapsed")).toBe("false");
  });
});
