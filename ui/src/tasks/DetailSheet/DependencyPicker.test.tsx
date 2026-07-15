import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { DependencyPicker } from "./DependencyPicker";
import { fixtureTasks } from "../../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));
const sessionTasks = fixtureTasks;

function byId(id: string) {
  return fixtureTasks.find((t) => t.id === id)!;
}

describe("DependencyPicker", () => {
  it("toggles a dependency on and off", () => {
    const onChange = vi.fn();
    render(
      <DependencyPicker
        task={byId("tsk_queued")}
        tasksById={tasksById}
        sessionTasks={sessionTasks}
        dependencies={["tsk_dep_not_done"]}
        onChange={onChange}
      />,
    );
    fireEvent.click(screen.getByText("Add retry backoff to the ingest worker").closest("label")!.querySelector("input")!);
    expect(onChange).toHaveBeenCalledWith(["tsk_dep_not_done", "tsk_ready"]);
  });

  it("warns when picking a dependency that would create a cycle", () => {
    // tsk_cycle_a already depends on tsk_cycle_b — picking tsk_cycle_a as a
    // dependency of tsk_cycle_b (which has no deps yet here) would close the loop.
    const editedTask = { ...byId("tsk_cycle_b"), dependencies: [] };
    render(
      <DependencyPicker
        task={editedTask}
        tasksById={tasksById}
        sessionTasks={sessionTasks}
        dependencies={[]}
        onChange={vi.fn()}
      />,
    );
    const row = screen.getByText("Cycle A").closest("label")!;
    expect(row.textContent).toMatch(/would create a cycle/);
  });
});
