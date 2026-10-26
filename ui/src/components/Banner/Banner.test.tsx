import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { Banner, BANNER_RESOLVED_MS } from "./Banner";

function bus(active: boolean) {
  return (
    <Banner
      active={active}
      tone="warn"
      title="Live updates paused"
      action={{ label: "Reload", onClick: () => {} }}
      resolvedTitle="Live updates resumed"
    />
  );
}

describe("Banner", () => {
  afterEach(() => vi.useRealTimers());

  it("shows while its condition holds and nothing otherwise", () => {
    const { rerender } = render(bus(false));
    expect(screen.queryByRole("status")).toBeNull();
    rerender(bus(true));
    expect(screen.getByRole("status").textContent).toContain("Live updates paused");
    expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  });

  it("a failure is an alert", () => {
    render(<Banner active tone="bad" variant="inline" title="Couldn't load agents" />);
    expect(screen.getByRole("alert")).toBeTruthy();
  });

  it("when the condition clears it says so for a moment, then goes", () => {
    vi.useFakeTimers();
    const { rerender } = render(bus(true));
    rerender(bus(false));
    expect(screen.getByText("Live updates resumed")).toBeTruthy();
    expect(screen.queryByText("Live updates paused")).toBeNull();
    act(() => vi.advanceTimersByTime(BANNER_RESOLVED_MS));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("without a resolved title it just goes", () => {
    const { rerender } = render(<Banner active title="Couldn't load agents" />);
    rerender(<Banner active={false} title="Couldn't load agents" />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("dismissed, it stays hidden for this occurrence and comes back for the next", () => {
    const { rerender } = render(bus(true));
    fireEvent.click(screen.getByLabelText("Dismiss"));
    expect(screen.queryByText("Live updates paused")).toBeNull();
    rerender(bus(true));
    expect(screen.queryByText("Live updates paused")).toBeNull();
    // Clearing after a dismissal doesn't announce a resolution nobody saw.
    rerender(bus(false));
    expect(screen.queryByText("Live updates resumed")).toBeNull();
    rerender(bus(true));
    expect(screen.getByText("Live updates paused")).toBeTruthy();
  });
});
