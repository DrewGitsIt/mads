import { describe, expect, it } from "vitest";
import { criticalPath, leftOutBy, planWarning, runnableInPlan, waveOf } from "./planScope";
import type { Task, TaskStatus } from "../../router/types";

function task(id: string, dependencies: string[] = [], extra: Partial<Task> = {}): Task {
  return {
    id,
    session_id: "ses_1",
    status: "ready" as TaskStatus,
    dependencies,
    retry_count: 0,
    created_at: "t",
    updated_at: "t",
    title: id,
    source: "user",
    ...extra,
  };
}

function index(tasks: Task[]): Record<string, Task> {
  return Object.fromEntries(tasks.map((t) => [t.id, t]));
}

// The mockup's shape: t1 → t2 → t5, t1 → t3, t4 alone, t7 a draft that t6 needs.
const t1 = task("t1");
const t2 = task("t2", ["t1"]);
const t3 = task("t3", ["t1"]);
const t4 = task("t4");
const t5 = task("t5", ["t2"]);
const t7 = task("t7", [], { draft: true });
const t6 = task("t6", ["t7"]);
const tasks = [t1, t2, t3, t4, t5, t6, t7];
const byId = index(tasks);
const none = new Set<string>();

describe("leftOutBy", () => {
  it("is undefined when nothing upstream is in the way", () => {
    expect(leftOutBy(t5, byId, none)).toBeUndefined();
    expect(leftOutBy(t1, byId, none)).toBeUndefined();
  });

  it("names a directly excluded dependency", () => {
    expect(leftOutBy(t2, byId, new Set(["t1"]))?.id).toBe("t1");
  });

  it("names the excluded task at any depth", () => {
    expect(leftOutBy(t5, byId, new Set(["t1"]))?.id).toBe("t1");
  });

  it("names a draft dependency", () => {
    expect(leftOutBy(t6, byId, none)?.id).toBe("t7");
  });

  it("treats a dependency that is neither ready nor done as in the way", () => {
    const blocked = task("b", [], { status: "blocked" });
    const child = task("c", ["b"]);
    expect(leftOutBy(child, index([blocked, child]), none)?.id).toBe("b");
  });

  it("doesn't look past a done dependency", () => {
    const old = task("old", [], { draft: true });
    const done = task("done", ["old"], { status: "done" });
    const child = task("c", ["done"]);
    expect(leftOutBy(child, index([old, done, child]), none)).toBeUndefined();
  });

  it("ignores ids it doesn't know", () => {
    expect(leftOutBy(task("c", ["ghost"]), byId, none)).toBeUndefined();
  });

  it("terminates on a dependency cycle", () => {
    const a = task("a", ["b"]);
    const b = task("b", ["a"]);
    expect(leftOutBy(a, index([a, b]), none)).toBeUndefined();
  });
});

describe("runnableInPlan", () => {
  it("drops drafts, excluded tasks and everything downstream of them", () => {
    const ids = runnableInPlan(tasks, byId, new Set(["t2"])).map((t) => t.id);
    expect(ids).toEqual(["t1", "t3", "t4"]);
  });

  it("drops tasks that aren't ready", () => {
    const done = task("d", [], { status: "done" });
    const child = task("c", ["d"]);
    expect(runnableInPlan([done, child], index([done, child]), none).map((t) => t.id)).toEqual(["c"]);
  });
});

describe("waveOf", () => {
  it("is 1 with no dependencies and 1 + the deepest dependency otherwise", () => {
    expect(waveOf(t1, byId)).toBe(1);
    expect(waveOf(t2, byId)).toBe(2);
    expect(waveOf(t5, byId)).toBe(3);
    expect(waveOf(t6, byId)).toBe(2);
  });

  it("takes the deepest of several dependencies", () => {
    const join = task("j", ["t4", "t5"]);
    expect(waveOf(join, index([...tasks, join]))).toBe(4);
  });

  it("terminates on a dependency cycle", () => {
    const a = task("a", ["b"]);
    const b = task("b", ["a"]);
    expect(waveOf(a, index([a, b]))).toBe(2);
  });
});

describe("criticalPath", () => {
  it("is the longest chain among runnable tasks, first wave first", () => {
    expect(criticalPath(tasks, byId, none)).toEqual(["t1", "t2", "t5"]);
  });

  it("re-routes when part of the chain is left out", () => {
    expect(criticalPath(tasks, byId, new Set(["t2"]))).toEqual(["t1", "t3"]);
  });

  it("doesn't count dependencies that are already done", () => {
    const done = task("d", [], { status: "done" });
    const a = task("a", ["d"]);
    const b = task("b");
    const c = task("c", ["b"]);
    const all = [done, a, b, c];
    expect(criticalPath(all, index(all), none)).toEqual(["b", "c"]);
  });

  it("is empty when nothing is runnable", () => {
    expect(criticalPath([t7], index([t7]), none)).toEqual([]);
  });

  it("terminates on a dependency cycle", () => {
    const a = task("a", ["b"]);
    const b = task("b", ["a"]);
    expect(criticalPath([a, b], index([a, b]), none).length).toBeGreaterThan(0);
  });
});

describe("planWarning", () => {
  it("names the ready tasks a left-out or draft dependency keeps out, and what they depend on", () => {
    expect(planWarning(tasks, byId, none)).toBe("t6 won't run (depends on t7)");
    expect(planWarning(tasks, byId, new Set(["t1"]))).toBe("t2, t3, t5, t6 won't run (depends on t1, t7)");
  });

  it("says nothing when only a non-ready dependency is in the way", () => {
    const blocked = task("b", [], { status: "blocked" });
    const after = task("a", ["b"]);
    expect(planWarning([blocked, after], index([blocked, after]), none)).toBeUndefined();
  });

  it("doesn't name tasks that are themselves left out", () => {
    expect(planWarning(tasks, byId, new Set(["t6"]))).toBeUndefined();
  });
});
