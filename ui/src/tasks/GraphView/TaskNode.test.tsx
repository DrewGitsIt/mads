import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import { TaskNode, type TaskNodeType } from "./TaskNode";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
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

function renderNode(
  t: Task,
  {
    unsequenced = false,
    inCycle = false,
    dimmed = false,
    reducedMotion = false,
    taskRunState,
    columns,
    tasksById,
  }: {
    unsequenced?: boolean;
    inCycle?: boolean;
    dimmed?: boolean;
    reducedMotion?: boolean;
    taskRunState?: TaskRunState;
    columns?: string[];
    tasksById?: Record<string, Task>;
  } = {},
) {
  const node: TaskNodeType = {
    id: t.id,
    type: "taskNode",
    position: { x: 0, y: 0 },
    data: {
      task: t,
      tasksById: tasksById ?? { [t.id]: t },
      taskRunState,
      columns,
      reducedMotion,
      unsequenced,
      inCycle,
      dimmed,
    },
  };
  return render(
    <ReactFlowProvider>
      <div style={{ width: 800, height: 600 }}>
        <ReactFlow nodes={[node]} edges={[]} nodeTypes={{ taskNode: TaskNode }} fitView={false} />
      </div>
    </ReactFlowProvider>,
  );
}

function glyphOf(container: HTMLElement): Element | null {
  return container.querySelector('[aria-hidden="true"]');
}

function glyphHasVariant(container: HTMLElement, variant: string): boolean {
  const glyph = glyphOf(container);
  return glyph !== null && new RegExp(`_${variant}_`).test(glyph.className);
}

