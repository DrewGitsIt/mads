import { describe, expect, it } from "vitest";
import { layout } from "./layout";
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

describe("layout", () => {
  it("is deterministic across repeated calls on the same input", () => {
    const tasks = [
      task({ id: "tsk_b", title: "second" }),
      task({ id: "tsk_a", title: "first" }),
      task({ id: "tsk_c", title: "third", dependencies: ["tsk_a", "tsk_b"] }),
    ];
    const first = layout(tasks);
    const second = layout([...tasks].reverse());
    expect(second.nodes.map((n) => ({ id: n.id, x: n.x, y: n.y }))).toEqual(
      first.nodes.map((n) => ({ id: n.id, x: n.x, y: n.y })),
    );
  });

  it("places dependency nodes at a shallower rank (smaller y) than dependents", () => {
    const tasks = [
      task({ id: "tsk_a", title: "dep" }),
      task({ id: "tsk_b", title: "dependent", dependencies: ["tsk_a"] }),
    ];
    const { nodes } = layout(tasks);
    const a = nodes.find((n) => n.id === "tsk_a")!;
    const b = nodes.find((n) => n.id === "tsk_b")!;
    expect(a.y).toBeLessThan(b.y);
  });

  it("produces one edge per valid dependency, skipping dangling refs", () => {
    const tasks = [
      task({ id: "tsk_a", title: "dep" }),
      task({ id: "tsk_b", title: "dependent", dependencies: ["tsk_a", "tsk_missing"] }),
    ];
    const { edges } = layout(tasks);
    expect(edges).toEqual([{ id: "tsk_a->tsk_b", from: "tsk_a", to: "tsk_b", inCycle: false }]);
  });

  it("marks edges that participate in a returned cycle", () => {
    const tasks = [
      task({ id: "tsk_a", title: "a", dependencies: ["tsk_b"] }),
      task({ id: "tsk_b", title: "b", dependencies: ["tsk_a"] }),
    ];
    const { edges } = layout(tasks, [["tsk_a", "tsk_b"]]);
    expect(edges.every((e) => e.inCycle)).toBe(true);
  });

  it("computes a bounding extent covering all node geometry", () => {
    const tasks = [task({ id: "tsk_a", title: "a" }), task({ id: "tsk_b", title: "b" })];
    const { nodes, extent } = layout(tasks);
    for (const n of nodes) {
      expect(n.x - n.width / 2).toBeGreaterThanOrEqual(extent.minX);
      expect(n.x + n.width / 2).toBeLessThanOrEqual(extent.maxX);
      expect(n.y - n.height / 2).toBeGreaterThanOrEqual(extent.minY);
      expect(n.y + n.height / 2).toBeLessThanOrEqual(extent.maxY);
    }
  });
});
