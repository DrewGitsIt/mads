import type { Task } from "../../router/types";
import { isDependencyQueued } from "../../tasks/ListView/statusPresentation";
import { excludedUpstream, leftOutBy } from "./planScope";

/**
 * Where a task stands relative to the run the Tasks tab is showing — the one
 * vocabulary the run bar, metro, task list, graph and lens all share
 * (docs/TASKS-PIPELINE-DESIGN.md: Ready, Queued, Running, Blocked, Done,
 * Not in this run).
 *
 * - ready: Plan only — will run if started now.
 * - queued: in the live run, waiting its turn or on a dependency.
 * - running: the pipeline is working on it.
 * - blocked: needs a person (a dependency wait is queued, not blocked).
 * - done
 * - skipped: in the run's scope but it can't or didn't run — a dependency is
 *   left out or never finished, or the run ended before it started.
 * - excluded: left out by the user's include box.
 * - draft: unconfirmed; never runs.
 */
export type RunStatus = "ready" | "queued" | "running" | "blocked" | "done" | "skipped" | "excluded" | "draft";

export type RunMode = "plan" | "live" | "past";

export interface RunStatusContext {
  mode: RunMode;
  tasksById: Record<string, Task>;
  /** Plan: the include boxes left unticked. Live/past: the run's excluded_task_ids. */
  excluded: ReadonlySet<string>;
}

export function runStatusOf(task: Task, ctx: RunStatusContext): RunStatus {
  if (task.draft) return "draft";
  if (ctx.excluded.has(task.id)) return "excluded";
  if (task.status === "done") return "done";
  // In Plan there is no live run, so in_progress is a leftover of an
  // interrupted one: it won't be picked up.
  if (task.status === "in_progress") return ctx.mode === "plan" ? "skipped" : "running";
  if (task.status === "blocked") {
    if (ctx.mode !== "plan" && isDependencyQueued(task, ctx.tasksById)) {
      if (ctx.mode === "past" || excludedUpstream(task, ctx.tasksById, ctx.excluded)) return "skipped";
      return "queued";
    }
    return "blocked";
  }
  if (task.status === "ready") {
    if (ctx.mode === "past") return "skipped";
    if (ctx.mode === "plan") return leftOutBy(task, ctx.tasksById, ctx.excluded) ? "skipped" : "ready";
    // Live: a dependency the run left out means it never runs (the engine
    // reports it skipped); any other unfinished dependency is a wait.
    return excludedUpstream(task, ctx.tasksById, ctx.excluded) ? "skipped" : "queued";
  }
  // needs_review / wont_do: outside any run's reach.
  return "skipped";
}

export interface RunGroup {
  label: string;
  statuses: RunStatus[];
}

/** List-view groups for a live or past run, in display order. */
export const RUN_GROUPS: RunGroup[] = [
  { label: "Blocked", statuses: ["blocked"] },
  { label: "Running", statuses: ["running"] },
  { label: "Queued", statuses: ["queued"] },
  { label: "Done", statuses: ["done"] },
  { label: "Not in this run", statuses: ["skipped", "excluded", "draft"] },
];

/** List-view groups in Plan. */
export const PLAN_GROUPS: RunGroup[] = [
  { label: "Will run", statuses: ["ready"] },
  { label: "Not in this run", statuses: ["blocked", "skipped", "excluded", "draft"] },
];
