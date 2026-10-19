import type { PipelinePickupTask, Task } from "../../router/types";
import { isDependencyQueued } from "../../tasks/ListView/statusPresentation";
import type { TaskRunState } from "../state/usePipelineRun";
import { excludedUpstream, leftOutBy } from "./planScope";

/**
 * Where a task stands relative to the run the Tasks tab is showing — the one
 * vocabulary the run bar, metro, task list, graph and lens all share
 * (docs/TASKS-PIPELINE-DESIGN.md: Ready, Queued, Running, Interrupted,
 * Blocked, Done, Not in this run).
 *
 * - ready: Plan only — will run if started now.
 * - queued: in the live run, waiting its turn or on a dependency.
 * - running: the pipeline is working on it.
 * - interrupted: pipeline work started and nobody is doing it now (in
 *   progress, moved there by the pipeline, no live lease;
 *   docs/PIPELINE-LEASES-DESIGN.md). Plan: a run will pick it up where it
 *   stopped. Live: not picked up yet. Past: it was in flight when that run
 *   ended. A pipeline-only word: never a task's own status.
 * - blocked: needs a person (a dependency wait is queued, not blocked).
 * - done
 * - skipped: in the run's scope but it can't or didn't run — a dependency is
 *   left out or never finished, or the run ended before it started.
 * - excluded: left out by the user's include box.
 * - draft: unconfirmed; never runs.
 */
export type RunStatus =
  | "ready"
  | "queued"
  | "running"
  | "interrupted"
  | "blocked"
  | "done"
  | "skipped"
  | "excluded"
  | "draft";

export type RunMode = "plan" | "live" | "past";

/** The pickup endpoint's answer for the template a run would use, by task id. */
export interface PickupInfo {
  tasks: Record<string, PipelinePickupTask>;
  /** The session's runs share one directory: pickup is less isolated. */
  sharedDirectory: boolean;
}

export interface RunStatusContext {
  mode: RunMode;
  tasksById: Record<string, Task>;
  /** Plan: the include boxes left unticked. Live/past: the run's excluded_task_ids. */
  excluded: ReadonlySet<string>;
  /** Live/past: the shown run's fold. A task in it is one the run worked on. */
  taskRunState?: Record<string, TaskRunState>;
  /** Plan and live: the session's Interrupted tasks (GET .../pipeline/pickup). */
  interrupted?: ReadonlySet<string>;
}

/** The ids in a pickup answer, as the set RunStatusContext.interrupted takes. */
export function interruptedIds(pickup: PickupInfo | undefined): ReadonlySet<string> {
  return new Set(Object.keys(pickup?.tasks ?? {}));
}

function inProgressStatus(task: Task, ctx: RunStatusContext): RunStatus {
  const worked = !!ctx.taskRunState?.[task.id];
  const interrupted = !!ctx.interrupted?.has(task.id);
  switch (ctx.mode) {
    case "plan":
      // Interrupted: any new run picks it up. Otherwise it's someone's own
      // work in progress (a person's, or another run's): not this run's.
      if (!interrupted) return "skipped";
      return leftOutBy(task, ctx.tasksById, ctx.excluded, ctx.interrupted) ? "skipped" : "interrupted";
    case "live":
      // The run is on it, or will pick it up once it gets there.
      if (worked) return "running";
      return interrupted ? "interrupted" : "running";
    case "past":
      // In flight when that run ended; a run that never touched it has nothing to say.
      return worked ? "interrupted" : "skipped";
  }
}

export function runStatusOf(task: Task, ctx: RunStatusContext): RunStatus {
  if (task.draft) return "draft";
  if (ctx.excluded.has(task.id)) return "excluded";
  if (task.status === "done") return "done";
  if (task.status === "in_progress") return inProgressStatus(task, ctx);
  if (task.status === "blocked") {
    if (ctx.mode !== "plan" && isDependencyQueued(task, ctx.tasksById)) {
      if (ctx.mode === "past" || excludedUpstream(task, ctx.tasksById, ctx.excluded)) return "skipped";
      return "queued";
    }
    return "blocked";
  }
  if (task.status === "ready") {
    if (ctx.mode === "past") return "skipped";
    if (ctx.mode === "plan") return leftOutBy(task, ctx.tasksById, ctx.excluded, ctx.interrupted) ? "skipped" : "ready";
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

/**
 * List-view groups for a live or past run, in display order: what needs a
 * person first (Blocked), then the work in motion (Running, Queued), then
 * Interrupted — work that stopped and waits for a run, not for a person, so
 * it sits below the live work rather than beside Blocked — then the finished
 * and the out-of-scope.
 */
export const RUN_GROUPS: RunGroup[] = [
  { label: "Blocked", statuses: ["blocked"] },
  { label: "Running", statuses: ["running"] },
  { label: "Queued", statuses: ["queued"] },
  { label: "Interrupted", statuses: ["interrupted"] },
  { label: "Done", statuses: ["done"] },
  { label: "Not in this run", statuses: ["skipped", "excluded", "draft"] },
];

/** List-view groups in Plan. An Interrupted task will run: a run picks it up. */
export const PLAN_GROUPS: RunGroup[] = [
  { label: "Will run", statuses: ["interrupted", "ready"] },
  { label: "Not in this run", statuses: ["blocked", "skipped", "excluded", "draft"] },
];

/**
 * Plan: where a run picks an Interrupted task up, for its sub-line —
 * "Interrupted · resumes at code_review", or "Interrupted · restarts under
 * <template>, keeps its work"; "· shared directory" in current-directory
 * mode. Undefined for a task the pickup answer doesn't name.
 */
export function pickupLine(taskId: string, pickup: PickupInfo | undefined): string | undefined {
  const p = pickup?.tasks[taskId];
  if (!p) return undefined;
  const where =
    p.mode === "resume"
      ? p.from_stage
        ? `resumes at ${p.from_stage}`
        : "resumes"
      : `restarts under ${p.template_name}, keeps its work`;
  return `Interrupted · ${where}${pickup?.sharedDirectory ? " · shared directory" : ""}`;
}
