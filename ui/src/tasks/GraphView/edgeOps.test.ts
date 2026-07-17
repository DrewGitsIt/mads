import { describe, expect, it } from "vitest";
import { applyEdgeStrip, invertPlan, planEdgeStrip } from "./edgeOps";
import { FakeRouterClient } from "../../router/fixtures";
import type { Task } from "../../router/types";

function task(overrides: Partial<Task>): Task {
  return {
    id: "tsk_1",
    session_id: "ses_1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "a task",
    source: "user",
    ...overrides,
  };
}

function tasksById(tasks: Task[]): Record<string, Task> {
  return Object.fromEntries(tasks.map((t) => [t.id, t]));
}

const tsk_a = task({ id: "tsk_a" });
const tsk_b = task({ id: "tsk_b", dependencies: ["tsk_a"] });
const tsk_c = task({ id: "tsk_c", dependencies: ["tsk_b"] });

describe("planEdgeStrip", () => {
  it("finds both the node's own dependencies and its dependents' dependencies", () => {
    const plan = planEdgeStrip("tsk_b", tasksById([tsk_a, tsk_b, tsk_c]));
    expect(plan.updates).toEqual(
      expect.arrayContaining([
        { task: tsk_b, nextDependencies: [] },
        { task: tsk_c, nextDependencies: [] },
      ]),
    );
    expect(plan.updates).toHaveLength(2);
  });

  it("omits the node itself when it has no dependencies of its own", () => {
    const plan = planEdgeStrip("tsk_a", tasksById([tsk_a, tsk_b]));
    expect(plan.updates).toEqual([{ task: tsk_b, nextDependencies: [] }]);
  });
});

describe("invertPlan", () => {
  it("round-trips back to the original dependency arrays", () => {
    const plan = planEdgeStrip("tsk_b", tasksById([tsk_a, tsk_b, tsk_c]));
    const inverted = invertPlan(plan);
    expect(inverted.updates).toEqual(
      expect.arrayContaining([
        { task: tsk_b, nextDependencies: ["tsk_a"] },
        { task: tsk_c, nextDependencies: ["tsk_b"] },
      ]),
    );
  });
});

describe("applyEdgeStrip", () => {
  it("calls routerClient.updateTask once per affected task", async () => {
    const client = new FakeRouterClient(undefined, undefined, undefined, undefined, [tsk_a, tsk_b, tsk_c]);
    const plan = planEdgeStrip("tsk_b", tasksById([tsk_a, tsk_b, tsk_c]));
    await applyEdgeStrip(client, plan);

    const updatedB = await client.getTask("tsk_b");
    const updatedC = await client.getTask("tsk_c");
    expect(updatedB.dependencies).toEqual([]);
    expect(updatedC.dependencies).toEqual([]);
  });
});
