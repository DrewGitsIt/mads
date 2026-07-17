import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { GraphCanvas } from "./GraphCanvas";
import { computeGraphLayout } from "./layout";
import { installXyflowMeasurements } from "./xyflowTestEnv";
import type { Task } from "../../router/types";

let uninstall: () => void;
beforeAll(() => {
  uninstall = installXyflowMeasurements();
});
afterAll(() => {
  uninstall();
});

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

// tsk_a (in_progress) -> tsk_b: an ordinary, animated-by-source-status edge.
// tsk_d -> tsk_c: flagged as a cycle edge via the `cycles` arg below (a
// single edge is enough — `isCycleEdge` tags by cycle membership, it
// doesn't require the reverse edge to also be present), not animated.
const tsk_a = task({ id: "tsk_a", title: "a", status: "in_progress", created_at: "2026-01-01T00:00:00Z" });
const tsk_b = task({ id: "tsk_b", title: "b", dependencies: ["tsk_a"], created_at: "2026-01-01T00:01:00Z" });
const tsk_c = task({ id: "tsk_c", title: "c", dependencies: ["tsk_d"], created_at: "2026-01-01T00:02:00Z" });
const tsk_d = task({ id: "tsk_d", title: "d", created_at: "2026-01-01T00:03:00Z" });
const tasks = [tsk_a, tsk_b, tsk_c, tsk_d];
const tasksById = Object.fromEntries(tasks.map((t) => [t.id, t]));
const cycles = [["tsk_c", "tsk_d"]];

function renderCanvas() {
  const result = computeGraphLayout(tasks, cycles, {}, new Map());
  return render(
    <GraphCanvas
      canvasNodes={result.canvasNodes}
      edges={result.edges}
      tasksById={tasksById}
      unsequencedIds={new Set()}
    />,
  );
}

function edgeMarker(container: HTMLElement, edgeId: string): string | null {
  const g = container.querySelector(`.react-flow__edge[data-id="${edgeId}"]`);
  return g?.querySelector(".react-flow__edge-path")?.getAttribute("marker-end") ?? null;
}

describe("GraphCanvas", () => {
  it("renders one node per canvas node", () => {
    const { container } = renderCanvas();
    expect(container.querySelectorAll(".react-flow__node")).toHaveLength(4);
  });

  it("renders one edge per layout edge", () => {
    const { container } = renderCanvas();
    expect(container.querySelectorAll(".react-flow__edge")).toHaveLength(2);
  });

  it("gives an ordinary edge the CSS-styled default arrow marker (not xyflow's object markerEnd form)", () => {
    const { container } = renderCanvas();
    expect(edgeMarker(container, "tsk_a->tsk_b")).toBe("url('#mads-arrow')");
  });

  it("gives a cycle edge the cycle arrow marker variant", () => {
    const { container } = renderCanvas();
    expect(edgeMarker(container, "tsk_d->tsk_c")).toBe("url('#mads-arrow-cycle')");
  });

  it("animates an edge whose source task is in_progress", () => {
    const { container } = renderCanvas();
    const g = container.querySelector('.react-flow__edge[data-id="tsk_a->tsk_b"]');
    expect(g?.classList.contains("animated")).toBe(true);
  });

  it("does not animate an edge whose source task is not in_progress", () => {
    const { container } = renderCanvas();
    const g = container.querySelector('.react-flow__edge[data-id="tsk_d->tsk_c"]');
    expect(g?.classList.contains("animated")).toBe(false);
  });

  it("gives each node a target and a source handle with visually-distinct classes", () => {
    const { container } = renderCanvas();
    const node = container.querySelector('[data-id="tsk_b"]');
    const handles = Array.from(node?.querySelectorAll(".react-flow__handle") ?? []);
    expect(handles).toHaveLength(2);
    const classNames = handles.map((h) => h.className);
    expect(classNames.some((c) => c.includes("handleIn"))).toBe(true);
    expect(classNames.some((c) => c.includes("handleOut"))).toBe(true);
  });
});
