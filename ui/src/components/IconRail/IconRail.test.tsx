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

  it("has no pin/collapse toggle", () => {
    render(<IconRail items={items} />);
    expect(screen.queryByLabelText("Pin rail open")).toBeNull();
    expect(screen.queryByLabelText("Collapse rail")).toBeNull();
  });

  it("item click suppresses the hover-peek until the pointer leaves", () => {
    render(<IconRail items={items} />);
    const nav = screen.getByRole("navigation", { name: "Main navigation" });

    fireEvent.mouseOver(nav);
    fireEvent.click(screen.getByLabelText("Runs"));
    expect(nav.getAttribute("data-peek-suppressed")).toBe("true");

    fireEvent.mouseLeave(nav);
    expect(nav.getAttribute("data-peek-suppressed")).toBe("false");
  });

  it("item click's suppression clears once focus leaves the nav", () => {
    render(
      <>
        <IconRail items={items} />
        <button type="button">elsewhere</button>
      </>,
    );
    const nav = screen.getByRole("navigation", { name: "Main navigation" });

    fireEvent.click(screen.getByLabelText("Runs"));
    fireEvent.blur(screen.getByLabelText("Runs"), {
      relatedTarget: screen.getByText("elsewhere"),
    });
    expect(nav.getAttribute("data-peek-suppressed")).toBe("false");
  });
});
