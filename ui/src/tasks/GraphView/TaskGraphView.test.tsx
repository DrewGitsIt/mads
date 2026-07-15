import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskGraphView } from "./TaskGraphView";
import { FakeRouterClient } from "../../router/fixtures";
import { fixtureTasks } from "../../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

describe("TaskGraphView", () => {
  it("renders one graph node per task and fetches the authoritative cycle set", async () => {
    const client = new FakeRouterClient();
    render(
      <TaskGraphView
        sessionId="session-active"
        routerClient={client}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    expect(screen.getAllByText(/Cycle [AB]/).length).toBe(2);
    await waitFor(async () => {
      const cycles = await client.taskCycles("session-active");
      expect(cycles.length).toBeGreaterThan(0);
    });
  });

  it("highlights cycle edges distinctly once cycles resolve", async () => {
    const client = new FakeRouterClient();
    const { container } = render(
      <TaskGraphView
        sessionId="session-active"
        routerClient={client}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    await waitFor(() => {
      expect(container.querySelector('[class*="edgeCycle"]')).not.toBeNull();
    });
  });

  it("invokes onSelectTask when a node is clicked", () => {
    const onSelectTask = vi.fn();
    const client = new FakeRouterClient();
    render(
      <TaskGraphView
        sessionId="session-active"
        routerClient={client}
        tasks={fixtureTasks}
        tasksById={tasksById}
        onSelectTask={onSelectTask}
      />,
    );
    const readyTitle = screen.getByText("Add retry backoff to the ingest worker");
    fireEvent.click(readyTitle.closest('[role="button"]')!);
    expect(onSelectTask).toHaveBeenCalledWith(
      fixtureTasks.find((t) => t.id === "tsk_ready"),
    );
  });
});
