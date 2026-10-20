import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { TaskGraphView, type TaskGraphViewProps } from "./TaskGraphView";
import { FakeRouterClient } from "../../router/fixtures";
import { installXyflowMeasurements } from "./xyflowTestEnv";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";

let uninstall: () => void;
beforeAll(() => {
  uninstall = installXyflowMeasurements();
});
afterAll(() => uninstall());
beforeEach(() => localStorage.clear());

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

const STAGES: RunStage[] = [
  { name: "architect", kind: "agent" },
  { name: "executor", kind: "agent" },
  { name: "build", kind: "command" },
];

function edgeClasses(container: HTMLElement, id: string): string {
  return container.querySelector(`.react-flow__edge[data-id="${id}"]`)?.getAttribute("class") ?? "";
}

function renderView(tasks: Task[], props: Partial<TaskGraphViewProps> = {}) {
  const tasksById = Object.fromEntries(tasks.map((t) => [t.id, t]));
  return render(
    <TaskGraphView
      sessionId="session-run"
      routerClient={new FakeRouterClient()}
      tasks={tasks}
      tasksById={tasksById}
      stages={STAGES}
      {...props}
    />,
  );
}

describe("TaskGraphView — status filter strip", () => {
  const tasks = [task({ id: "a", title: "A" }), task({ id: "b", title: "B", status: "done", dependencies: ["a"] })];
  const strip = () => screen.queryByRole("button", { name: /ready$/ });

  it("shows in Plan", () => {
    renderView(tasks, { mode: "plan" });
    expect(strip()).not.toBeNull();
  });

  it.each(["live", "past"] as const)("is hidden for a %s run", (mode) => {
    renderView(tasks, { mode });
    expect(strip()).toBeNull();
  });
});

describe("TaskGraphView — Plan", () => {
  // a -> b -> c is the longest chain; x is left out, so y (behind it) won't run.
  const a = task({ id: "a", title: "A" });
  const b = task({ id: "b", title: "B", dependencies: ["a"] });
  const c = task({ id: "c", title: "C", dependencies: ["b"] });
  const x = task({ id: "x", title: "X" });
  const y = task({ id: "y", title: "Y", dependencies: ["x"] });
  const tasks = [a, b, c, x, y];

  it("draws the critical path in its own edge class, with a legend counting its waves", () => {
    const { container } = renderView(tasks, { mode: "plan", excluded: new Set(["x"]) });
    expect(edgeClasses(container, "a->b")).toMatch(/edgeCritical/);
    expect(edgeClasses(container, "b->c")).toMatch(/edgeCritical/);
    expect(screen.getByText("critical path (3 waves)")).toBeTruthy();
  });

  it("draws an edge out of a left-out task as cut", () => {
    const { container } = renderView(tasks, { mode: "plan", excluded: new Set(["x"]) });
    expect(edgeClasses(container, "x->y")).toMatch(/edgeCut/);
    expect(edgeClasses(container, "x->y")).not.toMatch(/edgeCritical/);
  });

  it("shows wave badges and include boxes, and clicking a node selects it rather than focusing", () => {
    const onSelectTask = vi.fn();
    const onFocusTask = vi.fn();
    const onToggleInclude = vi.fn();
    const { container } = renderView(tasks, { mode: "plan", onSelectTask, onFocusTask, onToggleInclude });
    expect(screen.getByText("wave 3")).toBeTruthy();
    fireEvent.click(screen.getByRole("checkbox", { name: "Include X in this run" }));
    expect(onToggleInclude).toHaveBeenCalledWith("x");
    expect(onSelectTask).not.toHaveBeenCalled();
    fireEvent.click(container.querySelector('.react-flow__node[data-id="a"]')!);
    expect(onSelectTask).toHaveBeenCalledWith(a);
    expect(onFocusTask).not.toHaveBeenCalled();
  });
});

