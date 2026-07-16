import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { IconRail } from "./IconRail";

const items = [
  { id: "runs", label: "Runs", icon: <span>R</span> },
  { id: "queue", label: "Queue", icon: <span>Q</span> },
];

describe("IconRail", () => {
  beforeEach(() => localStorage.clear());

  it("renders every item's label", () => {
    render(<IconRail items={items} />);
    expect(screen.getByLabelText("Runs")).toBeTruthy();
    expect(screen.getByLabelText("Queue")).toBeTruthy();
  });

  it("marks the active item", () => {
    render(<IconRail items={items} activeId="queue" />);
    expect(screen.getByLabelText("Queue").getAttribute("aria-selected")).toBe(
      "true",
    );
    expect(screen.getByLabelText("Runs").getAttribute("aria-selected")).toBe(
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
    const nav = screen.getByLabelText("Pin rail open").closest("nav")!;
    expect(nav.getAttribute("data-collapsed")).toBe("true");

    fireEvent.click(screen.getByLabelText("Pin rail open"));
    expect(nav.getAttribute("data-collapsed")).toBe("false");
  });

  it("caret click while hovered visibly collapses (suppresses the hover-peek)", () => {
    render(<IconRail items={items} defaultCollapsed={false} />);
    const nav = screen.getByLabelText("Collapse rail").closest("nav")!;

    fireEvent.mouseOver(nav);
    fireEvent.click(screen.getByLabelText("Collapse rail"));

    expect(nav.getAttribute("data-collapsed")).toBe("true");
    expect(nav.getAttribute("data-peek-suppressed")).toBe("true");
  });

  it("background click pins the rail open", () => {
    render(<IconRail items={items} defaultCollapsed />);
    const nav = screen.getByLabelText("Pin rail open").closest("nav")!;
    expect(nav.getAttribute("data-collapsed")).toBe("true");

    fireEvent.click(nav);
    expect(nav.getAttribute("data-collapsed")).toBe("false");
  });

  it("nav-item click never toggles collapsed state", () => {
    render(<IconRail items={items} defaultCollapsed />);
    const nav = screen.getByLabelText("Pin rail open").closest("nav")!;
    expect(nav.getAttribute("data-collapsed")).toBe("true");

    fireEvent.click(screen.getByLabelText("Runs"));
    expect(nav.getAttribute("data-collapsed")).toBe("true");
  });

  it("persists the collapsed/pinned choice across mounts via localStorage", () => {
    const { unmount } = render(<IconRail items={items} defaultCollapsed={false} />);
    fireEvent.click(screen.getByLabelText("Collapse rail"));
    unmount();

    render(<IconRail items={items} defaultCollapsed={false} />);
    const nav = screen.getByLabelText("Pin rail open").closest("nav")!;
    expect(nav.getAttribute("data-collapsed")).toBe("true");
  });

  it("restores hover-peek after a full expand/collapse cycle", () => {
    render(<IconRail items={items} defaultCollapsed={false} />);
    const nav = screen.getByLabelText("Collapse rail").closest("nav")!;

    fireEvent.mouseOver(nav);
    fireEvent.click(screen.getByLabelText("Collapse rail"));
    expect(nav.getAttribute("data-peek-suppressed")).toBe("true");

    fireEvent.mouseLeave(nav);
    expect(nav.getAttribute("data-peek-suppressed")).toBe("false");
  });
});
