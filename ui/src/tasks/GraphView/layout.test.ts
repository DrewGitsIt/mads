import { describe, expect, it } from "vitest";
import {
  buildEdges,
  connectedComponents,
  computeGraphLayout,
  packComponents,
  NODE_WIDTH,
  NODE_HEIGHT,
  type ComponentLayout,
  type ComponentSlots,
} from "./layout";
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

const emptySlots: ComponentSlots = new Map();

describe("computeGraphLayout", () => {
  it("is deterministic across repeated calls on the same input", () => {
    const tasks = [
      task({ id: "tsk_b", title: "second" }),
      task({ id: "tsk_a", title: "first" }),
      task({ id: "tsk_c", title: "third", dependencies: ["tsk_a", "tsk_b"] }),
    ];
    const first = computeGraphLayout(tasks, [], {}, emptySlots);
    const second = computeGraphLayout([...tasks].reverse(), [], {}, emptySlots);
    expect(second.canvasNodes.map((n) => ({ id: n.id, x: n.x, y: n.y }))).toEqual(
      first.canvasNodes.map((n) => ({ id: n.id, x: n.x, y: n.y })),
    );
  });

  it("places dependency nodes at a shallower rank (smaller y) than dependents", () => {
    const tasks = [
      task({ id: "tsk_a", title: "dep" }),
      task({ id: "tsk_b", title: "dependent", dependencies: ["tsk_a"] }),
    ];
    const { canvasNodes } = computeGraphLayout(tasks, [], {}, emptySlots);
    const a = canvasNodes.find((n) => n.id === "tsk_a")!;
    const b = canvasNodes.find((n) => n.id === "tsk_b")!;
    expect(a.y).toBeLessThan(b.y);
  });

  it("produces one edge per valid dependency, skipping dangling refs", () => {
    const tasks = [
      task({ id: "tsk_a", title: "dep" }),
      task({ id: "tsk_b", title: "dependent", dependencies: ["tsk_a", "tsk_missing"] }),
    ];
    const { edges } = computeGraphLayout(tasks, [], {}, emptySlots);
    expect(edges).toEqual([{ id: "tsk_a->tsk_b", from: "tsk_a", to: "tsk_b", inCycle: false }]);
  });

  it("marks edges that participate in a returned cycle", () => {
    const tasks = [
      task({ id: "tsk_a", title: "a", dependencies: ["tsk_b"] }),
      task({ id: "tsk_b", title: "b", dependencies: ["tsk_a"] }),
    ];
    const { edges } = computeGraphLayout(tasks, [["tsk_a", "tsk_b"]], {}, emptySlots);
    expect(edges.every((e) => e.inCycle)).toBe(true);
  });

  it("computes a bounding extent covering all canvas node geometry", () => {
    const tasks = [
      task({ id: "tsk_a", title: "a", dependencies: ["tsk_b"] }),
      task({ id: "tsk_b", title: "b" }),
    ];
    const { canvasNodes, extent } = computeGraphLayout(tasks, [], {}, emptySlots);
    for (const n of canvasNodes) {
      expect(n.x - n.width / 2).toBeGreaterThanOrEqual(extent.minX);
      expect(n.x + n.width / 2).toBeLessThanOrEqual(extent.maxX);
      expect(n.y - n.height / 2).toBeGreaterThanOrEqual(extent.minY);
      expect(n.y + n.height / 2).toBeLessThanOrEqual(extent.maxY);
    }
  });

  describe("tray membership", () => {
    it("puts a 0-edge task with no manual position in the tray", () => {
      const tasks = [task({ id: "tsk_a" })];
      const { trayTaskIds, canvasNodes } = computeGraphLayout(tasks, [], {}, emptySlots);
      expect(trayTaskIds).toEqual(["tsk_a"]);
      expect(canvasNodes).toHaveLength(0);
    });

    it("puts a 0-edge task with a manual position on the canvas as a floating node", () => {
      const tasks = [task({ id: "tsk_a" })];
      const { trayTaskIds, canvasNodes } = computeGraphLayout(
        tasks,
        [],
        { tsk_a: { x: 100, y: 200 } },
        emptySlots,
      );
      expect(trayTaskIds).toEqual([]);
      expect(canvasNodes).toEqual([
        { id: "tsk_a", x: 100, y: 200, width: NODE_WIDTH, height: NODE_HEIGHT, task: tasks[0] },
      ]);
    });

    it("keeps a >=1-edge task on the canvas regardless of manual position", () => {
      const tasks = [task({ id: "tsk_a" }), task({ id: "tsk_b", dependencies: ["tsk_a"] })];

      const withoutManual = computeGraphLayout(tasks, [], {}, emptySlots);
      expect(withoutManual.trayTaskIds).toEqual([]);
      expect(withoutManual.canvasNodes.map((n) => n.id).sort()).toEqual(["tsk_a", "tsk_b"]);

      const withManual = computeGraphLayout(tasks, [], { tsk_a: { x: 5, y: 5 } }, emptySlots);
      expect(withManual.trayTaskIds).toEqual([]);
      const a = withManual.canvasNodes.find((n) => n.id === "tsk_a")!;
      expect(a.x).toBe(5);
      expect(a.y).toBe(5);
    });
  });
});

describe("buildEdges", () => {
  it("skips dangling dependency refs", () => {
    const tasks = [task({ id: "tsk_a" }), task({ id: "tsk_b", dependencies: ["tsk_a", "tsk_missing"] })];
    expect(buildEdges(tasks)).toEqual([{ id: "tsk_a->tsk_b", from: "tsk_a", to: "tsk_b", inCycle: false }]);
  });
});

describe("connectedComponents", () => {
  it("groups tasks correctly across disjoint edge sets, excluding degree-0 tasks", () => {
    const tasks = [
      task({ id: "tsk_a" }),
      task({ id: "tsk_b", dependencies: ["tsk_a"] }),
      task({ id: "tsk_c" }),
      task({ id: "tsk_d", dependencies: ["tsk_c"] }),
      task({ id: "tsk_isolated" }),
    ];
    const edges = buildEdges(tasks);
    const components = connectedComponents(
      tasks.map((t) => t.id),
      edges,
    );
    expect(components).toEqual([
      ["tsk_a", "tsk_b"],
      ["tsk_c", "tsk_d"],
    ]);
  });
});

describe("packComponents", () => {
  function fakeComponent(key: string, width: number, height: number): ComponentLayout {
    return { key, nodes: [], width, height };
  }

  it("reuses exact-key slots verbatim", () => {
    const previous: ComponentSlots = new Map([["tsk_a,tsk_b", { x: 300, y: 400 }]]);
    const slots = packComponents([fakeComponent("tsk_a,tsk_b", 220, 88)], previous);
    expect(slots.get("tsk_a,tsk_b")).toEqual({ x: 300, y: 400 });
  });

  it("assigns fresh, non-colliding slots to unmatched components", () => {
    const slots = packComponents(
      [fakeComponent("tsk_a,tsk_b", 220, 88), fakeComponent("tsk_c,tsk_d", 220, 88)],
      new Map(),
    );
    const first = slots.get("tsk_a,tsk_b")!;
    const second = slots.get("tsk_c,tsk_d")!;
    expect(first).not.toEqual(second);
    expect(second.x).toBeGreaterThanOrEqual(first.x + 220);
  });
});
