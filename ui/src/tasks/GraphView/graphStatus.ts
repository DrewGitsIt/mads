import type { ChipTone } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import { isDependencyQueued, statusPresentation } from "../ListView/statusPresentation";

/**
 * GraphView-scoped bucketing of the real 6-value TaskStatus down to the 5
 * treatments the status-encoding mockup defines. needs_review/wont_do fold
 * into ready (crisp/neutral, default weight) — deliberate simplification,
 * not an oversight (see HANDOFF.md §0 at the time this was written).
 */
export type GraphStatus = "done" | "in_progress" | "ready" | "blocked" | "draft";

export const GRAPH_STATUS_ORDER: GraphStatus[] = ["done", "in_progress", "ready", "blocked", "draft"];

export const GRAPH_STATUS_CHIP_TONE: Record<GraphStatus, ChipTone> = {
  done: "ok",
  in_progress: "info",
  ready: "neutral",
  blocked: "bad",
  draft: "draft",
};

/**
 * Priority order matters: a task can be `draft: true` with any status.
 * done > in_progress > blocked > draft > ready.
 */
export function graphStatusOf(task: Task): GraphStatus {
  if (task.status === "done") return "done";
  if (task.status === "in_progress") return "in_progress";
  if (task.status === "blocked") return "blocked";
  if (task.draft) return "draft";
  return "ready";
}

/** Precise accessible text — reuses statusPresentation, not the 5-bucket label. */
export function graphStatusAccessibleLabel(task: Task, tasksById: Record<string, Task>): string {
  if (task.draft && task.status !== "done" && task.status !== "in_progress" && task.status !== "blocked") {
    return "Draft";
  }
  return statusPresentation(task, tasksById).label;
}

/**
 * Fallback chain, cheapest/most-specific first: (1) dependency-queued
 * synthesis (reused, not reimplemented), (2) last failed stage's reason
 * from run state, (3) most recent blocker note, (4) undefined — caller must
 * render the accessible label rather than a blank line. There is no backend
 * field today that gives a human-authored block reason outside of these
 * three sources.
 */
export function blockedReason(
  task: Task,
  tasksById: Record<string, Task>,
  taskRunState?: Record<string, TaskRunState>,
): string | undefined {
  if (task.status !== "blocked") return undefined;

  if (isDependencyQueued(task, tasksById)) {
    const n = statusPresentation(task, tasksById).depChips.length;
    return `waiting on ${n} ${n === 1 ? "dependency" : "dependencies"}`;
  }

  const attempts = taskRunState?.[task.id]?.attempts ?? [];
  const lastFailed = [...attempts].reverse().find((a) => a.status === "failed" && a.reason);
  if (lastFailed?.reason) return lastFailed.reason;

  const blockerNotes = (task.notes ?? []).filter((n) => n.type === "blocker");
  const lastNote = blockerNotes[blockerNotes.length - 1];
  if (lastNote) return lastNote.content;

  return undefined;
}