describe("TaskNode", () => {
  it("renders the full title in the DOM with a structural clamp class and a full-text title attribute", () => {
    const longTitle = "A very long task title that would overflow a two line card ".repeat(3).trim();
    renderNode(task({ title: longTitle }));
    const titleEl = screen.getByText(longTitle);
    expect(titleEl.className).toMatch(/title/);
    expect(titleEl.getAttribute("title")).toBe(longTitle);
  });

  it("omits the unsequenced marker when data.unsequenced is false", () => {
    renderNode(task({}), { unsequenced: false });
    expect(screen.queryByText("unsequenced")).toBeNull();
  });

  it("shows the unsequenced marker when data.unsequenced is true", () => {
    renderNode(task({}), { unsequenced: true });
    expect(screen.getByText("unsequenced")).toBeTruthy();
  });

  it("has no cycle-ring class when data.inCycle is false", () => {
    const { container } = renderNode(task({}), { inCycle: false });
    expect(container.querySelector('[class*="inCycle"]')).toBeNull();
  });

  it("gets a cycle-ring class when data.inCycle is true", () => {
    const { container } = renderNode(task({}), { inCycle: true });
    expect(container.querySelector('[class*="inCycle"]')).toBeTruthy();
  });

  it("has no dimmed class when data.dimmed is false", () => {
    const { container } = renderNode(task({}), { dimmed: false });
    expect(container.querySelector('[class*="dimmed"]')).toBeNull();
  });

  it("gets a dimmed class when data.dimmed is true", () => {
    const { container } = renderNode(task({}), { dimmed: true });
    expect(container.querySelector('[class*="dimmed"]')).toBeTruthy();
  });

  it("omits the retry indicator when retry_count is 0", () => {
    renderNode(task({ retry_count: 0 }));
    expect(screen.queryByText(/retry/)).toBeNull();
  });

  it("shows a retry indicator when retry_count > 0", () => {
    renderNode(task({ retry_count: 2 }));
    expect(screen.getByText("retry ×2")).toBeTruthy();
  });

  it("renders a target and a source handle with visually-distinct classes", () => {
    const { container } = renderNode(task({}));
    const handles = Array.from(container.querySelectorAll(".react-flow__handle"));
    expect(handles).toHaveLength(2);
    const classNames = handles.map((h) => h.className);
    expect(classNames.some((c) => c.includes("handleIn"))).toBe(true);
    expect(classNames.some((c) => c.includes("handleOut"))).toBe(true);
    expect(classNames[0]).not.toBe(classNames[1]);
  });

  describe("status treatments — no per-card tag, text status preserved for accessibility", () => {
    it("done: receded card class + filled-disc glyph, aria-label carries the text status", () => {
      const t = task({ status: "done" });
      const { container } = renderNode(t);
      expect(container.querySelector('[class*="status_done"]')).toBeTruthy();
      expect(glyphHasVariant(container, "done")).toBe(true);
      expect(screen.getByLabelText(/a task — done/i)).toBeTruthy();
    });

    it("in_progress: loud card class + spinning-arc glyph, aria-label carries the text status", () => {
      const t = task({ status: "in_progress" });
      const { container } = renderNode(t);
      expect(container.querySelector('[class*="status_in_progress"]')).toBeTruthy();
      expect(glyphHasVariant(container, "in_progress")).toBe(true);
      expect(screen.getByLabelText(/a task — in progress/i)).toBeTruthy();
    });

    it("ready: hollow-ring glyph, aria-label carries the text status", () => {
      const t = task({ status: "ready" });
      const { container } = renderNode(t);
      expect(glyphHasVariant(container, "ready")).toBe(true);
      expect(screen.getByLabelText(/a task — ready/i)).toBeTruthy();
    });

    it("blocked (needs human): danger card class + stop-octagon glyph, aria-label includes status and no reason", () => {
      const t = task({ status: "blocked" });
      const { container } = renderNode(t);
      expect(container.querySelector('[class*="status_blocked"]')).toBeTruthy();
      expect(glyphHasVariant(container, "blocked")).toBe(true);
      expect(screen.getByLabelText(/a task — needs human/i)).toBeTruthy();
    });

    it("blocked (queued on a dependency): the block reason renders inline on the card and in the aria-label", () => {
      const dep = task({ id: "tsk_dep", status: "ready" });
      const t = task({ status: "blocked", dependencies: [dep.id] });
      const byId = { [t.id]: t, [dep.id]: dep };
      renderNode(t, { tasksById: byId });
      expect(screen.getByText("waiting on 1 dependency")).toBeTruthy();
      expect(screen.getByLabelText(/a task — queued, waiting on 1 dependency/i)).toBeTruthy();
    });

    it("draft: ghost card class + dashed-ring glyph, aria-label reports Draft", () => {
      const t = task({ status: "ready", draft: true });
      const { container } = renderNode(t);
      expect(container.querySelector('[class*="status_draft"]')).toBeTruthy();
      expect(glyphHasVariant(container, "draft")).toBe(true);
      expect(screen.getByLabelText(/a task — draft/i)).toBeTruthy();
    });
  });

  describe("stage pips", () => {
    const columns = ["architect", "executor", "build", "code_review", "merge", "verify"];

    it("renders one pip per stage, marking past stages dimmed and the current stage lit and named", () => {
      const t = task({ status: "in_progress" });
      const runState: TaskRunState = { taskId: t.id, currentStage: "build", attempts: [], touchedFiles: [] };
      const { container } = renderNode(t, { taskRunState: runState, columns });

      const pips = container.querySelectorAll('[class*="_pip_"]');
      expect(pips).toHaveLength(columns.length);
      const past = container.querySelectorAll('[class*="pipPast"]');
      const now = container.querySelectorAll('[class*="pipNow"]');
      expect(past).toHaveLength(2); // architect, executor
      expect(now).toHaveLength(1);
      expect(screen.getByText("build")).toBeTruthy();
    });

    it("omits stage pips entirely for a non in_progress task", () => {
      const t = task({ status: "ready" });
      const { container } = renderNode(t, { columns });
      expect(container.querySelector('[class*="pips"]')).toBeNull();
    });

    it("omits stage pips when no run is live (columns absent)", () => {
      const t = task({ status: "in_progress" });
      const { container } = renderNode(t);
      expect(container.querySelector('[class*="pips"]')).toBeNull();
    });
  });

  describe("reduced motion", () => {
    it("the in_progress glyph does not carry the spin class when reducedMotion is true", () => {
      const t = task({ status: "in_progress" });
      const { container } = renderNode(t, { reducedMotion: true });
      expect(glyphHasVariant(container, "spin")).toBe(false);
    });

    it("the in_progress glyph carries the spin class when reducedMotion is false", () => {
      const t = task({ status: "in_progress" });
      const { container } = renderNode(t, { reducedMotion: false });
      expect(glyphHasVariant(container, "spin")).toBe(true);
    });
  });
});
