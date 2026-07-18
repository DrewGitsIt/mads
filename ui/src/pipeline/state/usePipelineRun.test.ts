import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { usePipelineRun } from "./usePipelineRun";
import { FakeEventSource } from "../../router/testFakeEventSource";
import { FakePipelineClient } from "../client/fixtures";
import {
  recordedEnvelopes,
  recordedSessionId,
  recordedTaskIds,
  recordedTasks,
} from "../client/recordedRun";

function newClient() {
  return new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
}

function playRecordedRun() {
  const source = FakeEventSource.instances[0];
  act(() => {
    for (const env of recordedEnvelopes) {
      source.emit(env.type, JSON.stringify(env));
    }
  });
}

describe("usePipelineRun", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("seeds the task list on mount", async () => {
    const client = newClient();
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.tasks.map((t) => t.id)).toEqual(recordedTasks.map((t) => t.id));
    expect(result.current.phase).toBe("idle");
  });

  it("moves a task's card through stages and derives column order from the recorded run", async () => {
    const client = newClient();
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();

    await waitFor(() => expect(result.current.phase).toBe("finished"));
    expect(result.current.columns).toEqual([
      "architect",
      "executor",
      "build",
      "code_review",
      "merge",
      "verify",
    ]);
    expect(result.current.outcome).toBe("incomplete");

    const coreLib = result.current.taskRunState[recordedTaskIds.coreLib];
    expect(coreLib.currentStage).toBe("verify");
    expect(coreLib.attempts.map((a) => a.stage)).toEqual([
      "architect",
      "executor",
      "build",
      "code_review",
      "merge",
      "verify",
    ]);
    expect(coreLib.attempts.every((a) => a.status === "completed")).toBe(true);
  });

  it("renders a loop-back as repeated attempts on the same stage, each carrying the failure feedback", async () => {
    const client = newClient();
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();

    await waitFor(() => expect(result.current.phase).toBe("finished"));
    const flakyApi = result.current.taskRunState[recordedTaskIds.flakyApi];
    const codeReviewAttempts = flakyApi.attempts.filter((a) => a.stage === "code_review");

    expect(codeReviewAttempts.map((a) => a.attempt)).toEqual([1, 2, 3]);
    expect(codeReviewAttempts.every((a) => a.status === "failed")).toBe(true);
    for (const attempt of codeReviewAttempts) {
      expect(attempt.reason).toMatch(/missing error handling/);
    }
    // Loop-backs re-enter executor, not a new column.
    expect(result.current.columns).not.toContain("code_review_retry");
  });

  it("a blocked task's final currentStage reflects where it blocked", async () => {
    const client = newClient();
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();

    await waitFor(() => expect(result.current.phase).toBe("finished"));
    const flakyApi = result.current.taskRunState[recordedTaskIds.flakyApi];
    expect(flakyApi.currentStage).toBe("code_review");

    const blockedTask = result.current.tasks.find((t) => t.id === recordedTaskIds.flakyApi);
    expect(blockedTask?.status).toBe("blocked");
    expect(blockedTask?.notes?.map((n) => n.type)).toEqual([
      "observation",
      "observation",
      "blocker",
    ]);
  });

  it("a task with no folded stage.started stays out of taskRunState (still queued)", async () => {
    const client = newClient();
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.loading).toBe(false));

    playRecordedRun();

    await waitFor(() => expect(result.current.phase).toBe("finished"));
    expect(result.current.taskRunState[recordedTaskIds.cli]).toBeUndefined();
    const cliTask = result.current.tasks.find((t) => t.id === recordedTaskIds.cli);
    expect(cliTask?.status).toBe("ready");
  });

  it("resumes an in-flight run from listRuns/getRunEvents, then keeps folding live SSE from the cursor", async () => {
    const client = newClient();
    client.runs = [
      {
        session_id: recordedSessionId,
        run_seq: 100,
        started_at: "2026-07-16T05:00:00Z",
        outcome: "running",
        tasks_done: 0,
        tasks_blocked: 0,
        tasks_skipped: 0,
        cost_usd: 0,
      },
    ];
    client.runEvents = [
      {
        seq: 100,
        timestamp: "2026-07-16T05:00:00Z",
        type: "pipeline.started",
        session_id: recordedSessionId,
        actor: "pipeline",
        payload: { template: "default-implementation" },
      },
      {
        seq: 101,
        timestamp: "2026-07-16T05:00:01Z",
        type: "stage.started",
        session_id: recordedSessionId,
        task_id: recordedTaskIds.coreLib,
        actor: "pipeline",
        payload: { stage: "architect" },
      },
    ];
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));

    await waitFor(() => expect(result.current.resuming).toBe(false));
    expect(result.current.phase).toBe("running");
    expect(result.current.columns).toEqual(["architect"]);
    expect(result.current.taskRunState[recordedTaskIds.coreLib]?.currentStage).toBe("architect");

    act(() => {
      const env = {
        seq: 102,
        timestamp: "2026-07-16T05:00:02Z",
        type: "stage.completed",
        session_id: recordedSessionId,
        task_id: recordedTaskIds.coreLib,
        actor: "pipeline",
        payload: { stage: "architect" },
      };
      FakeEventSource.instances[0].emit(env.type, JSON.stringify(env));
    });

    await waitFor(() =>
      expect(result.current.taskRunState[recordedTaskIds.coreLib]?.attempts[0]?.status).toBe(
        "completed",
      ),
    );
  });

  it("with no in-flight run, resuming resolves promptly and phase stays idle until a live pipeline.started arrives (regression guard)", async () => {
    const client = newClient();
    client.runs = [];
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));

    await waitFor(() => expect(result.current.resuming).toBe(false));
    expect(result.current.phase).toBe("idle");

    playRecordedRun();

    await waitFor(() => expect(result.current.phase).toBe("finished"));
  });
});
