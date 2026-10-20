import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { foldReducer, initialRunFoldState, usePipelineRun } from "./usePipelineRun";
import type { Envelope } from "../../router/types";
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

  it("picks the newest running run when an older one never terminated (stale run doesn't shadow a live one)", async () => {
    const client = newClient();
    client.runs = [
      {
        session_id: recordedSessionId,
        run_seq: 50,
        started_at: "2026-07-19T23:07:00Z",
        outcome: "running",
        tasks_done: 0,
        tasks_blocked: 0,
        tasks_skipped: 0,
        cost_usd: 0,
      },
      {
        session_id: recordedSessionId,
        run_seq: 100,
        started_at: "2026-07-21T09:37:00Z",
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
        timestamp: "2026-07-21T09:37:00Z",
        type: "pipeline.started",
        session_id: recordedSessionId,
        actor: "pipeline",
        payload: { template: "default-implementation" },
      },
      {
        seq: 101,
        timestamp: "2026-07-21T09:37:01Z",
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
    expect(result.current.runStartSeq).toBe(100);
    expect(result.current.columns).toEqual(["architect"]);
    expect(result.current.taskRunState[recordedTaskIds.coreLib]?.currentStage).toBe("architect");
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

  it("a stage.retried bumps `retries` on the open attempt without creating a new attempt (structural retry, not a loop-back)", async () => {
    const client = newClient();
    client.runs = [];
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.resuming).toBe(false));

    act(() => {
      const source = FakeEventSource.instances[0];
      const envs = [
        {
          seq: 200,
          timestamp: "2026-07-16T05:00:00Z",
          type: "pipeline.started",
          session_id: recordedSessionId,
          actor: "pipeline",
          payload: { template: "default-implementation" },
        },
        {
          seq: 201,
          timestamp: "2026-07-16T05:00:01Z",
          type: "stage.started",
          session_id: recordedSessionId,
          task_id: recordedTaskIds.coreLib,
          actor: "pipeline",
          payload: { stage: "build" },
        },
        {
          seq: 202,
          timestamp: "2026-07-16T05:00:02Z",
          type: "stage.retried",
          session_id: recordedSessionId,
          task_id: recordedTaskIds.coreLib,
          actor: "pipeline",
          payload: { stage: "build", attempt: 1, stage_attempt: 2, max: 3, reason: "provider crashed" },
        },
      ];
      for (const env of envs) source.emit(env.type, JSON.stringify(env));
    });

    await waitFor(() => {
      const attempt = result.current.taskRunState[recordedTaskIds.coreLib]?.attempts[0];
      expect(attempt?.retries).toBe(1);
    });
    const attempt = result.current.taskRunState[recordedTaskIds.coreLib]?.attempts[0];
    // Still a single attempt entry — a retry never opens a new stage.started attempt.
    expect(result.current.taskRunState[recordedTaskIds.coreLib]?.attempts).toHaveLength(1);
    expect(attempt?.attempt).toBe(1);
    expect(attempt?.status).toBe("started");

    // A second retried event on the same open attempt keeps incrementing.
    act(() => {
      const env = {
        seq: 203,
        timestamp: "2026-07-16T05:00:03Z",
        type: "stage.retried",
        session_id: recordedSessionId,
        task_id: recordedTaskIds.coreLib,
        actor: "pipeline",
        payload: { stage: "build", attempt: 2, stage_attempt: 2, max: 3, reason: "provider crashed again" },
      };
      FakeEventSource.instances[0].emit(env.type, JSON.stringify(env));
    });

    await waitFor(() => {
      expect(result.current.taskRunState[recordedTaskIds.coreLib]?.attempts[0]?.retries).toBe(2);
    });
  });

  it("a stage.retried with no open attempt for that stage is a no-op", async () => {
    const client = newClient();
    client.runs = [];
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));
    await waitFor(() => expect(result.current.resuming).toBe(false));

    act(() => {
      const source = FakeEventSource.instances[0];
      const envs = [
        {
          seq: 300,
          timestamp: "2026-07-16T05:00:00Z",
          type: "pipeline.started",
          session_id: recordedSessionId,
          actor: "pipeline",
          payload: { template: "default-implementation" },
        },
        {
          seq: 301,
          timestamp: "2026-07-16T05:00:01Z",
          type: "stage.started",
          session_id: recordedSessionId,
          task_id: recordedTaskIds.coreLib,
          actor: "pipeline",
          payload: { stage: "build" },
        },
        {
          seq: 302,
          timestamp: "2026-07-16T05:00:02Z",
          type: "stage.completed",
          session_id: recordedSessionId,
          task_id: recordedTaskIds.coreLib,
          actor: "pipeline",
          payload: { stage: "build" },
        },
      ];
      for (const env of envs) source.emit(env.type, JSON.stringify(env));
    });

    await waitFor(() => {
      expect(result.current.taskRunState[recordedTaskIds.coreLib]?.attempts[0]?.status).toBe(
        "completed",
      );
    });
    const before = result.current.taskRunState[recordedTaskIds.coreLib];

    // The stage already closed out — a late/stray stage.retried for it has no open attempt to bump.
    act(() => {
      const env = {
        seq: 303,
        timestamp: "2026-07-16T05:00:03Z",
        type: "stage.retried",
        session_id: recordedSessionId,
        task_id: recordedTaskIds.coreLib,
        actor: "pipeline",
        payload: { stage: "build", attempt: 1, stage_attempt: 1, max: 3, reason: "provider crashed" },
      };
      FakeEventSource.instances[0].emit(env.type, JSON.stringify(env));
    });

    // Give the fold a tick to (not) apply the event, then assert state is unchanged.
    await Promise.resolve();
    expect(result.current.taskRunState[recordedTaskIds.coreLib]).toEqual(before);
    expect(result.current.taskRunState[recordedTaskIds.coreLib]?.attempts[0]?.retries).toBeUndefined();
  });

  it("reads run_id, template and excluded_task_ids from a resumed run's pipeline.started, so a remount can cancel", async () => {
    const client = newClient();
    client.runs = [
      {
        session_id: recordedSessionId,
        run_seq: 100,
        run_id: "run_abc",
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
        payload: {
          template: "default-implementation",
          run_id: "run_abc",
          excluded_task_ids: [recordedTaskIds.coreLib],
        },
      },
    ];
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));

    await waitFor(() => expect(result.current.resuming).toBe(false));
    expect(result.current.runId).toBe("run_abc");
    expect(result.current.template).toBe("default-implementation");
    expect(result.current.excludedTaskIds).toEqual([recordedTaskIds.coreLib]);
  });

  it("falls back to the run history's run_id when an older pipeline.started carried none", async () => {
    const client = newClient();
    client.runs = [
      {
        session_id: recordedSessionId,
        run_seq: 100,
        run_id: "run_from_history",
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
    ];
    const { result } = renderHook(() => usePipelineRun(recordedSessionId, client));

    await waitFor(() => expect(result.current.resuming).toBe(false));
    expect(result.current.runId).toBe("run_from_history");
    expect(result.current.excludedTaskIds).toEqual([]);
  });
});

