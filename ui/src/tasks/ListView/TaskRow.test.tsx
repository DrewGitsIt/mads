import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TaskRow } from "./TaskRow";
import { fixtureTasks } from "../../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

function byId(id: string) {
  const t = fixtureTasks.find((task) => task.id === id);
  if (!t) throw new Error(`fixture ${id} not found`);
  return t;
}

describe("TaskRow", () => {
  it("renders a dependency-queued block calmly (info tone, dashed ring, dep chip)", () => {
    render(<TaskRow task={byId("tsk_queued")} tasksById={tasksById} />);
    expect(screen.getByText("Queued")).toBeTruthy();
    expect(screen.getByText("Design the retry policy")).toBeTruthy();
  });

  it("renders a needs-human block in red, with no dep chips", () => {
    render(<TaskRow task={byId("tsk_needs_human")} tasksById={tasksById} />);
    expect(screen.getByText("Needs human")).toBeTruthy();
  });

  it("renders labels for a plain ready task", () => {
    render(<TaskRow task={byId("tsk_ready")} tasksById={tasksById} />);
    expect(screen.getByText("backend")).toBeTruthy();
  });

  it("omits label chips in compact mode", () => {
    render(<TaskRow task={byId("tsk_ready")} tasksById={tasksById} compact />);
    expect(screen.queryByText("backend")).toBeNull();
  });

  it("invokes onClick with the task when clicked", () => {
    const onClick = vi.fn();
    render(<TaskRow task={byId("tsk_ready")} tasksById={tasksById} onClick={onClick} />);
    fireEvent.click(screen.getByRole("button"));
    expect(onClick).toHaveBeenCalledWith(byId("tsk_ready"));
  });
});
