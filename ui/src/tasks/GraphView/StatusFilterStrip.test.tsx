import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { StatusFilterStrip } from "./StatusFilterStrip";
import type { Task } from "../../router/types";

function task(overrides: Partial<Task>): Task {
  return {
    id: "tsk_1",
    session_id: "ses_1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "a task",
    source: "user",
    ...overrides,
  };
}

describe("StatusFilterStrip", () => {
  it("renders all five status buckets with live counts, including a 0 count", () => {
    const tasks = [
      task({ id: "tsk_a", status: "done" }),
      task({ id: "tsk_b", status: "done" }),
      task({ id: "tsk_c", status: "in_progress" }),
      task({ id: "tsk_d", status: "ready" }),
    ];
    render(
      <StatusFilterStrip
        tasks={tasks}
        activeFilter={null}
        onHoverStatus={() => {}}
        onToggleFilter={() => {}}
      />,
    );
    expect(screen.getByText("done").closest("button")?.textContent).toContain("2");
    expect(screen.getByText("in progress").closest("button")?.textContent).toContain("1");
    expect(screen.getByText("ready").closest("button")?.textContent).toContain("1");
    expect(screen.getByText("blocked").closest("button")?.textContent).toContain("0");
    expect(screen.getByText("draft").closest("button")?.textContent).toContain("0");
  });

  it("fires onHoverStatus with the status on mouse enter, and with null on mouse leave", () => {
    const onHoverStatus = vi.fn();
    render(
      <StatusFilterStrip tasks={[]} activeFilter={null} onHoverStatus={onHoverStatus} onToggleFilter={() => {}} />,
    );
    const doneChip = screen.getByText("done").closest("button")!;
    fireEvent.mouseEnter(doneChip);
    expect(onHoverStatus).toHaveBeenCalledWith("done");
    fireEvent.mouseLeave(doneChip);
    expect(onHoverStatus).toHaveBeenCalledWith(null);
  });

  it("fires onToggleFilter with the status on click", () => {
    const onToggleFilter = vi.fn();
    render(
      <StatusFilterStrip tasks={[]} activeFilter={null} onHoverStatus={() => {}} onToggleFilter={onToggleFilter} />,
    );
    fireEvent.click(screen.getByText("blocked").closest("button")!);
    expect(onToggleFilter).toHaveBeenCalledWith("blocked");
  });

  it("marks the active filter's chip pressed", () => {
    render(
      <StatusFilterStrip tasks={[]} activeFilter="blocked" onHoverStatus={() => {}} onToggleFilter={() => {}} />,
    );
    expect(screen.getByText("blocked").closest("button")?.getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByText("ready").closest("button")?.getAttribute("aria-pressed")).toBe("false");
  });
});