describe("TaskGraphView — live run", () => {
  const done = task({ id: "d", title: "Done one", status: "done" });
  const running = task({ id: "r", title: "Rotate tokens", status: "in_progress", dependencies: ["d"] });
  const next = task({ id: "n", title: "Revoke family", dependencies: ["r"] });
  const tasks = [done, running, next];
  const taskRunState: Record<string, TaskRunState> = {
    r: {
      taskId: "r",
      currentStage: "executor",
      attempts: [
        { stage: "architect", attempt: 1, status: "completed", startedSeq: 1 },
        { stage: "executor", attempt: 1, status: "started", startedSeq: 2 },
      ],
      touchedFiles: [],
    },
  };

  it("animates the edge into a running task", () => {
    const { container } = renderView(tasks, { mode: "live", taskRunState });
    const hot = container.querySelector('.react-flow__edge[data-id="d->r"]');
    expect(hot?.getAttribute("class")).toMatch(/edgeHot/);
    expect(hot?.classList.contains("animated")).toBe(true);
    expect(edgeClasses(container, "r->n")).not.toMatch(/edgeHot/);
  });

  it("clicking a node focuses it", () => {
    const onFocusTask = vi.fn();
    const onSelectTask = vi.fn();
    const { container } = renderView(tasks, { mode: "live", taskRunState, onFocusTask, onSelectTask });
    fireEvent.click(container.querySelector('.react-flow__node[data-id="n"]')!);
    expect(onFocusTask).toHaveBeenCalledWith("n");
    expect(onSelectTask).not.toHaveBeenCalled();
  });

  it("switches between Overview rings and Cards from the zoom control", () => {
    const { container } = renderView(tasks, { mode: "live", taskRunState });
    expect(screen.getByText("⌘ scroll / pinch")).toBeTruthy();
    expect(container.querySelector(".react-flow__minimap")).toBeTruthy();
    expect(container.querySelector("svg path[data-state]")).toBeNull();
    fireEvent.click(screen.getByRole("radio", { name: "Overview" }));
    expect(container.querySelectorAll("svg path[data-state]").length).toBe(tasks.length * STAGES.length);
    fireEvent.click(screen.getByRole("radio", { name: "Cards" }));
    expect(container.querySelector("svg path[data-state]")).toBeNull();
  });

  it("Focus on the control zooms into the running task", () => {
    const onFocusTask = vi.fn();
    renderView(tasks, { mode: "live", taskRunState, onFocusTask });
    fireEvent.click(screen.getByRole("radio", { name: "Focus" }));
    expect(onFocusTask).toHaveBeenCalledWith("r");
  });

  describe("focus", () => {
    function Focused({ onFocusTask }: { onFocusTask: (id: string | undefined) => void }) {
      const [focus, setFocus] = useState<string | undefined>("r");
      return (
        <TaskGraphView
          sessionId="session-run"
          routerClient={new FakeRouterClient()}
          tasks={tasks}
          tasksById={Object.fromEntries(tasks.map((t) => [t.id, t]))}
          stages={STAGES}
          mode="live"
          taskRunState={taskRunState}
          runLabel="Run 4"
          focusTaskId={focus}
          onFocusTask={(id) => {
            onFocusTask(id);
            setFocus(id);
          }}
          renderFocus={(id) => <div data-testid="lens">lens for {id}</div>}
        />
      );
    }

    it("renders the lens beside the graph with a breadcrumb, and rings the focused node", () => {
      const { container } = render(<Focused onFocusTask={vi.fn()} />);
      expect(screen.getByTestId("lens").textContent).toBe("lens for r");
      const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
      expect(within(crumbs).getByRole("button", { name: "Run 4" })).toBeTruthy();
      expect(within(crumbs).getByText("Rotate tokens")).toBeTruthy();
      expect(within(crumbs).getByText("executor")).toBeTruthy();
      expect(crumbs.textContent).toContain("Esc back to graph");
      // Nodes draw at Overview level; the focused one is ringed.
      expect(container.querySelectorAll("svg path[data-state]").length).toBeGreaterThan(0);
      expect(container.querySelector('.react-flow__node[data-id="r"] svg[class*="focused"]')).toBeTruthy();
      expect(container.querySelector('.react-flow__node[data-id="n"] svg[class*="focused"]')).toBeNull();
      expect(screen.getByRole("radio", { name: "Focus" }).getAttribute("aria-checked")).toBe("true");
      // The column is narrow: no minimap over the nodes, no gesture hint.
      expect(container.querySelector(".react-flow__minimap")).toBeNull();
      expect(screen.queryByText("⌘ scroll / pinch")).toBeNull();
    });

    it("lists upstream and unblocked tasks as chips that refocus", () => {
      const onFocusTask = vi.fn();
      render(<Focused onFocusTask={onFocusTask} />);
      fireEvent.click(screen.getByRole("button", { name: "Revoke family" }));
      expect(onFocusTask).toHaveBeenLastCalledWith("n");
      expect(screen.getByTestId("lens").textContent).toBe("lens for n");
      expect(screen.getByRole("button", { name: "Rotate tokens" })).toBeTruthy(); // now upstream
    });

    it("Esc leaves focus", () => {
      const onFocusTask = vi.fn();
      render(<Focused onFocusTask={onFocusTask} />);
      fireEvent.keyDown(document, { key: "Escape" });
      expect(onFocusTask).toHaveBeenCalledWith(undefined);
      expect(screen.queryByTestId("lens")).toBeNull();
      expect(screen.queryByRole("navigation", { name: "Breadcrumb" })).toBeNull();
    });

    it("the Run crumb and the Cards level leave focus too", () => {
      const onFocusTask = vi.fn();
      const { unmount } = render(<Focused onFocusTask={onFocusTask} />);
      fireEvent.click(screen.getByRole("button", { name: "Run 4" }));
      expect(onFocusTask).toHaveBeenCalledWith(undefined);
      unmount();

      const again = vi.fn();
      const { container } = render(<Focused onFocusTask={again} />);
      fireEvent.click(screen.getByRole("radio", { name: "Cards" }));
      expect(again).toHaveBeenCalledWith(undefined);
      expect(container.querySelector("svg path[data-state]")).toBeNull();
    });
  });
});

