import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { FakeRouterClient } from "../../router/fixtures";
import { RouterValidationError } from "../../router/client";
import type { RouterClient } from "../../router/client";
import type { Task } from "../../router/types";
import { useTaskStore } from "../useTaskStore";
import type { GraphCanvasProps } from "./GraphCanvas";
import { TaskGraphView } from "./TaskGraphView";

/**
 * Same idiom as TaskGraphView.dragFlow.test.tsx: mock GraphCanvas and call
 * its captured onConnect/onDeleteEdges props directly, since real drag/
 * click gestures aren't reliably simulatable in happy-dom (that's already
 * covered for rendering by GraphCanvas.test.tsx). This file only exercises
 * orchestration: the right routerClient.updateTask calls, the toast, the
 * cycle banner/ring.
 */
let capturedProps: GraphCanvasProps | null = null;
vi.mock("./GraphCanvas", () => ({
  GraphCanvas: (props: GraphCanvasProps) => {
    capturedProps = props;
    return <div data-testid="graph-canvas-stub" />;
  },
}));

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

function Harness({ routerClient, sessionId }: { routerClient: RouterClient; sessionId: string }) {
  const { tasks, tasksById } = useTaskStore(routerClient, sessionId);
  return (
    <TaskGraphView sessionId={sessionId} routerClient={routerClient} tasks={tasks} tasksById={tasksById} />
  );
}

beforeEach(() => {
  capturedProps = null;
  localStorage.clear();
});

