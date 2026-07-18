import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@testing-library/react";
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

  it("gives a cycle edge a token-styled line class distinct from an ordinary edge", () => {
    const { container } = renderCanvas();
    const ordinary = container.querySelector('.react-flow__edge[data-id="tsk_a->tsk_b"]');
    const cycle = container.querySelector('.react-flow__edge[data-id="tsk_d->tsk_c"]');
    expect(Array.from(ordinary?.classList ?? []).some((c) => c.includes("edgeCycle"))).toBe(false);
    expect(Array.from(cycle?.classList ?? []).some((c) => c.includes("edgeCycle"))).toBe(true);
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

  describe("prefers-reduced-motion", () => {
    afterEach(() => {
      vi.unstubAllGlobals();
    });

    it("replaces the animated dash with a static edgeLiveStatic class and drops the xyflow animated class", () => {
      vi.stubGlobal("matchMedia", (query: string) => ({
        matches: true,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }));
      const { container } = renderCanvas();
      const g = container.querySelector('.react-flow__edge[data-id="tsk_a->tsk_b"]');
      expect(g?.classList.contains("animated")).toBe(false);
      expect(Array.from(g?.classList ?? []).some((c) => c.includes("edgeLiveStatic"))).toBe(true);
      expect(Array.from(g?.classList ?? []).some((c) => c.includes("edgeLive") && !c.includes("edgeLiveStatic"))).toBe(
        false,
      );
    });
  });
});

describe("GraphCanvas — highlight mode", () => {
  // Diamond: hi_a -> hi_b, hi_a -> hi_c, hi_b -> hi_d, hi_c -> hi_d.
  // Disjoint second component: hi_e -> hi_f, shares no ids with the diamond.
  const hi_a = task({ id: "hi_a", title: "a", created_at: "2026-02-01T00:00:00Z" });
  const hi_b = task({ id: "hi_b", title: "b", dependencies: ["hi_a"], created_at: "2026-02-01T00:01:00Z" });
  const hi_c = task({ id: "hi_c", title: "c", dependencies: ["hi_a"], created_at: "2026-02-01T00:02:00Z" });
  const hi_d = task({
    id: "hi_d",
    title: "d",
    dependencies: ["hi_b", "hi_c"],
    created_at: "2026-02-01T00:03:00Z",
  });
  const hi_e = task({ id: "hi_e", title: "e", created_at: "2026-02-01T00:04:00Z" });
  const hi_f = task({ id: "hi_f", title: "f", dependencies: ["hi_e"], created_at: "2026-02-01T00:05:00Z" });
  const highlightTasks = [hi_a, hi_b, hi_c, hi_d, hi_e, hi_f];
  const highlightTasksById = Object.fromEntries(highlightTasks.map((t) => [t.id, t]));

  function isDimmed(container: HTMLElement, taskId: string): boolean {
    const node = container.querySelector(`.react-flow__node[data-id="${taskId}"]`);
    return Array.from(node?.querySelectorAll('[class*="dimmed"]') ?? []).length > 0;
  }

  function renderHighlightCanvas() {
    const result = computeGraphLayout(highlightTasks, [], {}, new Map());
    return render(
      <GraphCanvas
        canvasNodes={result.canvasNodes}
        edges={result.edges}
        tasksById={highlightTasksById}
        unsequencedIds={new Set()}
      />,
    );
  }

  it("selecting a middle diamond node dims its sibling branch but not its ancestors/descendants", () => {
    const { container } = renderHighlightCanvas();
    fireEvent.click(container.querySelector('.react-flow__node[data-id="hi_b"]')!);

    expect(isDimmed(container, "hi_a")).toBe(false);
    expect(isDimmed(container, "hi_b")).toBe(false);
    expect(isDimmed(container, "hi_d")).toBe(false);
    expect(isDimmed(container, "hi_c")).toBe(true);
  });

  it("never dims a disjoint component's own nodes when it is the one selected, and dims everything else", () => {
    const { container } = renderHighlightCanvas();
    fireEvent.click(container.querySelector('.react-flow__node[data-id="hi_e"]')!);

    expect(isDimmed(container, "hi_e")).toBe(false);
    expect(isDimmed(container, "hi_f")).toBe(false);
    expect(isDimmed(container, "hi_a")).toBe(true);
    expect(isDimmed(container, "hi_b")).toBe(true);
    expect(isDimmed(container, "hi_c")).toBe(true);
    expect(isDimmed(container, "hi_d")).toBe(true);
  });

  it("clears dimming on Escape", () => {
    const { container } = renderHighlightCanvas();
    fireEvent.click(container.querySelector('.react-flow__node[data-id="hi_b"]')!);
    expect(isDimmed(container, "hi_c")).toBe(true);

    fireEvent.keyDown(document, { key: "Escape" });

    expect(isDimmed(container, "hi_c")).toBe(false);
  });

  it("clears dimming on a background (pane) click", () => {
    const { container } = renderHighlightCanvas();
    fireEvent.click(container.querySelector('.react-flow__node[data-id="hi_b"]')!);
    expect(isDimmed(container, "hi_c")).toBe(true);

    fireEvent.click(container.querySelector(".react-flow__pane")!);

    expect(isDimmed(container, "hi_c")).toBe(false);
  });
});
