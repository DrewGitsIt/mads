import { describe, expect, it } from "vitest";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { PickupInfo } from "../../pipeline/plan/runStatus";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import { nodeRunViews, RUN_STATUS_LABEL, RUN_STATUS_TONE, stageStatesOf } from "./runView";

const stages: RunStage[] = ["architect", "executor", "code_review", "merge"].map((name) => ({ name, kind: "agent" }));
const task: Task = {
  id: "t",
  session_id: "s",
  status: "in_progress",
  retry_count: 0,
  created_at: "",
  updated_at: "",
  title: "t",
  source: "user",
};
const pickup: PickupInfo = {
  sharedDirectory: false,
  tasks: { t: { task_id: "t", mode: "resume", from_stage: "code_review", template_name: "tpl", template_version: 1 } },
};
const states = (cells: { state: string }[]) => cells.map((c) => c.state);

describe("graph run view: Interrupted", () => {
  it("has its own word and a calm tone, not Blocked's", () => {
    expect(RUN_STATUS_LABEL.interrupted).toBe("Interrupted");
    expect(RUN_STATUS_TONE.interrupted).toBe("warn");
    expect(RUN_STATUS_TONE.interrupted).not.toBe(RUN_STATUS_TONE.blocked);
  });

  it("Plan: the stages a resume skips show done; the rest wait", () => {
    expect(states(stageStatesOf(task, "interrupted", "plan", stages, undefined, pickup))).toEqual([
      "done",
      "done",
      "waiting",
      "waiting",
    ]);
  });

  it("past: the stage it was in when the run ended shows stopped", () => {
    const rs: TaskRunState = {
      taskId: "t",
      currentStage: "executor",
      touchedFiles: [],
      attempts: [
        { stage: "architect", attempt: 1, status: "completed", startedSeq: 1 },
        { stage: "executor", attempt: 1, status: "started", startedSeq: 2 },
      ],
    };
    expect(states(stageStatesOf(task, "interrupted", "past", stages, rs))).toEqual(["done", "stopped", "waiting", "waiting"]);
  });

  it("live: a picked-up task shows the stages an earlier run completed as done", () => {
    const rs: TaskRunState = {
      taskId: "t",
      currentStage: "code_review",
      touchedFiles: [],
      attempts: [{ stage: "code_review", attempt: 2, status: "started", startedSeq: 5, resumed: true }],
      pickup: { mode: "resume", fromStage: "code_review", completedStages: ["architect", "executor"], seq: 4 },
    };
    expect(states(stageStatesOf(task, "running", "live", stages, rs))).toEqual(["done", "done", "now", "waiting"]);
  });

  it("Plan: the status line says where it picks up", () => {
    const views = nodeRunViews([task], {
      mode: "plan",
      tasksById: { t: task },
      excluded: new Set(),
      stages,
      taskRunState: {},
      pickup,
      interrupted: new Set(["t"]),
    });
    expect(views.t.status).toBe("interrupted");
    expect(views.t.line).toEqual({ text: "Interrupted · resumes at code_review", tone: "warn" });
    expect(views.t.include).toEqual({ checked: true, disabled: false });
  });
});
