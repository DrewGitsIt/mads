import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { Task } from "../../router/types";
import { RunTaskList, type RunTaskListProps } from "./RunTaskList";
import { attempt, byId, runState, task } from "../Metro/testTasks";

const TASKS: Task[] = [
  task("dep", { title: "Extract TokenStore", status: "in_progress" }),
  task("q", { title: "Auth docs", dependencies: ["dep"] }),
  task("blk", { title: "Remove legacy JWT", status: "blocked" }),
  task("done", { title: "CSRF token", status: "done", labels: ["security"] }),
  task("draft", { title: "e2e login test", draft: true }),
  task("free", { title: "SameSite cookies" }),
];

function renderList(over: Partial<RunTaskListProps> = {}, tasks = TASKS) {
  const handlers = {
    onSelectTask: vi.fn(),
    onToggleInclude: vi.fn(),
    onConfirmDraft: vi.fn(),
    onRetry: vi.fn(),
    onEditTask: vi.fn(),
    onAddTask: vi.fn(),
  };
  const utils = render(
    <RunTaskList
      tasks={tasks}
      tasksById={byId(tasks)}
      mode="live"
      excluded={new Set()}
      taskRunState={{
        dep: runState("dep", [attempt("executor", 1, "completed"), attempt("executor", 2, "started")], "executor"),
        blk: runState("blk", [attempt("code_review", 1, "failed", "fold reason")], "code_review"),
      }}
      blockReasons={{ blk: "3 callers in admin/ still use VerifyLegacy" }}
      {...handlers}
      {...over}
    />,
  );
  return { ...utils, ...handlers };
}

const headings = () => screen.getAllByRole("heading").map((h) => h.textContent);
const row = (title: string) => screen.getByText(title).closest("li") as HTMLElement;

