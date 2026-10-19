import { describe, expect, it } from "vitest";
import type { Task, TaskStatus } from "../../router/types";
import { runStatusOf, type RunMode } from "./runStatus";

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

  it("live: ready and dependency-waiting tasks are queued; in_progress is running", () => {
    expect(runStatusOf(b, ctx("live"))).toBe("queued");
    expect(runStatusOf(c, ctx("live"))).toBe("queued");
    expect(runStatusOf(task("r", "in_progress"), ctx("live"))).toBe("running");
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

  it("plan: an in_progress leftover from an interrupted run won't be picked up", () => {
    expect(runStatusOf(task("r", "in_progress"), ctx("plan"))).toBe("skipped");
  });
});
