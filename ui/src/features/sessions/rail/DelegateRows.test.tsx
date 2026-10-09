import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DelegateRows, RECENT_DELEGATE_MS, visibleDelegates } from "./DelegateRows";
import type { Delegate } from "../../../router/types";

const NOW = Date.parse("2026-10-08T12:00:00Z");

function d(id: string, extra: Partial<Delegate> = {}): Delegate {
  return {
    id: `dlg_${id}`,
    session_id: "s1",
    harness_id: id,
    description: `task ${id}`,
    type: "general-purpose",
    state: "running",
    runs: 1,
    started_at: "2026-10-08T11:58:00Z",
    created_at: "2026-10-08T11:58:00Z",
    updated_at: "2026-10-08T11:58:00Z",
    ...extra,
  };
}

describe("visibleDelegates", () => {
  it("keeps every running delegate and finished ones only briefly", () => {
    const fresh = d("fresh", { state: "completed", ended_at: new Date(NOW - 10_000).toISOString() });
    const stale = d("stale", { state: "failed", ended_at: new Date(NOW - RECENT_DELEGATE_MS - 1).toISOString() });
    expect(visibleDelegates([d("a"), fresh, stale], NOW).map((x) => x.harness_id)).toEqual(["a", "fresh"]);
  });
});

describe("DelegateRows", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
  });
  afterEach(() => vi.useRealTimers());

  it("renders one row per delegate with a live clock while running and the ending's glyph after", () => {
    render(
      <DelegateRows
        delegates={[
          d("run"),
          d("done", { state: "failed", reason: "failed", ended_at: new Date(NOW - 5_000).toISOString(), duration_ms: 31_300 }),
        ]}
      />,
    );
    const rows = screen.getAllByRole("button");
    expect(rows[0].textContent).toContain("task run");
    expect(rows[0].textContent).toContain("2m 00s");
    expect(rows[0].querySelector('[data-status="in_progress"]')).toBeTruthy();
    expect(rows[1].textContent).toContain("31.3s");
    expect(rows[1].querySelector('[data-status="failed"]')).toBeTruthy();
  });

  it("folds past three rows behind '+N more'", () => {
    render(<DelegateRows delegates={["a", "b", "c", "e", "f"].map((id) => d(id))} />);
    expect(screen.getAllByText(/^task /)).toHaveLength(3);
    fireEvent.click(screen.getByText("+2 more"));
    expect(screen.getAllByText(/^task /)).toHaveLength(5);
    expect(screen.getByText("Show fewer")).toBeTruthy();
  });

  it("opens the delegate it names, and marks the open one selected", () => {
    const onOpen = vi.fn();
    render(<DelegateRows delegates={[d("a"), d("b")]} selectedHarnessId="b" onOpen={onOpen} />);
    fireEvent.click(screen.getByText("task a"));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ harness_id: "a" }));
    expect(screen.getByText("task b").closest("button")?.hasAttribute("data-selected")).toBe(true);
  });

  it("renders nothing once every delegate has aged out", () => {
    const { container } = render(
      <DelegateRows delegates={[d("old", { state: "completed", ended_at: "2026-10-08T11:00:00Z" })]} />,
    );
    expect(container.innerHTML).toBe("");
  });
});
