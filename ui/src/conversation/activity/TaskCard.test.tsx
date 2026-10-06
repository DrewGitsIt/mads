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

  it("a running card shows the subagent's current step; a finished one drops it", () => {
    const model: TaskModel = {
      taskId: "tk1",
      toolUseId: "tu1",
      description: "Empirical color harmony science",
      currentStep: "Searching for Ou Luo 2006 colour harmony",
      running: true,
      completed: false,
    };
    const { rerender } = render(<TaskCard tool={spawnTool} model={model} />);
    expect(screen.getByText("Empirical color harmony science")).toBeTruthy();
    expect(screen.getByText("Searching for Ou Luo 2006 colour harmony")).toBeTruthy();
    rerender(<TaskCard tool={spawnTool} model={{ ...model, running: false, completed: true }} />);
    expect(screen.queryByText("Searching for Ou Luo 2006 colour harmony")).toBeNull();
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

  it("a blocked spawn shows blocked, beating the refusal's is_error result and a lifecycle", () => {
    const refused = { ...spawnTool, result: { text: "denied", is_error: true } };
    const { unmount } = render(<TaskCard tool={refused} status="blocked" />);
    expect(screen.getByRole("img", { name: "blocked by permissions" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "failed" })).toBeNull();
    unmount();

    // Stamped on the wire rather than out-of-band, with a running model.
    const model: TaskModel = { taskId: "tk1", toolUseId: "tu1", running: true, completed: false };
    render(<TaskCard tool={{ ...spawnTool, status: "blocked" }} model={model} />);
    expect(screen.getByRole("img", { name: "blocked by permissions" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "in progress" })).toBeNull();
  });

  it("holds the permission cards anchored to it (the subagent's own calls)", () => {
    const onResolve = vi.fn().mockResolvedValue(undefined);
    render(
      <TaskCard
        tool={spawnTool}
        permissions={[
          { id: "perm-9", tool: "Bash", status: "pending", tool_use_id: "sub1", parent_tool_use_id: "tu1" },
        ]}
        onResolvePermission={onResolve}
      />,
    );
    const card = screen.getByTestId("task-card");
    expect(card.textContent).toContain("awaiting your decision");
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    expect(onResolve).toHaveBeenCalledWith("perm-9", "deny");
  });

  it("click opens the subagent stream", () => {
    const onOpen = vi.fn();
    render(<TaskCard tool={spawnTool} onOpen={onOpen} />);
    fireEvent.click(screen.getByRole("button"));
    expect(onOpen).toHaveBeenCalled();
  });
});
