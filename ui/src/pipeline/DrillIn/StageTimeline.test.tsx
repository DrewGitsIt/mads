import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { attemptDuration, StageTimeline } from "./StageTimeline";
import { usePipelineRun } from "../state/usePipelineRun";
import { FakeEventSource } from "../../router/testFakeEventSource";
import { FakePipelineClient } from "../client/fixtures";
import { recordedEnvelopes, recordedSessionId, recordedTaskIds, recordedTasks } from "../client/recordedRun";

function playRecordedRun() {
  const source = FakeEventSource.instances[0];
  act(() => {
    for (const env of recordedEnvelopes) {
      source.emit(env.type, JSON.stringify(env));
    }
  });
}

describe("StageTimeline", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("marks flaky api's repeated code_review attempts as loop-backs and shows the failure feedback", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();
    await waitFor(() => expect(result.current.phase).toBe("finished"));

    const flakyApi = result.current.taskRunState[recordedTaskIds.flakyApi];
    const codeReviewAttempts = flakyApi.attempts.filter((a) => a.stage === "code_review");
    render(<StageTimeline attempts={codeReviewAttempts} />);

    expect(screen.getAllByText("code_review").length).toBe(3);
    expect(screen.queryByText("loop-back 1")).toBeNull();
    expect(screen.getByText("loop-back 2")).toBeTruthy();
    expect(screen.getByText("loop-back 3")).toBeTruthy();
    // Status is a coloured dot; the word is there for screen readers only.
    expect(screen.getAllByText("failed:").length).toBe(3);
    expect(
      screen.getAllByTestId("stage-timeline-entry").map((li) => li.querySelector("[data-status]")?.getAttribute("data-status")),
    ).toEqual(["failed", "failed", "failed"]);
    expect(screen.getAllByText(/missing error handling/).length).toBe(3);
  });

  it("shows an empty state before any stage has started", () => {
    render(<StageTimeline attempts={[]} />);
    expect(screen.getByText("No stages started yet.")).toBeTruthy();
  });

  it("shows a retried chip for a structural retry, distinct from the loop-back chip", () => {
    render(
      <StageTimeline
        attempts={[
          { stage: "build", attempt: 1, status: "started", retries: 2, startedSeq: 1 },
        ]}
      />,
    );
    expect(screen.getByText("retried ×2")).toBeTruthy();
    expect(screen.queryByText(/loop-back/)).toBeNull();
  });

  it("shows both the loop-back and retried chips together for a stage reached by loop-back that then structurally retried", () => {
    render(
      <StageTimeline
        attempts={[
          { stage: "code_review", attempt: 2, status: "started", retries: 1, startedSeq: 1 },
        ]}
      />,
    );
    expect(screen.getByText("loop-back 2")).toBeTruthy();
    expect(screen.getByText("retried ×1")).toBeTruthy();
  });

  it("omits the retried chip when retries is 0 or unset", () => {
    render(
      <StageTimeline
        attempts={[
          { stage: "build", attempt: 1, status: "started", retries: 0, startedSeq: 1 },
          { stage: "verify", attempt: 1, status: "started", startedSeq: 2 },
        ]}
      />,
    );
    expect(screen.queryByText(/retried/)).toBeNull();
  });

  it("shows each closed attempt's length, and a live one's time so far only with a clock", () => {
    const attempts = [
      { stage: "architect", attempt: 1, status: "completed" as const, startedSeq: 1, endedSeq: 2, startedAt: "2026-10-19T10:00:00Z", endedAt: "2026-10-19T10:02:10Z" },
      { stage: "executor", attempt: 1, status: "started" as const, startedSeq: 3, startedAt: "2026-10-19T10:02:10Z" },
    ];
    const now = new Date("2026-10-19T10:03:52Z").getTime();
    const { rerender } = render(<StageTimeline attempts={attempts} now={now} />);
    expect(screen.getByText("2:10")).toBeTruthy();
    expect(screen.getByText("live 1:42")).toBeTruthy();
    rerender(<StageTimeline attempts={attempts} />);
    expect(screen.queryByText(/live/)).toBeNull();
  });
});

describe("attemptDuration", () => {
  it("has nothing to say for skipped attempts or ones without timestamps", () => {
    expect(attemptDuration({ stage: "a", attempt: 1, status: "skipped", startedSeq: 1, startedAt: "2026-10-19T10:00:00Z", endedAt: "2026-10-19T10:00:00Z" })).toBeUndefined();
    expect(attemptDuration({ stage: "a", attempt: 1, status: "completed", startedSeq: 1, endedSeq: 2 })).toBeUndefined();
  });
});
