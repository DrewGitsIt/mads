import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TaskCard } from "./TaskCard";
import type { TaskRunState } from "../state/usePipelineRun";
import { recordedTaskIds, recordedTasks } from "../client/recordedRun";

const flakyApi = recordedTasks.find((t) => t.id === recordedTaskIds.flakyApi)!;

// Mirrors flakyApi's real folded state from the recorded fixture (3rd
// code_review attempt, loop-back reason) — see usePipelineRun.test.ts.
const flakyApiRunState: TaskRunState = {
  taskId: recordedTaskIds.flakyApi,
  currentStage: "code_review",
  touchedFiles: [],
  attempts: [
    { stage: "code_review", attempt: 1, status: "failed", reason: "missing error handling", startedSeq: 1 },
    { stage: "code_review", attempt: 2, status: "failed", reason: "missing error handling", startedSeq: 2 },
    { stage: "code_review", attempt: 3, status: "failed", reason: "missing error handling", startedSeq: 3 },
  ],
};

describe("TaskCard", () => {
  it("shows an attempt badge when the current stage's attempt count is greater than 1", () => {
    render(<TaskCard task={flakyApi} runState={flakyApiRunState} />);
    expect(screen.getByText("attempt 3")).toBeTruthy();
  });

  it("hides the attempt badge on a first attempt", () => {
    const coreLib = recordedTasks.find((t) => t.id === recordedTaskIds.coreLib)!;
    const runState: TaskRunState = {
      taskId: recordedTaskIds.coreLib,
      currentStage: "architect",
      touchedFiles: [],
      attempts: [{ stage: "architect", attempt: 1, status: "started", startedSeq: 1 }],
    };
    render(<TaskCard task={coreLib} runState={runState} />);
    expect(screen.queryByText(/attempt \d/)).toBeNull();
  });

  it("hides the attempt badge with no run state at all", () => {
    render(<TaskCard task={flakyApi} />);
    expect(screen.queryByText(/attempt \d/)).toBeNull();
  });

  it("always shows the task title", () => {
    render(<TaskCard task={flakyApi} runState={flakyApiRunState} />);
    expect(screen.getByText("flaky api")).toBeTruthy();
  });

  it("calls onClick when clicked, and disables the button without one", () => {
    const onClick = vi.fn();
    const { rerender } = render(<TaskCard task={flakyApi} onClick={onClick} />);
    fireEvent.click(screen.getByRole("button"));
    expect(onClick).toHaveBeenCalledTimes(1);

    rerender(<TaskCard task={flakyApi} />);
    expect((screen.getByRole("button") as HTMLButtonElement).disabled).toBe(true);
  });
});
