import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { DependencyPicker } from "./DependencyPicker";
import { fixtureTasks } from "../../router/fixtures";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));
const sessionTasks = fixtureTasks;

function byId(id: string) {
  return fixtureTasks.find((t) => t.id === id)!;
}

describe("DependencyPicker", () => {
  describe("Depends on", () => {
    it("renders only the task's real dependencies, with status glyph and summary line", () => {
      // tsk_queued depends only on tsk_dep_not_done — the wall of every
      // session task is gone, this is the one real edge.
      render(
        <DependencyPicker
          task={byId("tsk_queued")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={["tsk_dep_not_done"]}
          onChange={vi.fn()}
        />,
      );
      expect(screen.getByText("Design the retry policy")).toBeTruthy();
      expect(screen.queryByText("Cycle A")).toBeNull();
      expect(screen.getByText("1 · 1 ready")).toBeTruthy();
    });

    it("shows a status-bucketed summary line across multiple statuses", () => {
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={["tsk_done", "tsk_in_progress", "tsk_dep_not_done", "tsk_needs_human"]}
          onChange={vi.fn()}
        />,
      );
      expect(screen.getByText("4 · 1 done · 1 running · 1 ready · 1 blocked")).toBeTruthy();
    });

    it("renders the exact mockup empty-state copy when there are no dependencies", () => {
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={vi.fn()}
        />,
      );
      expect(screen.getByText("No dependencies")).toBeTruthy();
      expect(
        screen.getByText(
          "Runs as soon as a pipeline slot frees. Add one to sequence it behind another task.",
        ),
      ).toBeTruthy();
    });

    it("shows a blocked dependency's reason inline", () => {
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={["tsk_needs_human"]}
          onChange={vi.fn()}
        />,
      );
      expect(screen.getByText(/^blocked ·/)).toBeTruthy();
    });

    it("shows an in-progress dependency's current stage/attempt when live run state is present", () => {
      const taskRunState: Record<string, TaskRunState> = {
        tsk_in_progress: {
          taskId: "tsk_in_progress",
          currentStage: "Executor",
          attempts: [
            { stage: "Executor", attempt: 1, status: "started", startedSeq: 1 },
          ],
          touchedFiles: [],
        },
      };
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={["tsk_in_progress"]}
          onChange={vi.fn()}
          taskRunState={taskRunState}
        />,
      );
      expect(screen.getByText("in progress · Executor · attempt 1")).toBeTruthy();
    });

    it("removes a dependency when its hover × is clicked", () => {
      const onChange = vi.fn();
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={["tsk_done", "tsk_dep_not_done"]}
          onChange={onChange}
        />,
      );
      fireEvent.click(screen.getByLabelText("Remove dependency: Add retry metrics dashboard"));
      expect(onChange).toHaveBeenCalledWith(["tsk_dep_not_done"]);
    });

    it("clicking a dependency row calls onSelectTask with that task", () => {
      const onSelectTask = vi.fn();
      render(
        <DependencyPicker
          task={byId("tsk_queued")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={["tsk_dep_not_done"]}
          onChange={vi.fn()}
          onSelectTask={onSelectTask}
        />,
      );
      fireEvent.click(screen.getByText("Design the retry policy"));
      expect(onSelectTask).toHaveBeenCalledWith(byId("tsk_dep_not_done"));
    });
  });

  describe("Add-dependency picker", () => {
    it("opens on + Add dependency… and filters candidates by typed query", () => {
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByText("+ Add dependency…"));
      expect(screen.getByText("Add retry metrics dashboard")).toBeTruthy();

      fireEvent.change(screen.getByPlaceholderText("Search tasks…"), {
        target: { value: "retry policy" },
      });
      expect(screen.getByText("Design the retry policy")).toBeTruthy();
      expect(screen.queryByText("Add retry metrics dashboard")).toBeNull();
    });

    it("disables an already-a-dependency candidate with its badge", () => {
      render(
        <DependencyPicker
          task={byId("tsk_queued")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={["tsk_dep_not_done"]}
          onChange={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByText("+ Add dependency…"));
      const badge = screen.getByText("already a dependency");
      expect(badge.closest("div")?.textContent).toContain("Design the retry policy");
    });

    it("disables a would-create-a-cycle candidate with its badge", () => {
      // tsk_cycle_a already depends on tsk_cycle_b — picking tsk_cycle_a as a
      // dependency of tsk_cycle_b (which has no deps yet here) would close the loop.
      const editedTask = { ...byId("tsk_cycle_b"), dependencies: [] };
      render(
        <DependencyPicker
          task={editedTask}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByText("+ Add dependency…"));
      const badge = screen.getByText("would create a cycle");
      expect(badge.closest("div")?.textContent).toContain("Cycle A");
    });

    it("adding a candidate calls onChange with the id appended and keeps the picker open", () => {
      const onChange = vi.fn();
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={onChange}
        />,
      );
      fireEvent.click(screen.getByText("+ Add dependency…"));
      fireEvent.click(screen.getByText("Add retry metrics dashboard"));
      expect(onChange).toHaveBeenCalledWith(["tsk_done"]);
      expect(screen.getByPlaceholderText("Search tasks…")).toBeTruthy();
    });

    it("supports arrow-key navigation and Enter to add", () => {
      const onChange = vi.fn();
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={onChange}
        />,
      );
      fireEvent.click(screen.getByText("+ Add dependency…"));
      fireEvent.change(screen.getByPlaceholderText("Search tasks…"), {
        target: { value: "retry" },
      });
      const input = screen.getByPlaceholderText("Search tasks…");
      fireEvent.keyDown(input, { key: "ArrowDown" });
      fireEvent.keyDown(input, { key: "ArrowDown" });
      fireEvent.keyDown(input, { key: "Enter" });
      expect(onChange).toHaveBeenCalledOnce();
    });

    it("closes the picker on Escape", () => {
      render(
        <DependencyPicker
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByText("+ Add dependency…"));
      expect(screen.getByPlaceholderText("Search tasks…")).toBeTruthy();
      fireEvent.keyDown(document, { key: "Escape" });
      expect(screen.queryByPlaceholderText("Search tasks…")).toBeNull();
    });
  });

  describe("Unblocks", () => {
    it("lists tasks whose dependencies include this one, with click-through", () => {
      const onSelectTask = vi.fn();
      render(
        <DependencyPicker
          task={byId("tsk_dep_not_done")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={vi.fn()}
          onSelectTask={onSelectTask}
        />,
      );
      expect(screen.getByText("1 task waits on this one")).toBeTruthy();
      fireEvent.click(screen.getByText("Roll retry backoff out to the export worker"));
      expect(onSelectTask).toHaveBeenCalledWith(byId("tsk_queued"));
    });

    it("renders the honest empty state when nothing waits on this task", () => {
      render(
        <DependencyPicker
          task={byId("tsk_done")}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={[]}
          onChange={vi.fn()}
        />,
      );
      expect(screen.getByText("Nothing waits on this task.")).toBeTruthy();
    });
  });
});
