import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TaskDrillIn } from "./TaskDrillIn";
import type { PipelineOutputSource, PipelineTurnsSource } from "../client/pipelineClient";
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

// Never delivers a turn — every TaskDrillIn test here exercises the
// fallback-to-raw-output path, same as today, since the enhanced feed's own
// behavior is covered by AgentFeedView.test.tsx/DrillInFeed.test.tsx.
function makeStubTurnsSource(): PipelineTurnsSource {
  return { onTurns: vi.fn(() => () => {}), close: vi.fn() };
}

describe("TaskDrillIn", () => {
  it("mounts a resizable panel hosting the timeline, touched files, and agent output", async () => {
    const source = makeStubSource();
    const { container } = render(
      <TaskDrillIn
        task={flakyApi}
        attempts={attempts}
        touchedFiles={["a.go", "b.go"]}
        outputSource={source}
        turnsSource={makeStubTurnsSource()}
      />,
    );

    expect(container.querySelector('[data-testid="resize-handle"]')).toBeTruthy();
    await waitFor(() => expect(container.querySelector('[data-testid="terminal-view"]')).toBeTruthy());

    expect(screen.getByText("flaky api")).toBeTruthy();
    expect(screen.getByText("code_review")).toBeTruthy();
    expect(screen.getByText("loop-back 3")).toBeTruthy();
    expect(screen.getByText("2 files touched")).toBeTruthy();
  });

  it("clamps drag height to tabHeight minus the board sliver when that's tighter than the 85vh cap", () => {
    const originalInnerHeight = window.innerHeight;
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 2000 });
    try {
      const source = makeStubSource();
      const { container } = render(
        <TaskDrillIn
          task={flakyApi}
          attempts={attempts}
          touchedFiles={[]}
          outputSource={source}
          turnsSource={makeStubTurnsSource()}
          tabHeight={400}
        />,
      );
      const handle = container.querySelector('[data-testid="resize-handle"]') as HTMLElement;
      const panel = handle.parentElement as HTMLElement;

      fireEvent.mouseDown(handle, { clientY: 1000 });
      fireEvent.mouseMove(window, { clientY: -100000 }); // drag far past any real clamp
      fireEvent.mouseUp(window);

      // tabHeight(400) - BOARD_SLIVER(96) = 304, well below 2000 * 0.85.
      expect(panel.style.height).toBe("304px");
    } finally {
      Object.defineProperty(window, "innerHeight", { configurable: true, value: originalInnerHeight });
    }
  });

  it("clamps drag height to 85% of tabHeight, not 85% of the full window, when that's tighter", () => {
    const originalInnerHeight = window.innerHeight;
    // A much taller window than the tab itself (app chrome above the
    // tabpanel) — the 85% cap must be measured against tabHeight, or this
    // would wrongly let the panel grow to tabHeight - BOARD_SLIVER (604).
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 2000 });
    try {
      const source = makeStubSource();
      const { container } = render(
        <TaskDrillIn
          task={flakyApi}
          attempts={attempts}
          touchedFiles={[]}
          outputSource={source}
          turnsSource={makeStubTurnsSource()}
          tabHeight={700}
        />,
      );
      const handle = container.querySelector('[data-testid="resize-handle"]') as HTMLElement;
      const panel = handle.parentElement as HTMLElement;

      fireEvent.mouseDown(handle, { clientY: 1000 });
      fireEvent.mouseMove(window, { clientY: -100000 });
      fireEvent.mouseUp(window);

      // tabHeight(700) * 0.85 = 595, tighter than tabHeight - BOARD_SLIVER (604).
      expect(panel.style.height).toBe("595px");
    } finally {
      Object.defineProperty(window, "innerHeight", { configurable: true, value: originalInnerHeight });
    }
  });

  it("falls back to the 85vh cap when no tabHeight is measured", () => {
    const originalInnerHeight = window.innerHeight;
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 1000 });
    try {
      const source = makeStubSource();
      const { container } = render(
        <TaskDrillIn
          task={flakyApi}
          attempts={attempts}
          touchedFiles={[]}
          outputSource={source}
          turnsSource={makeStubTurnsSource()}
        />,
      );
      const handle = container.querySelector('[data-testid="resize-handle"]') as HTMLElement;
      const panel = handle.parentElement as HTMLElement;

      fireEvent.mouseDown(handle, { clientY: 1000 });
      fireEvent.mouseMove(window, { clientY: -100000 });
      fireEvent.mouseUp(window);

      expect(panel.style.height).toBe("850px");
    } finally {
      Object.defineProperty(window, "innerHeight", { configurable: true, value: originalInnerHeight });
    }
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
        turnsSource={makeStubTurnsSource()}
        onClose={onClose}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "close" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
