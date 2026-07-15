import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { RunHeader } from "./RunHeader";
import { FakePipelineClient } from "../client/fixtures";
import { recordedSessionId, recordedTasks } from "../client/recordedRun";

function isDisabled(el: HTMLElement) {
  return (el as HTMLButtonElement).disabled;
}

describe("RunHeader", () => {
  it("Start is enabled while idle; Cancel is disabled until a run_id exists", () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(
      <RunHeader sessionId={recordedSessionId} client={client} phase="idle" tasks={recordedTasks} />,
    );

    expect(isDisabled(screen.getByRole("button", { name: "Start" }))).toBe(false);
    expect(isDisabled(screen.getByRole("button", { name: "Cancel" }))).toBe(true);
  });

  it("Pause and Resume are always disabled with the unsupported-engine tooltip", () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(
      <RunHeader sessionId={recordedSessionId} client={client} phase="running" tasks={recordedTasks} />,
    );

    const pause = screen.getByRole("button", { name: "Pause" });
    const resume = screen.getByRole("button", { name: "Resume" });
    expect(isDisabled(pause)).toBe(true);
    expect(isDisabled(resume)).toBe(true);
    expect(pause.title).toBe("not supported by the current pipeline engine");
    expect(resume.title).toBe("not supported by the current pipeline engine");
  });

  it("Cancel becomes enabled once Start has produced a run_id, and calls cancelPipeline with it", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    let cancelledRunId: string | undefined;
    client.cancelPipeline = async (runId: string) => {
      cancelledRunId = runId;
    };
    const { rerender } = render(
      <RunHeader sessionId={recordedSessionId} client={client} phase="idle" tasks={recordedTasks} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    await screen.findByRole("button", { name: "Start" });

    rerender(
      <RunHeader sessionId={recordedSessionId} client={client} phase="running" tasks={recordedTasks} />,
    );

    await waitFor(() => expect(isDisabled(screen.getByRole("button", { name: "Cancel" }))).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(cancelledRunId).toBe("run_fixture"));
  });

  it("calls onRunStarted with the new run_id once Start resolves", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    const onRunStarted = vi.fn();
    render(
      <RunHeader
        sessionId={recordedSessionId}
        client={client}
        phase="idle"
        tasks={recordedTasks}
        onRunStarted={onRunStarted}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    await waitFor(() => expect(onRunStarted).toHaveBeenCalledWith("run_fixture"));
  });

  it("renders task counts by status", () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(
      <RunHeader sessionId={recordedSessionId} client={client} phase="idle" tasks={recordedTasks} />,
    );

    expect(screen.getByText("3 tasks")).toBeTruthy();
    expect(screen.getByText("1 done")).toBeTruthy();
    expect(screen.getByText("1 blocked")).toBeTruthy();
    expect(screen.getByText("0 in progress")).toBeTruthy();
  });

  it("shows a static elapsed time once the run has finished", () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(
      <RunHeader
        sessionId={recordedSessionId}
        client={client}
        phase="finished"
        outcome="incomplete"
        runStartedAt="2026-07-15T05:30:12.265401682Z"
        runEndedAt="2026-07-15T05:30:12.28113183Z"
        tasks={recordedTasks}
      />,
    );

    expect(screen.getByText("0:00")).toBeTruthy();
    expect(screen.getByText("incomplete")).toBeTruthy();
  });
});