describe("TaskGraphView — the tray in a run", () => {
  // n has no edges, so it waits in the tray; a -> b is on the canvas.
  const a = task({ id: "a", title: "A", status: "done" });
  const b = task({ id: "b", title: "B", status: "done", dependencies: ["a"] });
  const notes = task({ id: "n", title: "Release notes", draft: true });
  const tasks = [a, b, notes];

  function Tray({ mode, onFocusTask }: { mode?: "plan" | "live" | "past"; onFocusTask: (id?: string) => void }) {
    const [focus, setFocus] = useState<string | undefined>();
    return (
      <TaskGraphView
        sessionId="session-tray"
        routerClient={new FakeRouterClient()}
        tasks={tasks}
        tasksById={Object.fromEntries(tasks.map((t) => [t.id, t]))}
        stages={mode ? STAGES : undefined}
        mode={mode}
        focusTaskId={focus}
        onFocusTask={(id) => {
          onFocusTask(id);
          setFocus(id);
        }}
        renderFocus={(id) => <div data-testid="lens">lens for {id}</div>}
      />
    );
  }

  it("a past run's tray speaks in run statuses: a draft is not in this run, and no slot is promised", () => {
    render(<Tray mode="past" onFocusTask={vi.fn()} />);
    expect(screen.getByText("No dependencies.")).toBeTruthy();
    expect(screen.queryByText("Runs when a slot opens · in this order.")).toBeNull();
    const card = screen.getByRole("button", { name: "Release notes: Not in this run" });
    expect(within(card).getByText("Not in this run")).toBeTruthy();
    expect(within(card).queryByText("Ready")).toBeNull();
  });

  it.each(["live", "past"] as const)("clicking a tray card in a %s run focuses it, and Esc closes the lens", (mode) => {
    const onFocusTask = vi.fn();
    render(<Tray mode={mode} onFocusTask={onFocusTask} />);
    fireEvent.click(screen.getByRole("button", { name: /^Release notes/ }));
    expect(onFocusTask).toHaveBeenLastCalledWith("n");
    expect(screen.getByTestId("lens").textContent).toBe("lens for n");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onFocusTask).toHaveBeenLastCalledWith(undefined);
    expect(screen.queryByTestId("lens")).toBeNull();
    expect(screen.getByRole("button", { name: /^Release notes/ })).toBeTruthy();
  });

  it("Enter on a focused tray card zooms in too", () => {
    const onFocusTask = vi.fn();
    render(<Tray mode="live" onFocusTask={onFocusTask} />);
    fireEvent.keyDown(screen.getByRole("button", { name: /^Release notes/ }), { key: "Enter" });
    expect(onFocusTask).toHaveBeenLastCalledWith("n");
  });

  it("Plan keeps the slot caption and doesn't focus from the tray", () => {
    render(<Tray mode="plan" onFocusTask={vi.fn()} />);
    expect(screen.getByText("Runs when a slot opens · in this order.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Release notes/ })).toBeNull();
    expect(screen.getByText("Release notes").closest("[draggable]")?.textContent).toContain("Not in this run");
  });

  it("with no run the tray is unchanged", () => {
    render(<Tray onFocusTask={vi.fn()} />);
    expect(screen.getByText("Runs when a slot opens · in this order.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Release notes/ })).toBeNull();
    expect(screen.getByText("Release notes").closest("[draggable]")?.textContent).not.toContain("Not in this run");
  });
});

describe("TaskGraphView — no run handed over", () => {
  it("draws the plain status cards with no zoom control", () => {
    const a = task({ id: "a" });
    const b = task({ id: "b", dependencies: ["a"] });
    renderView([a, b], { stages: undefined });
    expect(screen.queryByRole("radiogroup", { name: "Zoom level" })).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });
});
