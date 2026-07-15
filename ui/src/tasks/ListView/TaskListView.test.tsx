import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TaskListView } from "./TaskListView";
import { fixtureTasks } from "../../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

describe("TaskListView", () => {
  it("groups tasks by status when no filter is applied", () => {
    render(<TaskListView tasks={fixtureTasks} tasksById={tasksById} />);
    expect(screen.getByRole("heading", { name: "Ready" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Blocked" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Done" })).toBeTruthy();
    expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy();
  });

  it("omits empty groups", () => {
    const onlyReady = fixtureTasks.filter((t) => t.status === "ready");
    render(<TaskListView tasks={onlyReady} tasksById={tasksById} />);
    expect(screen.getByRole("heading", { name: "Ready" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Blocked" })).toBeNull();
  });

  it("collapses grouping to a flat list when a single status filter is applied", () => {
    render(
      <TaskListView tasks={fixtureTasks} tasksById={tasksById} statusFilter="blocked" />,
    );
    expect(screen.queryByRole("heading")).toBeNull();
    expect(screen.getByText("Roll retry backoff out to the export worker")).toBeTruthy();
    expect(screen.queryByText("Add retry backoff to the ingest worker")).toBeNull();
  });

  it("invokes onSelectTask when a row is clicked", () => {
    const onSelectTask = vi.fn();
    render(
      <TaskListView tasks={fixtureTasks} tasksById={tasksById} onSelectTask={onSelectTask} />,
    );
    fireEvent.click(screen.getByText("Add retry backoff to the ingest worker"));
    expect(onSelectTask).toHaveBeenCalledWith(
      fixtureTasks.find((t) => t.id === "tsk_ready"),
    );
  });
});