describe("foldReducer pipeline.started", () => {
  function started(seq: number, payload: Record<string, unknown>): Envelope {
    return {
      seq,
      timestamp: "2026-10-19T10:00:00Z",
      type: "pipeline.started",
      session_id: "ses_1",
      actor: "pipeline",
      payload,
    };
  }

  it("leaves runId undefined and excludedTaskIds empty for events from before they existed", () => {
    const state = foldReducer(initialRunFoldState, started(1, { template: "default-implementation" }));
    expect(state.phase).toBe("running");
    expect(state.runId).toBeUndefined();
    expect(state.excludedTaskIds).toEqual([]);
  });

  it("treats a null excluded_task_ids as empty", () => {
    const state = foldReducer(initialRunFoldState, started(1, { run_id: "r1", excluded_task_ids: null }));
    expect(state.runId).toBe("r1");
    expect(state.excludedTaskIds).toEqual([]);
  });

  it("a newer run replaces the previous run's id and exclusions", () => {
    let state = foldReducer(initialRunFoldState, started(1, { run_id: "r1", excluded_task_ids: ["t1"] }));
    state = foldReducer(state, started(9, { run_id: "r2" }));
    expect(state.runId).toBe("r2");
    expect(state.excludedTaskIds).toEqual([]);
  });

  it("ignores another run's end event, such as an older run marked lost", () => {
    const failed = (seq: number, payload: Record<string, unknown>): Envelope => ({
      ...started(seq, payload),
      type: "pipeline.failed",
      actor: "router",
    });
    let state = foldReducer(initialRunFoldState, started(9, { run_id: "r2" }));
    state = foldReducer(state, failed(10, { run_id: "r1", reason: "lost" }));
    expect(state.phase).toBe("running");
    state = foldReducer(state, failed(11, { run_id: "r2", reason: "lost" }));
    expect(state.phase).toBe("finished");
    expect(state.outcome).toBe("failed");
  });
});

describe("foldReducer stage timestamps", () => {
  function env(seq: number, type: string, timestamp: string, payload: Record<string, unknown> = {}): Envelope {
    return { seq, timestamp, type, session_id: "ses_1", task_id: "t1", actor: "pipeline", payload };
  }

  it("stamps an attempt with its opening and closing envelopes' timestamps", () => {
    const state = [
      env(1, "pipeline.started", "2026-10-19T10:00:00Z"),
      env(2, "stage.started", "2026-10-19T10:00:05Z", { stage: "architect" }),
      env(3, "stage.retried", "2026-10-19T10:01:00Z", { stage: "architect" }),
      env(4, "stage.failed", "2026-10-19T10:02:10Z", { stage: "architect", reason: "no" }),
      env(5, "stage.started", "2026-10-19T10:02:11Z", { stage: "architect" }),
      env(6, "stage.skipped", "2026-10-19T10:03:00Z", { stage: "docs" }),
    ].reduce(foldReducer, initialRunFoldState);
    const attempts = state.taskRunState.t1.attempts;
    expect(attempts.map((a) => [a.startedAt, a.endedAt])).toEqual([
      ["2026-10-19T10:00:05Z", "2026-10-19T10:02:10Z"],
      ["2026-10-19T10:02:11Z", undefined],
      ["2026-10-19T10:03:00Z", "2026-10-19T10:03:00Z"],
    ]);
  });

  it("the recorded run's attempts all carry both timestamps once it finishes", () => {
    const state = recordedEnvelopes.reduce(foldReducer, initialRunFoldState);
    const attempts = Object.values(state.taskRunState).flatMap((t) => t.attempts);
    expect(attempts.length).toBeGreaterThan(0);
    for (const a of attempts) {
      expect(a.startedAt).toBeTruthy();
      expect(a.endedAt).toBeTruthy();
    }
  });
});
