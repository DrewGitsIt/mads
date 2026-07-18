import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { FakeRouterClient } from "../../router/fixtures";
import { useTaskStore } from "../useTaskStore";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { RouterClient } from "../../router/client";
import type { Envelope, Task } from "../../router/types";
import type { GraphCanvasProps } from "./GraphCanvas";
import { TaskGraphView } from "./TaskGraphView";

/**
 * `onNodeDragStop` only fires after xyflow's own pointer-drag gesture
 * settles — simulating that gesture in happy-dom is exactly what the
 * HANDOFF calls out to avoid. Mocking `GraphCanvas` here lets these tests
 * call TaskGraphView's `onDropOnCanvas`/`onNodeDragStop` callback props
 * directly (native HTML5 tray->canvas DnD and real xyflow node/edge/marker
 * rendering are already covered — TaskGraphView.test.tsx and
 * GraphCanvas.test.tsx respectively). This file only exercises the
 * orchestration: tray/canvas membership, the confirm+undo flow, and that
 * the right `routerClient.updateTask` calls happen.
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

function DragHarness({ routerClient, sessionId }: { routerClient: RouterClient; sessionId: string }) {
  const { tasks, tasksById } = useTaskStore(routerClient, sessionId);
  return (
    <TaskGraphView sessionId={sessionId} routerClient={routerClient} tasks={tasks} tasksById={tasksById} />
  );
}

let nextSeq = 1;
function emitTaskEvent(envelope: Omit<Envelope, "seq" | "timestamp" | "actor">) {
  const full: Envelope = { seq: nextSeq++, timestamp: new Date().toISOString(), actor: "test", ...envelope };
  FakeEventSource.instances[0].emit(full.type, JSON.stringify(full));
}

beforeEach(() => {
  capturedProps = null;
  localStorage.clear();
  nextSeq = 1;
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TaskGraphView — tray -> canvas drop", () => {
  it("sets the dropped task's position with no API writes and marks it unsequenced", () => {
    const a = task({ id: "tsk_a", title: "floating" });
    const byId = { [a.id]: a };
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(<TaskGraphView sessionId="ses_drop" routerClient={client} tasks={[a]} tasksById={byId} />);
    act(() => {
      capturedProps!.onDropOnCanvas!("tsk_a", { x: 10, y: 20 });
    });

    expect(capturedProps!.unsequencedIds.has("tsk_a")).toBe(true);
    expect(
      capturedProps!.canvasNodes.some((n) => n.id === "tsk_a" && n.x === 10 && n.y === 20),
    ).toBe(true);
    expect(updateSpy).not.toHaveBeenCalled();
  });
});

describe("TaskGraphView — canvas -> tray drag-out", () => {
  it("skips the confirm dialog and makes no API writes for a zero-edge floating node", () => {
    const a = task({ id: "tsk_a", title: "floating" });
    const byId = { [a.id]: a };
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(<TaskGraphView sessionId="ses_zero" routerClient={client} tasks={[a]} tasksById={byId} />);
    act(() => {
      capturedProps!.onDropOnCanvas!("tsk_a", { x: 10, y: 20 });
    });
    act(() => {
      capturedProps!.onNodeDragStop!("tsk_a", { x: 0, y: 0 }, { x: 10, y: 20 });
    });

    expect(screen.queryByText(/Remove this task's dependencies/)).toBeNull();
    expect(capturedProps!.canvasNodes.some((n) => n.id === "tsk_a")).toBe(false);
    expect(updateSpy).not.toHaveBeenCalled();
  });

  it("opens a confirm dialog for a connected node, strips both directions on confirm, and undo restores the edges", async () => {
    const a = task({ id: "tsk_a", title: "a", created_at: "2026-01-01T00:00:00Z" });
    const b = task({
      id: "tsk_b",
      title: "b",
      dependencies: ["tsk_a"],
      created_at: "2026-01-01T00:01:00Z",
    });
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [a, b]);
    const updateSpy = vi.spyOn(client, "updateTask");

    render(<DragHarness routerClient={client} sessionId="ses_1" />);
    await waitFor(() => expect(capturedProps).not.toBeNull());
    await waitFor(() => expect(capturedProps!.canvasNodes.some((n) => n.id === "tsk_a")).toBe(true));

    act(() => {
      capturedProps!.onNodeDragStop!("tsk_a", { x: 0, y: 0 }, { x: 5, y: 5 });
    });
    expect(screen.getByText(/Remove this task's dependencies/)).toBeTruthy();
    expect(updateSpy).not.toHaveBeenCalled();

    await act(async () => {
      screen.getByRole("button", { name: "Remove edges" }).click();
    });
    await waitFor(() => expect(updateSpy).toHaveBeenCalledTimes(1));
    expect(updateSpy).toHaveBeenCalledWith(expect.objectContaining({ id: "tsk_b", dependencies: [] }));
    expect(screen.getByText("Undo")).toBeTruthy();

    // Mirrors what the real app does after any updateTask call: the owning
    // useTaskStore refetches on a streamed task.updated envelope.
    act(() => {
      emitTaskEvent({ type: "task.updated", task_id: "tsk_b", session_id: "ses_1" });
    });
    await waitFor(() => expect(capturedProps!.canvasNodes.some((n) => n.id === "tsk_a")).toBe(false));

    await act(async () => {
      screen.getByText("Undo").click();
    });
    await waitFor(() => expect(updateSpy).toHaveBeenCalledTimes(2));
    expect(updateSpy).toHaveBeenLastCalledWith(expect.objectContaining({ id: "tsk_b", dependencies: ["tsk_a"] }));

    act(() => {
      emitTaskEvent({ type: "task.updated", task_id: "tsk_b", session_id: "ses_1" });
    });
    await waitFor(() => expect(capturedProps!.canvasNodes.some((n) => n.id === "tsk_a")).toBe(true));
  });

  it("cancelling the confirm dialog makes no API writes and keeps the node on the canvas", () => {
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

    render(<TaskGraphView sessionId="ses_cancel" routerClient={client} tasks={[a, b]} tasksById={byId} />);
    act(() => {
      capturedProps!.onNodeDragStop!("tsk_a", { x: 0, y: 0 }, { x: 5, y: 5 });
    });
    act(() => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });

    expect(screen.queryByText(/Remove this task's dependencies/)).toBeNull();
    expect(updateSpy).not.toHaveBeenCalled();
    expect(capturedProps!.canvasNodes.some((n) => n.id === "tsk_a")).toBe(true);
  });
});
