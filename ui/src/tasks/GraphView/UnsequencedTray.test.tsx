import { createRef } from "react";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { UnsequencedTray } from "./UnsequencedTray";
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

function tasksById(tasks: Task[]): Record<string, Task> {
  return Object.fromEntries(tasks.map((t) => [t.id, t]));
}

describe("UnsequencedTray", () => {
  it("renders cards in creation order regardless of prop array order", () => {
    const tasks = [
      task({ id: "tsk_c", title: "third", created_at: "2026-07-01T12:00:00Z" }),
      task({ id: "tsk_a", title: "first", created_at: "2026-07-01T09:00:00Z" }),
      task({ id: "tsk_b", title: "second", created_at: "2026-07-01T10:00:00Z" }),
    ];
    render(<UnsequencedTray tasks={tasks} tasksById={tasksById(tasks)} containerRef={createRef()} />);
    const titles = screen.getAllByText(/^(first|second|third)$/).map((el) => el.textContent);
    expect(titles).toEqual(["first", "second", "third"]);
  });

  it("shows the exact caption text", () => {
    render(<UnsequencedTray tasks={[]} tasksById={{}} containerRef={createRef()} />);
    expect(screen.getByText("Runs when a slot opens · in this order.")).toBeTruthy();
  });

  it("renders each card as draggable", () => {
    const tasks = [task({ id: "tsk_a", title: "only" })];
    render(<UnsequencedTray tasks={tasks} tasksById={tasksById(tasks)} containerRef={createRef()} />);
    const card = screen.getByText("only").closest("[draggable]");
    expect(card).toBeTruthy();
    expect(card?.getAttribute("draggable")).toBe("true");
  });
});
