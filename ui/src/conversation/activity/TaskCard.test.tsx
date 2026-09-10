import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TaskCard } from "./TaskCard";
import type { TaskModel } from "./conversationItems";

const spawnTool = {
  id: "tu1",
  name: "Task",
  kind: "agent" as const,
  input: { description: "Verify classification end-to-end", subagent_type: "general-purpose" },
};

describe("TaskCard", () => {
  it("running variant: description, subagent type, live counters, spinner", () => {
    const model: TaskModel = {
      taskId: "tk1",
      toolUseId: "tu1",
      description: "Verify classification end-to-end",
      subagentType: "general-purpose",
      totalTokens: 9800,
      toolUses: 4,
      durationMs: 27000,
      running: true,
      completed: false,
    };
    render(<TaskCard tool={spawnTool} model={model} />);
    expect(screen.getByText(/Verify classification end-to-end/)).toBeTruthy();
    expect(screen.getByText(/general-purpose/)).toBeTruthy();
    expect(screen.getByTestId("task-counters").textContent).toBe("9.8k tok · 4 tools · 27.0s");
    expect(screen.getByRole("img", { name: "in progress" })).toBeTruthy();
  });

  it("completed variant: ✓ with the task_notification summary inline", () => {
    const model: TaskModel = {
      taskId: "tk1",
      toolUseId: "tu1",
      description: "Audit sweep idempotency",
      running: false,
      completed: true,
      summary: "Re-sweeping a turn with no deletions records nothing.",
    };
    render(<TaskCard tool={spawnTool} model={model} />);
    expect(screen.getByRole("img", { name: "completed" })).toBeTruthy();
    expect(screen.getByText("Re-sweeping a turn with no deletions records nothing.")).toBeTruthy();
  });

  it("a task with no notification renders interrupted-shape, never a fake spinner (T17)", () => {
    const model: TaskModel = {
      taskId: "tk1",
      toolUseId: "tu1",
      description: "Audit sweep idempotency",
      running: false,
      completed: false,
    };
    render(<TaskCard tool={spawnTool} model={model} />);
    expect(screen.getByRole("img", { name: "interrupted" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "in progress" })).toBeNull();
  });

  it("click opens the subagent stream", () => {
    const onOpen = vi.fn();
    render(<TaskCard tool={spawnTool} onOpen={onOpen} />);
    fireEvent.click(screen.getByRole("button"));
    expect(onOpen).toHaveBeenCalled();
  });
});
