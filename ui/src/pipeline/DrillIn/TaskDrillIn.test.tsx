import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TaskDrillIn } from "./TaskDrillIn";
import type { PipelineOutputSource } from "../client/pipelineClient";
import type { StageAttempt } from "../state/usePipelineRun";
import { recordedTaskIds, recordedTasks } from "../client/recordedRun";

const flakyApi = recordedTasks.find((t) => t.id === recordedTaskIds.flakyApi)!;

const attempts: StageAttempt[] = [
  { stage: "architect", attempt: 1, status: "completed", startedSeq: 1 },
  { stage: "code_review", attempt: 3, status: "failed", reason: "missing error handling", startedSeq: 2 },
];

function makeStubSource(): PipelineOutputSource {
  return { onChunk: vi.fn(() => () => {}), close: vi.fn() };
}

describe("TaskDrillIn", () => {
  it("mounts a resizable panel hosting the timeline, touched files, and agent output", async () => {
    const source = makeStubSource();
    const { container } = render(
      <TaskDrillIn task={flakyApi} attempts={attempts} touchedFiles={["a.go", "b.go"]} outputSource={source} />,
    );

    expect(container.querySelector('[data-testid="resize-handle"]')).toBeTruthy();
    await waitFor(() => expect(container.querySelector('[data-testid="terminal-view"]')).toBeTruthy());

    expect(screen.getByText("flaky api")).toBeTruthy();
    expect(screen.getByText("code_review")).toBeTruthy();
    expect(screen.getByText("loop-back 3")).toBeTruthy();
    expect(screen.getByText("2 files touched")).toBeTruthy();
  });

  it("calls onClose when the close control is clicked", () => {
    const source = makeStubSource();
    const onClose = vi.fn();
    render(
      <TaskDrillIn
        task={flakyApi}
        attempts={attempts}
        touchedFiles={[]}
        outputSource={source}
        onClose={onClose}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "close" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
