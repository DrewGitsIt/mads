import { describe, expect, it } from "vitest";
import type { Task, TaskStatus } from "../../router/types";
import { pickupLine, PLAN_GROUPS, RUN_GROUPS, runStatusOf, type PickupInfo, type RunMode } from "./runStatus";
import type { TaskRunState } from "../state/usePipelineRun";

function task(id: string, status: TaskStatus, extra: Partial<Task> = {}): Task {
  return { id, session_id: "s", status, retry_count: 0, created_at: "", updated_at: "", title: id, source: "test", ...extra };
}

const a = task("a", "ready");
const b = task("b", "ready", { dependencies: ["a"] });
const c = task("c", "blocked", { dependencies: ["a"] });
const byId = { a, b, c };
const ctx = (mode: RunMode, excluded: string[] = []) => ({ mode, tasksById: byId, excluded: new Set(excluded) });

describe("runStatusOf", () => {
  it("plan: ready tasks will run; a left-out dependency skips its dependents", () => {
    expect(runStatusOf(a, ctx("plan"))).toBe("ready");
    expect(runStatusOf(b, ctx("plan"))).toBe("ready");
    expect(runStatusOf(a, ctx("plan", ["a"]))).toBe("excluded");
    expect(runStatusOf(b, ctx("plan", ["a"]))).toBe("skipped");
  });

  it("drafts never run, in any mode", () => {
    for (const mode of ["plan", "live", "past"] as const) {
      expect(runStatusOf(task("d", "ready", { draft: true }), ctx(mode))).toBe("draft");
    }
  });

  it("live: ready and dependency-waiting tasks are queued; in_progress the run claimed is running", () => {
    expect(runStatusOf(b, ctx("live"))).toBe("queued");
    expect(runStatusOf(c, ctx("live"))).toBe("queued");
    const claimed: Record<string, TaskRunState> = { r: { taskId: "r", attempts: [], touchedFiles: [] } };
    expect(runStatusOf(task("r", "in_progress"), { ...ctx("live"), taskRunState: claimed })).toBe("running");
  });

  it("live: someone else's in-progress task (not claimed, not Interrupted) is not in this run", () => {
    expect(runStatusOf(task("p", "in_progress"), ctx("live"))).toBe("skipped");
  });

  it("live: a dependent of a task the run left out is not in this run, not queued", () => {
    expect(runStatusOf(a, ctx("live", ["a"]))).toBe("excluded");
    expect(runStatusOf(b, ctx("live", ["a"]))).toBe("skipped");
    expect(runStatusOf(c, ctx("live", ["a"]))).toBe("skipped");
    // Two levels down, and through a draft, likewise.
    const d = task("d", "ready", { dependencies: ["b"] });
    const draft = task("x", "ready", { draft: true });
    const e = task("e", "ready", { dependencies: ["x"] });
    const deep = { mode: "live" as const, tasksById: { ...byId, d, x: draft, e }, excluded: new Set(["a"]) };
    expect(runStatusOf(d, deep)).toBe("skipped");
    expect(runStatusOf(e, deep)).toBe("skipped");
    // A dependency that's running is a wait, not a cut.
    const running = task("a", "in_progress");
    expect(runStatusOf(b, { mode: "live", tasksById: { ...byId, a: running }, excluded: new Set() })).toBe("queued");
  });

  it("past: whatever never started was skipped; a real block stays blocked", () => {
    expect(runStatusOf(b, ctx("past"))).toBe("skipped");
    expect(runStatusOf(task("x", "blocked"), ctx("past"))).toBe("blocked");
    expect(runStatusOf(task("y", "done"), ctx("past"))).toBe("done");
  });

  it("plan: an Interrupted task will be picked up; someone else's in-progress task won't", () => {
    const r = task("r", "in_progress");
    const interrupted = new Set(["r"]);
    expect(runStatusOf(r, { ...ctx("plan"), interrupted })).toBe("interrupted");
    expect(runStatusOf(r, ctx("plan"))).toBe("skipped");
    // Excludable like any task.
    expect(runStatusOf(r, { ...ctx("plan", ["r"]), interrupted })).toBe("excluded");
    // A ready task that depends on it will run too.
    const dep = task("d", "ready", { dependencies: ["r"] });
    const tasksById = { ...byId, r, d: dep };
    expect(runStatusOf(dep, { mode: "plan", tasksById, excluded: new Set(), interrupted })).toBe("ready");
    expect(runStatusOf(dep, { mode: "plan", tasksById, excluded: new Set() })).toBe("skipped");
  });

  it("live: an Interrupted task the run hasn't reached reads Interrupted; one it picked up is running", () => {
    const r = task("r", "in_progress");
    const interrupted = new Set(["r"]);
    const picked: Record<string, TaskRunState> = { r: { taskId: "r", attempts: [], touchedFiles: [] } };
    expect(runStatusOf(r, { ...ctx("live"), interrupted })).toBe("interrupted");
    expect(runStatusOf(r, { ...ctx("live"), interrupted, taskRunState: picked })).toBe("running");
  });

  it("past: a task in flight when the run ended reads Interrupted, never Running or Blocked", () => {
    const r = task("r", "in_progress");
    const worked: Record<string, TaskRunState> = { r: { taskId: "r", currentStage: "build", attempts: [], touchedFiles: [] } };
    expect(runStatusOf(r, { ...ctx("past"), taskRunState: worked })).toBe("interrupted");
    expect(runStatusOf(r, ctx("past"))).toBe("skipped");
  });

  it("groups Interrupted after the live work in a run, and under Will run in Plan", () => {
    expect(RUN_GROUPS.map((g) => g.label)).toEqual(["Blocked", "Running", "Queued", "Interrupted", "Done", "Not in this run"]);
    expect(PLAN_GROUPS[0]).toMatchObject({ label: "Will run", statuses: expect.arrayContaining(["interrupted", "ready"]) });
  });
});

describe("pickupLine", () => {
  const info = (over: Partial<PickupInfo> = {}): PickupInfo => ({
    sharedDirectory: false,
    tasks: {
      r: { task_id: "r", mode: "resume", from_stage: "code_review", template_name: "tpl", template_version: 2 },
      s: { task_id: "s", mode: "restart", from_stage: "architect", template_name: "other", template_version: 1 },
    },
    ...over,
  });

  it("says where a run picks the task up", () => {
    expect(pickupLine("r", info())).toBe("Interrupted · resumes at code_review");
    expect(pickupLine("s", info())).toBe("Interrupted · restarts under other, keeps its work");
    expect(pickupLine("x", info())).toBeUndefined();
    expect(pickupLine("r", undefined)).toBeUndefined();
  });

  it("adds shared directory in current-directory mode", () => {
    expect(pickupLine("r", info({ sharedDirectory: true }))).toBe("Interrupted · resumes at code_review · shared directory");
  });
});
