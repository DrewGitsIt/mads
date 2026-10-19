import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { Task } from "../../router/types";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import { MetroMap, type MetroMapProps } from "./MetroMap";
import { attempt, byId, DEFAULT_STAGES, runState, task } from "./testTasks";

function renderMetro(tasks: Task[], over: Partial<MetroMapProps> & { rs?: Record<string, TaskRunState> } = {}) {
  const onSelectTask = vi.fn();
  const { rs, ...rest } = over;
  const utils = render(
    <MetroMap
      tasks={tasks}
      tasksById={byId(tasks)}
      mode="live"
      excluded={new Set()}
      taskRunState={rs ?? {}}
      stages={DEFAULT_STAGES}
      onSelectTask={onSelectTask}
      {...rest}
    />,
  );
  return { ...utils, onSelectTask };
}

describe("MetroMap", () => {
  it("draws termini and a station per stage with its kind", () => {
    const { container } = renderMetro([task("a"), task("b", { status: "done" })]);
    const stations = [...container.querySelectorAll("[data-station]")].map((el) => el.getAttribute("data-station"));
    expect(stations).toEqual(["Queued", ...DEFAULT_STAGES.map((s) => s.name), "Done"]);
    const build = container.querySelector('[data-station="build"]')!;
    expect(within(build as HTMLElement).getByText("command")).toBeTruthy();
    const queued = container.querySelector('[data-station="Queued"]') as HTMLElement;
    expect(within(queued).getByText("1 task")).toBeTruthy();
  });

  it("calls Ready the start terminus in Plan", () => {
    const { container } = renderMetro([task("a")], { mode: "plan" });
    expect(container.querySelector('[data-station="Ready"]')).toBeTruthy();
  });

  it("selects a task when its token is clicked; the token carries the full title", () => {
    const long = task("x", { title: "Add refresh-token rotation with reuse detection" });
    const { onSelectTask } = renderMetro([long]);
    const token = screen.getByTitle("Add refresh-token rotation with reuse detection");
    expect(token.textContent).toBe("Add refresh-token…");
    fireEvent.click(token);
    expect(onSelectTask).toHaveBeenCalledWith("x");
  });

  it("shows #N on a running token past its first attempt", () => {
    const r = task("r", { status: "in_progress", title: "Rotate" });
    const { container } = renderMetro([r], {
      rs: { r: runState("r", [attempt("executor", 1, "completed"), attempt("executor", 2, "started")], "executor") },
    });
    const stack = container.querySelector('[data-stack="stage:executor"]')!;
    expect(stack.textContent).toContain("#2");
  });

  it("puts blocked tokens under a Blocked caption", () => {
    const b = task("b", { status: "blocked", title: "Legacy" });
    const { container } = renderMetro([b], { rs: { b: runState("b", [attempt("code_review", 1, "failed")], "code_review") } });
    const stack = container.querySelector('[data-stack="blocked:code_review"]')!;
    expect(stack.parentElement!.textContent).toContain("Blocked");
    expect(within(stack as HTMLElement).getByText("Legacy")).toBeTruthy();
  });

  it("collapses a crowded stack to +N more, whose list filters and selects", () => {
    const tasks = Array.from({ length: 6 }, (_, i) => task(`t${i}`, { title: `Queued thing ${i}` }));
    tasks[4] = { ...tasks[4], title: "Rate limit login" };
    const { onSelectTask } = renderMetro(tasks);
    expect(screen.getAllByRole("button", { name: /Queued thing/ })).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "+4 more" }));
    const list = screen.getByRole("dialog");
    expect(within(list).getAllByRole("button")).toHaveLength(6);
    fireEvent.change(within(list).getByLabelText("Filter tasks"), { target: { value: "rate" } });
    const items = within(list).getAllByRole("button");
    expect(items).toHaveLength(1);
    fireEvent.click(items[0]);
    expect(onSelectTask).toHaveBeenCalledWith("t4");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("closes the +N list on Escape", () => {
    const tasks = Array.from({ length: 5 }, (_, i) => task(`t${i}`));
    renderMetro(tasks);
    fireEvent.click(screen.getByRole("button", { name: "+3 more" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("rings the focused token and lights its path", () => {
    const f = task("f", { status: "in_progress", title: "Focus me" });
    const { container } = renderMetro([f, task("g")], {
      focusTaskId: "f",
      rs: { f: runState("f", [attempt("executor", 1, "completed"), attempt("build", 1, "started")], "build") },
    });
    expect(screen.getByTitle("Focus me").getAttribute("aria-pressed")).toBe("true");
    const cls = (name: string) => container.querySelector(`[data-station="${name}"]`)!.className;
    expect(cls("build")).toMatch(/pathNow/);
    expect(cls("executor")).toMatch(/path/);
    expect(cls("merge")).toMatch(/dim/);
  });

  it("draws a labelled return arc per loop-back", () => {
    const { container } = renderMetro([]);
    expect(container.querySelectorAll("svg path")).toHaveLength(2);
    expect(screen.getAllByText("↺ on fail ≤2")).toHaveLength(2);
  });

  it("lists not-in-this-run tasks in their own row", () => {
    renderMetro([task("d", { draft: true, title: "Draft one" })]);
    expect(screen.getByText("Not in this run")).toBeTruthy();
    expect(screen.getByText("Draft one")).toBeTruthy();
  });
});
