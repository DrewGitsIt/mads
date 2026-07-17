import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { usePipelineRunHistory } from "./usePipelineRunHistory";
import { FakePipelineClient } from "../client/fixtures";
import type { Envelope, PipelineRun } from "../../router/types";
import { recordedEnvelopes, recordedSessionId, recordedTaskIds } from "../client/recordedRun";

function makeRun(overrides: Partial<PipelineRun>): PipelineRun {
  return {
    session_id: recordedSessionId,
    run_seq: 1,
    started_at: "2026-07-15T05:00:00Z",
    ended_at: "2026-07-15T05:01:00Z",
    outcome: "completed",
    tasks_done: 0,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0,
    ...overrides,
  };
}

const run2TaskId = "tsk_run2_only";

// A second, independent run appended after the recorded run's own
// pipeline.completed (seq 51) — used to exercise the cross-run upper-bound
// guard (HANDOFF.md §4b/§5).
const run2Envelopes: Envelope[] = [
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
    task_id: run2TaskId,
    actor: "pipeline",
    payload: { stage: "architect" },
  },
  {
    seq: 102,
    timestamp: "2026-07-16T05:00:02Z",
    type: "stage.completed",
    session_id: recordedSessionId,
    task_id: run2TaskId,
    actor: "pipeline",
    payload: { stage: "architect" },
  },
  {
    seq: 103,
    timestamp: "2026-07-16T05:00:03Z",
    type: "pipeline.completed",
    session_id: recordedSessionId,
    actor: "pipeline",
    payload: { outcome: "completed", done: 1, blocked: 0, skipped: 0 },
  },
];

const run1 = makeRun({
  run_seq: 7,
  started_at: "2026-07-15T05:30:12.265401682Z",
  ended_at: "2026-07-15T05:30:12.28113183Z",
  outcome: "incomplete",
  tasks_done: 1,
  tasks_blocked: 1,
  tasks_skipped: 1,
});

const run2 = makeRun({
  run_seq: 100,
  started_at: "2026-07-16T05:00:00Z",
  ended_at: "2026-07-16T05:00:03Z",
  outcome: "completed",
  tasks_done: 1,
});

describe("usePipelineRunHistory", () => {
  it("has no runs and isn't stuck loading when the session has no history", async () => {
    const client = new FakePipelineClient();
    const { result } = renderHook(() => usePipelineRunHistory(recordedSessionId, client, true));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.runs).toEqual([]);
    expect(result.current.selectedRunSeq).toBeUndefined();
    expect(result.current.columns).toEqual([]);
  });

  it("does not fetch while inactive", async () => {
    const client = new FakePipelineClient();
    client.runs = [run1];
    const { result } = renderHook(() => usePipelineRunHistory(recordedSessionId, client, false));

    // Give any errant effect a tick to fire, then confirm nothing happened.
    await act(async () => {});
    expect(result.current.runs).toEqual([]);
  });

  it("sorts runs newest-first and defaults selection to the newest terminal run, skipping a dangling one", async () => {
    const client = new FakePipelineClient();
    const dangling = makeRun({ run_seq: 200, outcome: "running", ended_at: undefined });
    client.runs = [run1, dangling, run2];
    client.runEvents = [...recordedEnvelopes, ...run2Envelopes];

    const { result } = renderHook(() => usePipelineRunHistory(recordedSessionId, client, true));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.runs.map((r) => r.run_seq)).toEqual([200, 100, 7]);
    // 200 is newest but dangling ("running") — the newest terminal run is 100.
    expect(result.current.selectedRunSeq).toBe(100);
  });

  it("selectRun switches the selection and re-folds that run's own events", async () => {
    const client = new FakePipelineClient();
    client.runs = [run1, run2];
    client.runEvents = [...recordedEnvelopes, ...run2Envelopes];

    const { result } = renderHook(() => usePipelineRunHistory(recordedSessionId, client, true));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.selectedRunSeq).toBe(100); // newest terminal run

    act(() => result.current.selectRun(7));
    await waitFor(() => expect(result.current.selectedRunSeq).toBe(7));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.taskRunState[recordedTaskIds.coreLib]).toBeDefined();
  });

  it("bounds an older selected run's fold to its own window, excluding the next-newer run's events", async () => {
    const client = new FakePipelineClient();
    client.runs = [run1, run2];
    client.runEvents = [...recordedEnvelopes, ...run2Envelopes];

    const { result } = renderHook(() => usePipelineRunHistory(recordedSessionId, client, true));
    await waitFor(() => expect(result.current.loading).toBe(false));

    // Default selection is the newest run (100) — its fold must not contain
    // anything from run 1.
    expect(result.current.selectedRunSeq).toBe(100);
    expect(result.current.taskRunState[run2TaskId]).toBeDefined();
    expect(result.current.taskRunState[recordedTaskIds.coreLib]).toBeUndefined();
    expect(result.current.columns).toEqual(["architect"]);

    // Switching to the older run (7) must fold only its own window, not
    // leak run 2's pipeline.started reset or its task state.
    act(() => result.current.selectRun(7));
    await waitFor(() => expect(result.current.selectedRunSeq).toBe(7));
    await waitFor(() =>
      expect(result.current.taskRunState[recordedTaskIds.coreLib]).toBeDefined(),
    );
    expect(result.current.taskRunState[run2TaskId]).toBeUndefined();
    expect(result.current.columns).toEqual([
      "architect",
      "executor",
      "build",
      "code_review",
      "merge",
      "verify",
    ]);
  });
});
