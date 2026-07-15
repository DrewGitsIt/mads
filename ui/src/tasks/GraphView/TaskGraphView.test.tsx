import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskGraphView } from "./TaskGraphView";
import { useTaskStore } from "../useTaskStore";
import { FakeRouterClient } from "../../router/fixtures";
import { fixtureTasks } from "../../router/fixtures";
import type { RouterClient } from "../../router/client";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

/**
 * TaskGraphView takes tasks/tasksById as plain props (§4/§6) — it does not
 * own useTaskStore itself, TasksTab does. To prove the *graph* re-renders
 * from a streamed task.* event (not just that useTaskStore's state updates),
 * this harness wires the two together the same way TasksTab does.
 */
function GraphHarness({
  routerClient,
  sessionId,
}: {
  routerClient: RouterClient;
  sessionId: string;
}) {
  const { tasks, tasksById } = useTaskStore(routerClient, sessionId);
  return (
    <TaskGraphView
      sessionId={sessionId}
      routerClient={routerClient}
      tasks={tasks}
      tasksById={tasksById}
    />
  );
}

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

  it("renders a new node when a streamed task.created event arrives, via the owning useTaskStore", async () => {
    const client = new FakeRouterClient();
    render(<GraphHarness routerClient={client} sessionId="session-active" />);
    await waitFor(() => {
      expect(screen.getAllByText(/Cycle [AB]/).length).toBe(2);
    });

    const created = await client.createTask({
      session_id: "session-active",
      title: "Streamed into the graph",
    });
    client.taskEventSource.dispatch({
      type: "task.created",
      task_id: created.id,
      session_id: "session-active",
    });

    await waitFor(() =>
      expect(screen.getByText("Streamed into the graph")).toBeTruthy(),
    );
  });

  it("updates an existing node's rendered title when a streamed task.updated event arrives", async () => {
    const client = new FakeRouterClient();
    render(<GraphHarness routerClient={client} sessionId="session-active" />);
    await waitFor(() => {
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy();
    });

    await client.updateTask({
      ...fixtureTasks.find((t) => t.id === "tsk_ready")!,
      title: "Renamed via streamed event",
    });
    client.taskEventSource.dispatch({
      type: "task.updated",
      task_id: "tsk_ready",
      session_id: "session-active",
    });

    await waitFor(() =>
      expect(screen.getByText("Renamed via streamed event")).toBeTruthy(),
    );
    expect(screen.queryByText("Add retry backoff to the ingest worker")).toBeNull();
  });
});
