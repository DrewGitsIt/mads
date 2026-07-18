import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useGraphRunState } from "./useGraphRunState";
import { FakeEventSource } from "../../router/testFakeEventSource";
import { FakePipelineClient } from "../../pipeline/client/fixtures";
import { recordedEnvelopes, recordedSessionId, recordedTaskIds } from "../../pipeline/client/recordedRun";

function playRecordedRun() {
  const source = FakeEventSource.instances[0];
  act(() => {
    for (const env of recordedEnvelopes) {
      source.emit(env.type, JSON.stringify(env));
    }
  });
}

describe("useGraphRunState", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns undefined and never touches EventSource when no client is given", () => {
    const { result } = renderHook(() => useGraphRunState(recordedSessionId, undefined));
    expect(result.current).toBeUndefined();
    expect(FakeEventSource.instances).toHaveLength(0);
  });

  it("folds a live run's stage events into taskRunState/columns, given a client", async () => {
    const client = new FakePipelineClient();
    const { result } = renderHook(() => useGraphRunState(recordedSessionId, client));

    playRecordedRun();

    await waitFor(() => expect(result.current?.phase).toBe("finished"));
    expect(result.current?.columns).toEqual([
      "architect",
      "executor",
      "build",
      "code_review",
      "merge",
      "verify",
    ]);
    expect(result.current?.taskRunState[recordedTaskIds.coreLib]?.currentStage).toBe("verify");
  });

  it("resumes a blocked task's already-finished run from listRuns/getRunEvents, with no live SSE needed", async () => {
    const client = new FakePipelineClient();
    client.runs = [
      {
        session_id: recordedSessionId,
        run_seq: 1,
        started_at: "2026-07-15T05:30:12.262609722Z",
        ended_at: "2026-07-15T05:30:12.28113183Z",
        outcome: "incomplete",
        tasks_done: 1,
        tasks_blocked: 1,
        tasks_skipped: 1,
        cost_usd: 0,
      },
    ];
    client.runEvents = recordedEnvelopes;
    const { result } = renderHook(() => useGraphRunState(recordedSessionId, client));

    await waitFor(() => expect(result.current?.phase).toBe("finished"));
    expect(result.current?.columns).toEqual([
      "architect",
      "executor",
      "build",
      "code_review",
      "merge",
      "verify",
    ]);
    expect(result.current?.taskRunState[recordedTaskIds.flakyApi]?.currentStage).toBe("code_review");
  });
});