describe("TaskGraphView — connect (create dependency)", () => {
  it("appends the dragged-from task to the target's dependencies via updateTask", async () => {
    const a = task({ id: "tsk_a", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({ id: "tsk_b", title: "b", created_at: "2026-01-01T00:01:00Z" });
    const byId = { [a.id]: a, [b.id]: b };
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(
      <TaskGraphView sessionId="ses_connect" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    await waitFor(() => expect(capturedProps).not.toBeNull());

    await act(async () => {
      capturedProps!.onConnect!("tsk_a", "tsk_b");
    });

    await waitFor(() => expect(updateSpy).toHaveBeenCalledTimes(1));
    expect(updateSpy).toHaveBeenCalledWith(
      expect.objectContaining({ id: "tsk_b", dependencies: ["tsk_a"] }),
    );
  });

  it("silently no-ops on a self-loop", async () => {
    const a = task({ id: "tsk_a", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const byId = { [a.id]: a };
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(<TaskGraphView sessionId="ses_self" routerClient={client} tasks={[a]} tasksById={byId} />);
    await waitFor(() => expect(capturedProps).not.toBeNull());

    await act(async () => {
      capturedProps!.onConnect!("tsk_a", "tsk_a");
    });

    expect(updateSpy).not.toHaveBeenCalled();
  });

  it("surfaces a cross-session (or any updateTask) rejection as an error toast", async () => {
    const a = task({ id: "tsk_a", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({ id: "tsk_b", title: "b", created_at: "2026-01-01T00:01:00Z" });
    const byId = { [a.id]: a, [b.id]: b };
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b]);
    vi.spyOn(client, "updateTask").mockRejectedValue(
      new RouterValidationError([{ field: "dependencies", message: "belongs to another session" }]),
    );

    render(
      <TaskGraphView sessionId="ses_reject" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    await waitFor(() => expect(capturedProps).not.toBeNull());

    await act(async () => {
      capturedProps!.onConnect!("tsk_a", "tsk_b");
    });

    await waitFor(() =>
      expect(screen.getByText("Couldn't create dependency")).toBeTruthy(),
    );
    expect(screen.getByText(/belongs to another session/)).toBeTruthy();
    expect(screen.queryByText("Undo")).toBeNull();
  });
});

describe("TaskGraphView — delete edge with undo", () => {
  it("prunes the dependency, shows an undo toast, and undo restores it", async () => {
    const a = task({ id: "tsk_a", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({
      id: "tsk_b",
      title: "b",
      dependencies: ["tsk_a"],
      created_at: "2026-01-01T00:01:00Z",
    });
    const byId = { [a.id]: a, [b.id]: b };
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(
      <TaskGraphView sessionId="ses_del" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    await waitFor(() => expect(capturedProps).not.toBeNull());

    await act(async () => {
      capturedProps!.onDeleteEdges!([{ from: "tsk_a", to: "tsk_b" }]);
    });

    await waitFor(() => expect(updateSpy).toHaveBeenCalledTimes(1));
    expect(updateSpy).toHaveBeenCalledWith(expect.objectContaining({ id: "tsk_b", dependencies: [] }));
    expect(screen.getByText("Dependency removed")).toBeTruthy();
    expect(screen.getByText("Undo")).toBeTruthy();

    await act(async () => {
      screen.getByText("Undo").click();
    });

    await waitFor(() => expect(updateSpy).toHaveBeenCalledTimes(2));
    expect(updateSpy).toHaveBeenLastCalledWith(
      expect.objectContaining({ id: "tsk_b", dependencies: ["tsk_a"] }),
    );
  });

  it("surfaces an updateTask rejection as an error toast with no undo action", async () => {
    const a = task({ id: "tsk_a", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({
      id: "tsk_b",
      title: "b",
      dependencies: ["tsk_a"],
      created_at: "2026-01-01T00:01:00Z",
    });
    const byId = { [a.id]: a, [b.id]: b };
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b]);
    vi.spyOn(client, "updateTask").mockRejectedValue(new Error("network down"));

    render(
      <TaskGraphView sessionId="ses_del_err" routerClient={client} tasks={[a, b]} tasksById={byId} />,
    );
    await waitFor(() => expect(capturedProps).not.toBeNull());

    await act(async () => {
      capturedProps!.onDeleteEdges!([{ from: "tsk_a", to: "tsk_b" }]);
    });

    await waitFor(() => expect(screen.getByText("Couldn't remove dependency")).toBeTruthy());
    expect(screen.getByText("network down")).toBeTruthy();
    expect(screen.queryByText("Undo")).toBeNull();
  });
});

describe("TaskGraphView — cycle warning", () => {
  it("rings exactly the cycle participants and shows the banner, clearing both once the cycle is fixed", async () => {
    const a = task({
      id: "tsk_a",
      title: "a",
      session_id: "ses_cycle",
      dependencies: ["tsk_b"],
      created_at: "2026-01-01T00:00:00Z",
    });
    const b = task({
      id: "tsk_b",
      title: "b",
      session_id: "ses_cycle",
      dependencies: ["tsk_a"],
      created_at: "2026-01-01T00:01:00Z",
    });
    const c = task({ id: "tsk_c", title: "c", session_id: "ses_cycle", created_at: "2026-01-01T00:02:00Z" });
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b, c]);

    render(<Harness routerClient={client} sessionId="ses_cycle" />);
    await waitFor(() => expect(capturedProps).not.toBeNull());
    await waitFor(() => expect(capturedProps!.cycleNodeIds?.has("tsk_a")).toBe(true));
    expect(capturedProps!.cycleNodeIds?.has("tsk_b")).toBe(true);
    expect(capturedProps!.cycleNodeIds?.has("tsk_c")).toBe(false);
    expect(screen.getByText(/cycle — the pipeline will refuse to launch these/)).toBeTruthy();

    // Break the cycle: tsk_b no longer depends on tsk_a.
    await client.updateTask({ ...b, dependencies: [] });
    act(() => {
      client.taskEventSource.dispatch({ type: "task.updated", task_id: "tsk_b", session_id: "ses_cycle" });
    });

    await waitFor(() => expect(capturedProps!.cycleNodeIds?.size ?? 0).toBe(0));
    expect(screen.queryByText(/cycle — the pipeline will refuse to launch these/)).toBeNull();
  });
});

describe("TaskGraphView — tray/marker transitions on edge edits", () => {
  it("connecting an edge onto an unsequenced-marker node clears its marker on both ends", async () => {
    const a = task({ id: "tsk_a", title: "a", session_id: "ses_marker1", created_at: "2026-01-01T00:00:00Z" });
    const b = task({ id: "tsk_b", title: "b", session_id: "ses_marker1", created_at: "2026-01-01T00:01:00Z" });
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(<Harness routerClient={client} sessionId="ses_marker1" />);
    await waitFor(() => expect(capturedProps).not.toBeNull());

    // Give both isolated tasks a manual position so they're canvas nodes
    // (0-edge + manual position -> floating canvas node with marker) —
    // matches how a user would have dragged them out of the tray already.
    act(() => {
      capturedProps!.onDropOnCanvas!("tsk_a", { x: 0, y: 0 });
    });
    act(() => {
      capturedProps!.onDropOnCanvas!("tsk_b", { x: 100, y: 100 });
    });
    await waitFor(() => expect(capturedProps!.unsequencedIds.has("tsk_a")).toBe(true));
    expect(capturedProps!.unsequencedIds.has("tsk_b")).toBe(true);

    await act(async () => {
      capturedProps!.onConnect!("tsk_a", "tsk_b");
    });
    await waitFor(() => expect(updateSpy).toHaveBeenCalledTimes(1));
    expect(updateSpy).toHaveBeenCalledWith(expect.objectContaining({ id: "tsk_b", dependencies: ["tsk_a"] }));

    act(() => {
      client.taskEventSource.dispatch({ type: "task.updated", task_id: "tsk_b", session_id: "ses_marker1" });
    });

    await waitFor(() => expect(capturedProps!.unsequencedIds.has("tsk_b")).toBe(false));
    expect(capturedProps!.unsequencedIds.has("tsk_a")).toBe(false);
  });

  it("removing a manually-positioned node's last edge reinstates its marker instead of returning it to the tray", async () => {
    const a = task({ id: "tsk_a", title: "a", session_id: "ses_marker2", created_at: "2026-01-01T00:00:00Z" });
    const b = task({
      id: "tsk_b",
      title: "b",
      session_id: "ses_marker2",
      dependencies: ["tsk_a"],
      created_at: "2026-01-01T00:01:00Z",
    });
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(<Harness routerClient={client} sessionId="ses_marker2" />);
    await waitFor(() => expect(capturedProps).not.toBeNull());
    await waitFor(() => expect(capturedProps!.canvasNodes.some((n) => n.id === "tsk_b")).toBe(true));

    // b already sits on the canvas because it has an edge; give it a manual
    // position too, as if the user had also dragged it, so removing the
    // edge below exercises the "manual position keeps it out of the tray"
    // branch of the membership rule rather than the no-position branch.
    act(() => {
      capturedProps!.onDropOnCanvas!("tsk_b", { x: 50, y: 60 });
    });

    await act(async () => {
      capturedProps!.onDeleteEdges!([{ from: "tsk_a", to: "tsk_b" }]);
    });
    await waitFor(() => expect(updateSpy).toHaveBeenCalledTimes(1));
    expect(updateSpy).toHaveBeenCalledWith(expect.objectContaining({ id: "tsk_b", dependencies: [] }));

    act(() => {
      client.taskEventSource.dispatch({ type: "task.updated", task_id: "tsk_b", session_id: "ses_marker2" });
    });

    await waitFor(() => expect(capturedProps!.unsequencedIds.has("tsk_b")).toBe(true));
    expect(capturedProps!.canvasNodes.some((n) => n.id === "tsk_b")).toBe(true);
    // Still on the canvas, not back in the tray shelf.
    expect(screen.queryByText("b")).toBeNull();
  });
});
