import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import { TaskNode, type TaskNodeType } from "./TaskNode";
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

function renderNode(t: Task, unsequenced = false) {
  const tasksById = { [t.id]: t };
  const node: TaskNodeType = {
    id: t.id,
    type: "taskNode",
    position: { x: 0, y: 0 },
    data: { task: t, tasksById, unsequenced },
  };
  return render(
    <ReactFlowProvider>
      <div style={{ width: 800, height: 600 }}>
        <ReactFlow nodes={[node]} edges={[]} nodeTypes={{ taskNode: TaskNode }} fitView={false} />
      </div>
    </ReactFlowProvider>,
  );
}

describe("TaskNode", () => {
  it("renders the full title in the DOM with a structural clamp class and a full-text title attribute", () => {
    const longTitle = "A very long task title that would overflow a two line card ".repeat(3).trim();
    renderNode(task({ title: longTitle }));
    const titleEl = screen.getByText(longTitle);
    expect(titleEl.className).toMatch(/title/);
    expect(titleEl.getAttribute("title")).toBe(longTitle);
  });

  it("omits the unsequenced marker when data.unsequenced is false", () => {
    renderNode(task({}), false);
    expect(screen.queryByText("unsequenced")).toBeNull();
  });

  it("shows the unsequenced marker when data.unsequenced is true", () => {
    renderNode(task({}), true);
    expect(screen.getByText("unsequenced")).toBeTruthy();
  });

  it("uses statusPresentation for the status chip, same as TaskRow", () => {
    renderNode(task({ status: "needs_review" }));
    expect(screen.getByText("Needs review")).toBeTruthy();
  });

  it("omits the retry indicator when retry_count is 0", () => {
    renderNode(task({ retry_count: 0 }));
    expect(screen.queryByText(/retry/)).toBeNull();
  });

  it("shows a retry indicator when retry_count > 0", () => {
    renderNode(task({ retry_count: 2 }));
    expect(screen.getByText("retry ×2")).toBeTruthy();
  });

  it("renders a target and a source handle with visually-distinct classes", () => {
    const { container } = renderNode(task({}));
    const handles = Array.from(container.querySelectorAll(".react-flow__handle"));
    expect(handles).toHaveLength(2);
    const classNames = handles.map((h) => h.className);
    expect(classNames.some((c) => c.includes("handleIn"))).toBe(true);
    expect(classNames.some((c) => c.includes("handleOut"))).toBe(true);
    expect(classNames[0]).not.toBe(classNames[1]);
  });
});
