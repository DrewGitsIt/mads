import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { StageBoard } from "./StageBoard";
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

describe("StageBoard", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("moves task cards through columns as the recorded run's stages advance, and finishes with core lib in Done", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();
    await waitFor(() => expect(result.current.phase).toBe("finished"));

    render(
      <StageBoard
        columns={result.current.columns}
        tasks={result.current.tasks}
        taskRunState={result.current.taskRunState}
      />,
    );

    const doneColumn = screen.getByText("Done").closest("div")!.parentElement!;
    expect(within(doneColumn).getByText("core lib")).toBeTruthy();
  });

  it("excludes a blocked task from every column — BlockedPanel's job, not the board's", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();
    await waitFor(() => expect(result.current.phase).toBe("finished"));

    render(
      <StageBoard
        columns={result.current.columns}
        tasks={result.current.tasks}
        taskRunState={result.current.taskRunState}
      />,
    );
    expect(screen.queryByText("flaky api")).toBeNull();
  });

  it("keeps a never-started task in Queued", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();
    await waitFor(() => expect(result.current.phase).toBe("finished"));
    expect(result.current.taskRunState[recordedTaskIds.cli]).toBeUndefined();

    render(
      <StageBoard
        columns={result.current.columns}
        tasks={result.current.tasks}
        taskRunState={result.current.taskRunState}
      />,
    );

    const queuedColumn = screen.getByText("Queued").closest("div")!.parentElement!;
    expect(within(queuedColumn).getByText("cli")).toBeTruthy();
  });

  it("renders a column per stage seen in the run, in first-seen order, plus Queued and Done", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();
    await waitFor(() => expect(result.current.phase).toBe("finished"));

    render(
      <StageBoard
        columns={result.current.columns}
        tasks={result.current.tasks}
        taskRunState={result.current.taskRunState}
      />,
    );

    expect(
      ["Queued", "architect", "executor", "build", "code_review", "merge", "verify", "Done"].every((label) =>
        screen.getByText(label),
      ),
    ).toBe(true);
  });
});
