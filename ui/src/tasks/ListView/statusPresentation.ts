import type { ChipTone } from "../../components/Chip/Chip";
import type { Task, TaskStatus } from "../../router/types";

/**
 * The Queued display rule (MADS-UX §3.3, ui/src/tasks/HANDOFF.md §2):
 * `contracts.Task` has no field distinguishing "blocked because a dependency
 * isn't done yet" (calm, expected) from "blocked and needs a human" (alarm).
 * Both share schema status "blocked" — this heuristic reconstructs the
 * distinction client-side. Implemented once here; TaskRow, TaskNode, and the
 * Context Rail's compact row all call it — don't reimplement the rule
 * anywhere else.
 */
export interface StatusPresentation {
  label: string;
  chipTone: ChipTone;
  dashedRing: boolean;
  /** IDs of the not-yet-done dependencies explaining a "Queued" block. */
  depChips: string[];
}

const STATUS_LABELS: Record<Exclude<TaskStatus, "blocked">, string> = {
  ready: "Ready",
  in_progress: "In progress",
  needs_review: "Needs review",
  done: "Done",
  wont_do: "Won't do",
};

/**
 * True when a "blocked" task's block is fully explained by an undone
 * dependency — the calm case. False means the block needs a human (red).
 */
export function isDependencyQueued(
  task: Task,
  tasksById: Record<string, Task>,
): boolean {
  if (task.status !== "blocked") return false;
  const deps = task.dependencies ?? [];
  if (deps.length === 0) return false;
  return deps.some((depId) => tasksById[depId]?.status !== "done");
}

export function statusPresentation(
  task: Task,
  tasksById: Record<string, Task>,
): StatusPresentation {
  if (task.status === "blocked") {
    if (isDependencyQueued(task, tasksById)) {
      const depChips = (task.dependencies ?? []).filter(
        (depId) => tasksById[depId]?.status !== "done",
      );
      return { label: "Queued", chipTone: "info", dashedRing: true, depChips };
    }
    return { label: "Needs human", chipTone: "bad", dashedRing: false, depChips: [] };
  }
  return {
    label: STATUS_LABELS[task.status],
    chipTone: task.status === "done" ? "ok" : task.status === "needs_review" ? "warn" : "neutral",
    dashedRing: false,
    depChips: [],
  };
}
