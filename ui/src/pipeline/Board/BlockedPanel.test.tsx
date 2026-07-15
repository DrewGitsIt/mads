import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { BlockedPanel } from "./BlockedPanel";
import { usePipelineRun } from "../state/usePipelineRun";
import { FakeEventSource } from "../../router/testFakeEventSource";
import { FakePipelineClient } from "../client/fixtures";
import { recordedEnvelopes, recordedSessionId, recordedTasks } from "../client/recordedRun";

function playRecordedRun() {
  const source = FakeEventSource.instances[0];
  act(() => {
    for (const env of recordedEnvelopes) {
      source.emit(env.type, JSON.stringify(env));
    }
  });
}

describe("BlockedPanel", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders nothing when no task is blocked", () => {
    const { container } = render(<BlockedPanel tasks={recordedTasks.filter((t) => t.status !== "blocked")} taskRunState={{}} />);
    expect(container.firstChild).toBeNull();
  });

  it("surfaces the blocked task with its blocker stage and its full accumulated feedback", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();
    await waitFor(() => expect(result.current.phase).toBe("finished"));

    render(<BlockedPanel tasks={result.current.tasks} taskRunState={result.current.taskRunState} />);

    expect(screen.getByText("flaky api")).toBeTruthy();
    expect(screen.getByText("blocked at code_review")).toBeTruthy();
    expect(screen.getByText(/loop-back 1\/2 to executor/)).toBeTruthy();
    expect(screen.getByText(/loop-back 2\/2 to executor/)).toBeTruthy();
    expect(screen.getByText(/^stage code_review: /)).toBeTruthy();
    expect(screen.getAllByText(/missing error handling/).length).toBe(3);
  });
});