describe("RunTaskList (live)", () => {
  it("groups in run order with counts, hiding empty groups", () => {
    renderList();
    expect(headings()).toEqual(["Blocked1", "Running1", "Queued2", "Done1", "Not in this run1"]);
  });

  it("writes each row's sub-line from its status", () => {
    renderList();
    expect(within(row("Extract TokenStore")).getByText("executor · attempt 2")).toBeTruthy();
    expect(within(row("Remove legacy JWT")).getByText("3 callers in admin/ still use VerifyLegacy")).toBeTruthy();
    expect(within(row("Auth docs")).getByText("waits on Extract TokenStore")).toBeTruthy();
    expect(within(row("SameSite cookies")).getByText("queued")).toBeTruthy();
    expect(within(row("CSRF token")).getByText("security")).toBeTruthy();
    expect(within(row("e2e login test")).getByText("draft: confirm it before it can run")).toBeTruthy();
  });

  it("falls back to the failed attempt's reason for a blocked task", () => {
    renderList({ blockReasons: {} });
    expect(within(row("Remove legacy JWT")).getByText("fold reason")).toBeTruthy();
  });

  it("locks a running row and offers no edit on it", () => {
    renderList();
    const r = row("Extract TokenStore");
    expect(within(r).getByTitle("Locked while the pipeline works on it")).toBeTruthy();
    expect(within(r).queryByRole("button", { name: /Edit/ })).toBeNull();
    expect(within(r).getByText("Running")).toBeTruthy();
  });

  it("retries a blocked task without selecting the row", () => {
    const { onRetry, onSelectTask } = renderList();
    fireEvent.click(within(row("Remove legacy JWT")).getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledWith("blk");
    expect(onSelectTask).not.toHaveBeenCalled();
  });

  it("edits without selecting, and selects on a row click", () => {
    const { onEditTask, onSelectTask } = renderList();
    fireEvent.click(within(row("SameSite cookies")).getByRole("button", { name: "Edit SameSite cookies" }));
    expect(onEditTask).toHaveBeenCalledWith("free");
    expect(onSelectTask).not.toHaveBeenCalled();
    fireEvent.click(row("CSRF token"));
    expect(onSelectTask).toHaveBeenCalledWith("done");
    fireEvent.click(screen.getByRole("button", { name: "Auth docs" }));
    expect(onSelectTask).toHaveBeenLastCalledWith("q");
    expect(onSelectTask).toHaveBeenCalledTimes(2);
  });

  it("dims draft rows and shows no include boxes", () => {
    renderList();
    expect(row("e2e login test").className).toMatch(/dimmed/);
    expect(row("SameSite cookies").className).not.toMatch(/dimmed/);
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  });

  it("adds a task, with the live hint", () => {
    const { onAddTask } = renderList();
    fireEvent.click(screen.getByRole("button", { name: /Add task/ }));
    expect(onAddTask).toHaveBeenCalled();
    expect(screen.getByText("A new ready task is picked up by this run")).toBeTruthy();
  });

  it("drops the hint outside a live run", () => {
    renderList({ mode: "past" });
    expect(screen.getByRole("button", { name: /Add task/ })).toBeTruthy();
    expect(screen.queryByText("A new ready task is picked up by this run")).toBeNull();
  });
});

describe("RunTaskList (plan)", () => {
  const PLAN: Task[] = [
    task("a", { title: "TokenStore" }),
    task("b", { title: "Rotation", dependencies: ["a"] }),
    task("c", { title: "Cookies" }),
    task("d", { title: "Draft e2e", draft: true }),
  ];

  it("groups into Will run and Not in this run", () => {
    renderList({ mode: "plan", excluded: new Set(["a"]), taskRunState: {} }, PLAN);
    expect(headings()).toEqual(["Will run1", "Not in this run3"]);
  });

  it("ticks included tasks and toggles without selecting the row", () => {
    const { onToggleInclude, onSelectTask } = renderList({ mode: "plan", excluded: new Set(["a"]), taskRunState: {} }, PLAN);
    const boxA = screen.getByRole("checkbox", { name: "Include TokenStore" });
    const boxC = screen.getByRole("checkbox", { name: "Include Cookies" });
    expect(boxA.getAttribute("aria-checked")).toBe("false");
    expect(boxC.getAttribute("aria-checked")).toBe("true");
    fireEvent.click(boxA);
    expect(onToggleInclude).toHaveBeenCalledWith("a");
    expect(onSelectTask).not.toHaveBeenCalled();
  });

  it("explains left-out and downstream rows, and dims them", () => {
    renderList({ mode: "plan", excluded: new Set(["a"]), taskRunState: {} }, PLAN);
    expect(within(row("TokenStore")).getByText("left out of this run")).toBeTruthy();
    expect(within(row("Rotation")).getByText("won't run: depends on TokenStore, which is left out")).toBeTruthy();
    expect(row("Rotation").className).toMatch(/dimmed/);
    expect(row("Cookies").className).not.toMatch(/dimmed/);
  });

  it("gives drafts a disabled box and a Confirm button", () => {
    const { onConfirmDraft, onToggleInclude, onSelectTask } = renderList({ mode: "plan", taskRunState: {} }, PLAN);
    const box = screen.getByRole("checkbox", { name: "Include Draft e2e" });
    expect(box.getAttribute("aria-disabled")).toBe("true");
    expect(box.getAttribute("title")).toBe("Drafts can't run until confirmed");
    fireEvent.click(box);
    expect(onToggleInclude).not.toHaveBeenCalled();
    expect(onSelectTask).not.toHaveBeenCalled();
    fireEvent.click(within(row("Draft e2e")).getByRole("button", { name: "Confirm" }));
    expect(onConfirmDraft).toHaveBeenCalledWith("d");
    expect(onSelectTask).not.toHaveBeenCalled();
  });

  it("shows no status chips in Plan", () => {
    renderList({ mode: "plan", taskRunState: {} }, PLAN);
    expect(screen.queryByText("Ready")).toBeNull();
  });
});
