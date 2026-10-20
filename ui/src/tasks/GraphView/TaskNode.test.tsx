import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import { TaskNode, type GraphLevel, type TaskNodeType } from "./TaskNode";
import { GraphRunActionsContext, type GraphRunActions } from "./graphRunActions";
import { nodeRunViews, type RunViewContext } from "./runView";
import type { RunStage } from "../../pipeline/plan/runStages";
import { installXyflowMeasurements } from "./xyflowTestEnv";
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

  describe("stage track (outside a run view)", () => {
    const columns = ["architect", "executor", "build", "code_review", "merge", "verify"];

    it("renders one segment per stage, marking finished stages done and the current stage now and named", () => {
      const t = task({ status: "in_progress" });
      const runState: TaskRunState = {
        taskId: t.id,
        currentStage: "build",
        attempts: [
          { stage: "architect", attempt: 1, status: "completed", startedSeq: 1 },
          { stage: "executor", attempt: 1, status: "completed", startedSeq: 2 },
          { stage: "build", attempt: 1, status: "started", startedSeq: 3 },
        ],
        touchedFiles: [],
      };
      const { container } = renderNode(t, { taskRunState: runState, columns });

      expect(container.querySelectorAll("[data-state]")).toHaveLength(columns.length);
      expect(container.querySelectorAll('[data-state="done"]')).toHaveLength(2); // architect, executor
      expect(container.querySelectorAll('[data-state="now"]')).toHaveLength(1);
      expect(screen.getByText("build")).toBeTruthy();
    });

    it("omits the stage track entirely for a non in_progress task", () => {
      const t = task({ status: "ready" });
      const { container } = renderNode(t, { columns });
      expect(container.querySelector("[data-state]")).toBeNull();
    });

    it("omits the stage track when no run is observed (columns absent)", () => {
      const t = task({ status: "in_progress" });
      const { container } = renderNode(t);
      expect(container.querySelector("[data-state]")).toBeNull();
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

  describe("run view", () => {
    // Unmeasured xyflow nodes are hidden, which hides their controls from role queries.
    let uninstall: () => void;
    beforeAll(() => {
      uninstall = installXyflowMeasurements();
    });
    afterAll(() => uninstall());

    const stages: RunStage[] = [
      { name: "architect", kind: "agent", when: '!contains(labels, "trivial")' },
      { name: "executor", kind: "agent" },
      { name: "build", kind: "command" },
      { name: "code_review", kind: "agent" },
    ];

    function renderRun(
      t: Task,
      {
        all = [t],
        level = "cards",
        focused = false,
        actions = {},
        ...ctx
      }: Partial<RunViewContext> & { all?: Task[]; level?: GraphLevel; focused?: boolean; actions?: GraphRunActions } = {},
    ) {
      const tasksById = Object.fromEntries(all.map((x) => [x.id, x]));
      const views = nodeRunViews(all, {
        mode: "live",
        tasksById,
        excluded: new Set(),
        stages,
        taskRunState: {},
        ...ctx,
      });
      const node: TaskNodeType = {
        id: t.id,
        type: "taskNode",
        position: { x: 0, y: 0 },
        data: {
          task: t,
          tasksById,
          reducedMotion: true,
          unsequenced: false,
          inCycle: false,
          dimmed: false,
          run: views[t.id],
          level,
          focused,
        },
      };
      return render(
        <GraphRunActionsContext.Provider value={actions}>
          <ReactFlowProvider>
            <div style={{ width: 800, height: 600 }}>
              <ReactFlow nodes={[node]} edges={[]} nodeTypes={{ taskNode: TaskNode }} fitView={false} />
            </div>
          </ReactFlowProvider>
        </GraphRunActionsContext.Provider>,
      );
    }

    it("Overview draws one ring arc per stage, colored by that stage's state", () => {
      const t = task({ status: "in_progress" });
      const rs: TaskRunState = {
        taskId: t.id,
        currentStage: "build",
        attempts: [
          { stage: "architect", attempt: 1, status: "skipped", startedSeq: 1 },
          { stage: "executor", attempt: 1, status: "completed", startedSeq: 2 },
          { stage: "build", attempt: 1, status: "started", startedSeq: 3 },
        ],
        touchedFiles: [],
      };
      const { container } = renderRun(t, { level: "overview", taskRunState: { [t.id]: rs } });
      const arcs = Array.from(container.querySelectorAll("svg path[data-state]"));
      expect(arcs.map((a) => a.getAttribute("data-state"))).toEqual(["skipped", "done", "now", "waiting"]);
      expect(arcs[2].getAttribute("class")).toMatch(/seg_now/);
      expect(screen.getByText("a task")).toBeTruthy(); // the label under the ring
      expect(screen.getByLabelText("a task — Running")).toBeTruthy();
    });

    it("Overview shows a failed arc and a blocked core", () => {
      const t = task({ status: "blocked" });
      const rs: TaskRunState = {
        taskId: t.id,
        attempts: [{ stage: "architect", attempt: 1, status: "failed", reason: "x", startedSeq: 1 }],
        touchedFiles: [],
      };
      const { container } = renderRun(t, { level: "overview", taskRunState: { [t.id]: rs } });
      expect(container.querySelector('path[data-state="failed"]')).toBeTruthy();
      expect(container.querySelector('circle[class*="core_blocked"]')).toBeTruthy();
    });

    it("rings the focused node", () => {
      const { container } = renderRun(task({ status: "done" }), { level: "overview", focused: true });
      expect(container.querySelector('svg[class*="focused"]')).toBeTruthy();
    });

    it("Cards: a stage track and the running line", () => {
      const t = task({ status: "in_progress" });
      const rs: TaskRunState = {
        taskId: t.id,
        currentStage: "executor",
        attempts: [{ stage: "executor", attempt: 2, status: "started", startedSeq: 1 }],
        touchedFiles: [],
      };
      const { container } = renderRun(t, { taskRunState: { [t.id]: rs } });
      expect(container.querySelectorAll("span[data-state]")).toHaveLength(stages.length);
      expect(screen.getByText("executor · attempt 2")).toBeTruthy();
    });

    it("Cards: a blocked task offers Edit & retry and Retry, without counting as a node click", () => {
      const t = task({ status: "blocked" });
      const onEditRetry = vi.fn();
      const onRetry = vi.fn();
      renderRun(t, { actions: { onEditRetry, onRetry } });
      fireEvent.click(screen.getByRole("button", { name: "Edit & retry" }));
      fireEvent.click(screen.getByRole("button", { name: "Retry" }));
      expect(onEditRetry).toHaveBeenCalledWith(t.id);
      expect(onRetry).toHaveBeenCalledWith(t.id);
    });

    it("Plan: an include box, a wave badge and the stages a label skips", () => {
      const a = task({ id: "a", title: "first" });
      const b = task({ id: "b", title: "second", dependencies: ["a"], labels: ["trivial"] });
      const onToggleInclude = vi.fn();
      renderRun(b, { all: [a, b], mode: "plan", actions: { onToggleInclude } });
      expect(screen.getByText("wave 2 · skips architect")).toBeTruthy();
      const box = screen.getByRole("checkbox", { name: "Include second in this run" }) as HTMLInputElement;
      expect(box.checked).toBe(true);
      fireEvent.click(box);
      expect(onToggleInclude).toHaveBeenCalledWith("b");
    });

    it("Plan: a draft's include box is disabled and the card dimmed", () => {
      const t = task({ draft: true });
      const { container } = renderRun(t, { mode: "plan" });
      expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(true);
      expect(container.querySelector('[class*="run_draft"]')).toBeTruthy();
      expect(screen.getByText("draft: confirm it before it can run")).toBeTruthy();
    });

    it("Plan: a task behind a left-out dependency says so", () => {
      const a = task({ id: "a", title: "first" });
      const b = task({ id: "b", title: "second", dependencies: ["a"] });
      renderRun(b, { all: [a, b], mode: "plan", excluded: new Set(["a"]) });
      expect(screen.getByText("won't run: depends on first")).toBeTruthy();
      expect(screen.queryByText(/^wave/)).toBeNull();
    });
  });
});
