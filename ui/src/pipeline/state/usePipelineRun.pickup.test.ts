import { describe, expect, it } from "vitest";
import { foldReducer, initialRunFoldState } from "./usePipelineRun";
import type { Envelope } from "../../router/types";

// docs/PIPELINE-LEASES-DESIGN.md B2: stage events carry run_seq and attempt;
// a run that picks an Interrupted task up writes stage.picked_up.
describe("foldReducer picking an Interrupted task up", () => {
  const RUN = 20;
  function env(seq: number, type: string, payload: Record<string, unknown> = {}, task = "t1"): Envelope {
    return { seq, timestamp: "2026-10-19T10:00:00Z", type, session_id: "ses_1", task_id: task, actor: "pipeline", payload };
  }
  const start: Envelope = { ...env(RUN, "pipeline.started", { run_id: "r2" }), task_id: undefined };

  it("records the pickup, and reads attempt numbers from the payload, continuing from the earlier run", () => {
    const state = [
      start,
      env(21, "stage.picked_up", {
        run_seq: RUN,
        mode: "resume",
        from_stage: "code_review",
        resumed_from_run_seq: 4,
        completed_stages: ["executor", "build"],
      }),
      env(22, "stage.started", { stage: "code_review", run_seq: RUN, attempt: 2, resumed: true }),
    ].reduce(foldReducer, initialRunFoldState);
    const t1 = state.taskRunState.t1;
    expect(t1.pickup).toEqual({
      mode: "resume",
      fromStage: "code_review",
      resumedFromRunSeq: 4,
      completedStages: ["executor", "build"],
      seq: 21,
      at: "2026-10-19T10:00:00Z",
    });
    expect(t1.attempts).toEqual([
      expect.objectContaining({ stage: "code_review", attempt: 2, status: "started", resumed: true }),
    ]);
  });

  it("counts entries for older events without an attempt number", () => {
    const state = [
      start,
      env(21, "stage.started", { stage: "executor" }),
      env(22, "stage.failed", { stage: "executor", reason: "no" }),
      env(23, "stage.started", { stage: "executor" }),
    ].reduce(foldReducer, initialRunFoldState);
    expect(state.taskRunState.t1.attempts.map((a) => a.attempt)).toEqual([1, 2]);
  });

  it("a resume that failed closes the resumed attempt and opens a rerun", () => {
    const state = [
      start,
      env(21, "stage.started", { stage: "executor", run_seq: RUN, attempt: 2, resumed: true }),
      env(22, "stage.started", { stage: "executor", run_seq: RUN, attempt: 3, rerun: true, resume_error: "no session" }),
    ].reduce(foldReducer, initialRunFoldState);
    const [resumed, rerun] = state.taskRunState.t1.attempts;
    expect(resumed).toMatchObject({ attempt: 2, status: "failed", reason: "couldn't resume: no session", endedSeq: 22 });
    expect(rerun).toMatchObject({ attempt: 3, status: "started", rerun: true, resumeError: "no session" });
  });

  it("a restart pickup records no completed stages", () => {
    const state = [start, env(21, "stage.picked_up", { run_seq: RUN, mode: "restart", from_stage: "architect" })].reduce(
      foldReducer,
      initialRunFoldState,
    );
    expect(state.taskRunState.t1.pickup).toMatchObject({ mode: "restart", fromStage: "architect", completedStages: [] });
    expect(state.taskRunState.t1.pickup?.resumedFromRunSeq).toBeUndefined();
  });

  it("ignores stage events another run wrote", () => {
    const state = [
      start,
      env(21, "stage.started", { stage: "executor", run_seq: 7, attempt: 1 }),
      env(22, "stage.picked_up", { run_seq: 7, mode: "resume" }),
      env(23, "stage.started", { stage: "build", run_seq: RUN, attempt: 1 }),
    ].reduce(foldReducer, initialRunFoldState);
    expect(state.taskRunState.t1.attempts.map((a) => a.stage)).toEqual(["build"]);
    expect(state.taskRunState.t1.pickup).toBeUndefined();
  });
});

// A live run's claim is the pipeline's own move to in_progress; any other
// actor's is someone else's work, which the run must not read as its own.
describe("foldReducer recording the run's claims", () => {
  const RUN = 30;
  function moved(seq: number, task: string, actor: string, to = "in_progress"): Envelope {
    return {
      seq,
      timestamp: "2026-10-19T10:00:00Z",
      type: "task.status_changed",
      session_id: "ses_1",
      task_id: task,
      actor,
      payload: { from: "ready", to },
    };
  }
  const start: Envelope = {
    seq: RUN,
    timestamp: "2026-10-19T10:00:00Z",
    type: "pipeline.started",
    session_id: "ses_1",
    actor: "pipeline",
    payload: { run_id: "r3" },
  };

  it("a task the pipeline moved to in_progress is the run's, before any stage starts", () => {
    const state = [start, moved(31, "t1", "pipeline")].reduce(foldReducer, initialRunFoldState);
    expect(state.taskRunState.t1).toEqual({ taskId: "t1", attempts: [], touchedFiles: [] });
  });

  it("ignores a person's move, other statuses, and moves from before the run", () => {
    const state = [moved(29, "t0", "pipeline"), start, moved(31, "t1", "user"), moved(32, "t2", "pipeline", "done")].reduce(
      foldReducer,
      initialRunFoldState,
    );
    expect(state.taskRunState).toEqual({});
  });
});
