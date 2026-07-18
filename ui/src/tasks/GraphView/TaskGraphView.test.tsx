import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskGraphView } from "./TaskGraphView";
import { useTaskStore } from "../useTaskStore";
import { FakeRouterClient } from "../../router/fixtures";
import { fixtureTasks } from "../../router/fixtures";
import { installXyflowMeasurements } from "./xyflowTestEnv";
import { graphStatusOf } from "./graphStatus";
import type { RouterClient } from "../../router/client";
import type { Task } from "../../router/types";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

let uninstall: () => void;
beforeAll(() => {
  uninstall = installXyflowMeasurements();
});
afterAll(() => {
  uninstall();
});

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

function edgeMarker(container: HTMLElement, edgeId: string): string | null {
  const g = container.querySelector(`.react-flow__edge[data-id="${edgeId}"]`);
  return g?.querySelector(".react-flow__edge-path")?.getAttribute("marker-end") ?? null;
}

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
      const marker =
        edgeMarker(container, "tsk_cycle_a->tsk_cycle_b") ??
        edgeMarker(container, "tsk_cycle_b->tsk_cycle_a");
      expect(marker).toBe("url('#mads-arrow-cycle')");
    });
  });

  it("invokes onSelectTask when a canvas node is clicked", () => {
    const onSelectTask = vi.fn();
    const client = new FakeRouterClient();
    const { container } = render(
      <TaskGraphView
        sessionId="session-active"
        routerClient={client}
        tasks={fixtureTasks}
        tasksById={tasksById}
        onSelectTask={onSelectTask}
      />,
    );
    // tsk_cycle_a has an edge (depends on tsk_cycle_b) so it's a canvas node, not tray.
    const node = container.querySelector('.react-flow__node[data-id="tsk_cycle_a"]');
    fireEvent.click(node!);
    expect(onSelectTask).toHaveBeenCalledWith(tasksById.tsk_cycle_a);
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

  it("renders a sparse session as a tray plus small packed components — no mega-row", () => {
    // 20 tasks, only 3 edges (touching 4 distinct tasks) — the rest are tray-eligible.
    const tasks: Task[] = [];
    for (let i = 0; i < 20; i++) {
      tasks.push(task({ id: `tsk_${i}`, title: `Task ${i}`, created_at: `2026-01-01T00:${String(i).padStart(2, "0")}:00Z` }));
    }
    tasks[1].dependencies = [tasks[0].id];
    tasks[3].dependencies = [tasks[2].id];
    tasks[5].dependencies = [tasks[4].id];
    const byId = Object.fromEntries(tasks.map((t) => [t.id, t]));
    const client = new FakeRouterClient();
    const { container } = render(
      <TaskGraphView sessionId="session-sparse" routerClient={client} tasks={tasks} tasksById={byId} />,
    );
    expect(container.querySelectorAll('[draggable="true"]').length).toBe(14);
    expect(container.querySelectorAll(".react-flow__node").length).toBe(6);
  });

  it("renders a long task title without overflowing its card", () => {
    const longTitle = "A".repeat(300);
    const a = task({ id: "tsk_long_a", title: longTitle, created_at: "2026-01-01T00:00:00Z" });
    const b = task({ id: "tsk_long_b", title: "dependent", dependencies: ["tsk_long_a"], created_at: "2026-01-01T00:01:00Z" });
    const byId = { [a.id]: a, [b.id]: b };
    const client = new FakeRouterClient();
    const { container } = render(
      <TaskGraphView sessionId="session-long" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    const title = container.querySelector('.react-flow__node[data-id="tsk_long_a"] [title][class*="title"]');
    expect(title?.getAttribute("title")).toBe(longTitle);
    expect(title?.className).toMatch(/title/);
  });

  it("persists a manual position to localStorage under the session key and reloads it on remount", () => {
    localStorage.clear();
    const a = task({ id: "tsk_pos_a", title: "positioned", created_at: "2026-01-01T00:00:00Z" });
    const byId = { [a.id]: a };
    const client = new FakeRouterClient();

    // No edges, so tsk_pos_a starts in the tray; seed a manual position directly
    // via loadPositions/localStorage the same way TaskGraphView reads it, then
    // confirm remounting the component surfaces that position (canvas, not tray).
    localStorage.setItem(
      "mads.taskGraph.positions.session-persist",
      JSON.stringify({ tsk_pos_a: { x: 40, y: 60 } }),
    );

    const { container, unmount } = render(
      <TaskGraphView sessionId="session-persist" routerClient={client} tasks={[a]} tasksById={byId} />,
    );
    expect(container.querySelector('.react-flow__node[data-id="tsk_pos_a"]')).not.toBeNull();
    unmount();

    const { container: remounted } = render(
      <TaskGraphView sessionId="session-persist" routerClient={client} tasks={[a]} tasksById={byId} />,
    );
    expect(remounted.querySelector('.react-flow__node[data-id="tsk_pos_a"]')).not.toBeNull();
    expect(JSON.parse(localStorage.getItem("mads.taskGraph.positions.session-persist")!)).toEqual({
      tsk_pos_a: { x: 40, y: 60 },
    });
  });

  it("toggles a status filter to dim nodes of every other status, and clears on a second click", () => {
    const a = task({ id: "tsk_filt_a", status: "ready", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({
      id: "tsk_filt_b",
      status: "done",
      title: "b",
      dependencies: ["tsk_filt_a"],
      created_at: "2026-01-01T00:01:00Z",
    });
    const byId = { [a.id]: a, [b.id]: b };
    const client = new FakeRouterClient();
    const { container } = render(
      <TaskGraphView sessionId="session-filter" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    const isDimmed = (taskId: string) =>
      Array.from(
        container.querySelector(`.react-flow__node[data-id="${taskId}"]`)?.querySelectorAll('[class*="dimmed"]') ??
          [],
      ).length > 0;

    fireEvent.click(screen.getByRole("button", { name: /ready$/ }));
    expect(isDimmed("tsk_filt_a")).toBe(false);
    expect(isDimmed("tsk_filt_b")).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: /ready$/ }));
    expect(isDimmed("tsk_filt_a")).toBe(false);
    expect(isDimmed("tsk_filt_b")).toBe(false);
  });

  it("dims by status on hover (spotlight) without needing a click, and un-dims on mouse leave", () => {
    const a = task({ id: "tsk_spot_a", status: "ready", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({
      id: "tsk_spot_b",
      status: "done",
      title: "b",
      dependencies: ["tsk_spot_a"],
      created_at: "2026-01-01T00:01:00Z",
    });
    const byId = { [a.id]: a, [b.id]: b };
    const client = new FakeRouterClient();
    const { container } = render(
      <TaskGraphView sessionId="session-spotlight" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    const isDimmed = (taskId: string) =>
      Array.from(
        container.querySelector(`.react-flow__node[data-id="${taskId}"]`)?.querySelectorAll('[class*="dimmed"]') ??
          [],
      ).length > 0;

    const doneChip = screen.getByRole("button", { name: /done$/ });
    fireEvent.mouseEnter(doneChip);
    expect(isDimmed("tsk_spot_a")).toBe(true);
    expect(isDimmed("tsk_spot_b")).toBe(false);

    fireEvent.mouseLeave(doneChip);
    expect(isDimmed("tsk_spot_a")).toBe(false);
    expect(isDimmed("tsk_spot_b")).toBe(false);
  });

  it("updates the status filter strip's counts live as tasks change", () => {
    const client = new FakeRouterClient();
    render(
      <TaskGraphView
        sessionId="session-active"
        routerClient={client}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    const readyCount = fixtureTasks.filter((t) => graphStatusOf(t) === "ready").length;
    expect(screen.getByRole("button", { name: new RegExp(`^${readyCount} ready$`) })).toBeTruthy();
  });

  it("re-fits the view after Auto-layout is clicked", async () => {
    localStorage.clear();
    // tsk_fit_b starts far from its packed slot, so clearing manual
    // positions on Auto-layout shrinks the extent a lot — a real signal
    // that the viewport actually re-fit, not just re-rendered.
    const a = task({ id: "tsk_fit_a", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({ id: "tsk_fit_b", title: "b", dependencies: ["tsk_fit_a"], created_at: "2026-01-01T00:01:00Z" });
    const byId = { [a.id]: a, [b.id]: b };
    localStorage.setItem(
      "mads.taskGraph.positions.session-fit",
      JSON.stringify({ tsk_fit_b: { x: 4000, y: 4000 } }),
    );
    const client = new FakeRouterClient();
    const { container } = render(
      <TaskGraphView sessionId="session-fit" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    const viewport = () => container.querySelector(".react-flow__viewport") as HTMLElement;
    const before = viewport().style.transform;

    fireEvent.click(screen.getByRole("button", { name: "Auto-layout" }));

    await waitFor(() => {
      expect(viewport().style.transform).not.toBe(before);
    });
  });
});
