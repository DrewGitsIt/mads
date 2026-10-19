import { leftOutBy, waveOf } from "../../pipeline/plan/planScope";
import type { RunStage } from "../../pipeline/plan/runStages";
import { runStatusOf, type RunMode, type RunStatus } from "../../pipeline/plan/runStatus";
import type { StageAttempt, TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import { blockedReason } from "./graphStatus";
import type { GraphEdgeLayout } from "./layout";

// What the Graph view draws for a run (docs/TASKS-PIPELINE-DESIGN.md, the
// mockup's renderGraph): per-task stage states for the ring and track, the
// shared status line, plan badges, and edge treatments. Pure, so the node
// and canvas components only render what these return.

/** One stage's state for one task — a ring arc or a track segment. `off`: the task isn't in this run. */
export type StageState = "done" | "now" | "failed" | "skipped" | "waiting" | "off";

/** The shared vocabulary's words for each run status (Blocked is never "Needs you"). */
export const RUN_STATUS_LABEL: Record<RunStatus, string> = {
  ready: "Ready",
  queued: "Queued",
  running: "Running",
  blocked: "Blocked",
  done: "Done",
  skipped: "Not in this run",
  excluded: "Not in this run",
  draft: "Not in this run",
};

export interface StageCell {
  name: string;
  state: StageState;
}

export type StatusLineTone = "neutral" | "running" | "bad" | "warn";

export interface StatusLine {
  text: string;
  tone: StatusLineTone;
}

export interface NodeRunView {
  mode: RunMode;
  status: RunStatus;
  stages: StageCell[];
  /** Absent for a ready task in Plan, which shows its wave badge instead. */
  line?: StatusLine;
  /** Blocked in a live or past run: offer Edit & retry / Retry. */
  retryable: boolean;
  /** Plan only. */
  wave?: number;
  /** Plan only: stages a label-gated `when:` will skip for this task. */
  skips: string[];
  /** Plan only: the include box. Drafts can't be included. */
  include?: { checked: boolean; disabled: boolean };
}

export interface RunViewContext {
  mode: RunMode;
  tasksById: Record<string, Task>;
  excluded: ReadonlySet<string>;
  stages: RunStage[];
  taskRunState: Record<string, TaskRunState>;
}

// `when: '!contains(labels, "trivial")'` skips the stage for a task
// labelled trivial. Only this negated-label shape is recognized; anything
// else is assumed to run (the engine evaluates the real expression).
const NOT_LABEL = /!\s*contains\(\s*labels\s*,\s*["']([^"']+)["']\s*\)/g;

/** True when the stage's `when:` is a negated label check the task's labels trip. */
export function skipsByLabel(task: Task, stage: RunStage): boolean {
  if (!stage.when) return false;
  const labels = task.labels ?? [];
  for (const m of stage.when.matchAll(NOT_LABEL)) {
    if (labels.includes(m[1])) return true;
  }
  return false;
}

function latestAttempt(attempts: StageAttempt[], stage: string): StageAttempt | undefined {
  for (let i = attempts.length - 1; i >= 0; i--) if (attempts[i].stage === stage) return attempts[i];
  return undefined;
}

/**
 * Each stage's state for a task. In Plan nothing has run: a task that will
 * run is all `waiting` (or `skipped` where a label gate applies). In a live
 * or past run each stage shows its newest attempt; while a task is running,
 * a completed stage after the current one shows `waiting` again (a loop
 * back will run it once more), and a failure stays visible as the reason.
 */
export function stageStatesOf(
  task: Task,
  status: RunStatus,
  mode: RunMode,
  stages: RunStage[],
  runState: TaskRunState | undefined,
): StageCell[] {
  const off = status === "draft" || status === "excluded" || (mode === "plan" ? status !== "ready" : status === "skipped");
  if (off) return stages.map((s) => ({ name: s.name, state: "off" }));
  if (mode === "plan") {
    return stages.map((s) => ({ name: s.name, state: skipsByLabel(task, s) ? "skipped" : "waiting" }));
  }
  const attempts = runState?.attempts ?? [];
  const currentIndex = runState?.currentStage ? stages.findIndex((s) => s.name === runState.currentStage) : -1;
  return stages.map((s, i): StageCell => {
    const a = latestAttempt(attempts, s.name);
    let state: StageState;
    if (!a && status === "running" && i === currentIndex) {
      state = "now";
    } else if (!a) {
      // Done with no attempts at all finished before this run's record; done
      // with other attempts recorded means this stage didn't run.
      if (status === "done") state = attempts.length === 0 ? "done" : "skipped";
      else state = "waiting";
    } else if (a.status === "started") {
      state = status === "running" ? "now" : "waiting";
    } else if (a.status === "completed") {
      state = status === "running" && currentIndex !== -1 && i > currentIndex ? "waiting" : "done";
    } else if (a.status === "failed") {
      state = "failed";
    } else {
      state = "skipped";
    }
    return { name: s.name, state };
  });
}

/** The stage the run is on (or last touched) for a task — also the focus breadcrumb's last crumb. */
export function currentStageOf(runState: TaskRunState | undefined): string | undefined {
  return runState?.currentStage ?? runState?.attempts[runState.attempts.length - 1]?.stage;
}

function statusLineOf(task: Task, status: RunStatus, ctx: RunViewContext): StatusLine | undefined {
  const runState = ctx.taskRunState[task.id];
  if (ctx.mode === "plan") {
    if (status === "ready") return undefined;
    if (status === "draft") return { text: "draft: confirm it before it can run", tone: "neutral" };
    if (status === "excluded") return { text: "left out of this run", tone: "neutral" };
    const cut = leftOutBy(task, ctx.tasksById, ctx.excluded);
    if (cut) return { text: `won't run: depends on ${cut.title}`, tone: "warn" };
    return { text: "not in this run", tone: "neutral" };
  }
  switch (status) {
    case "running": {
      const stage = currentStageOf(runState);
      if (!stage) return { text: "running", tone: "running" };
      const attempt = runState ? latestAttempt(runState.attempts, stage)?.attempt ?? 1 : 1;
      return { text: attempt > 1 ? `${stage} · attempt ${attempt}` : stage, tone: "running" };
    }
    case "blocked": {
      const failed = runState ? [...runState.attempts].reverse().find((a) => a.status === "failed") : undefined;
      const stage = failed?.stage ?? runState?.currentStage;
      const reason = blockedReason(task, ctx.tasksById, ctx.taskRunState);
      const head = stage ? `Blocked at ${stage}` : "Blocked";
      return { text: reason ? `${head}: ${reason}` : head, tone: "bad" };
    }
    case "queued": {
      const dep = (task.dependencies ?? []).map((id) => ctx.tasksById[id]).find((d) => d && d.status !== "done");
      return { text: dep ? `waits on ${dep.title}` : "queued", tone: "neutral" };
    }
    case "done":
      return { text: "✓ done", tone: "neutral" };
    default:
      return { text: "not in this run", tone: "neutral" };
  }
}

/** Everything a node draws for a run, for every task at once (waves share one memo). */
export function nodeRunViews(tasks: Task[], ctx: RunViewContext): Record<string, NodeRunView> {
  const waves = new Map<string, number>();
  const out: Record<string, NodeRunView> = {};
  for (const task of tasks) {
    const status = runStatusOf(task, ctx);
    const plan = ctx.mode === "plan";
    out[task.id] = {
      mode: ctx.mode,
      status,
      stages: stageStatesOf(task, status, ctx.mode, ctx.stages, ctx.taskRunState[task.id]),
      line: statusLineOf(task, status, ctx),
      retryable: !plan && status === "blocked",
      wave: plan && status === "ready" ? waveOf(task, ctx.tasksById, waves) : undefined,
      skips: plan && status === "ready" ? ctx.stages.filter((s) => skipsByLabel(task, s)).map((s) => s.name) : [],
      include: plan ? { checked: !task.draft && !ctx.excluded.has(task.id), disabled: !!task.draft } : undefined,
    };
  }
  return out;
}

export type EdgeRunKind = "critical" | "cut" | "hot" | "done";

/** Edge ids (`dep->task`) along the plan's critical path. */
export function criticalEdgeIds(path: string[]): Set<string> {
  const ids = new Set<string>();
  for (let i = 1; i < path.length; i++) ids.add(`${path[i - 1]}->${path[i]}`);
  return ids;
}

const CANT_FEED: ReadonlySet<RunStatus> = new Set(["excluded", "draft", "skipped", "blocked"]);

/**
 * How an edge reads for a run. Plan: the critical path, or a cut — an edge
 * into a task that won't run, from a dependency that can't finish. Live and
 * past: hot into a running task; done along a finished chain.
 */
export function edgeRunKindOf(
  edge: GraphEdgeLayout,
  mode: RunMode,
  views: Record<string, NodeRunView>,
  critical: ReadonlySet<string>,
): EdgeRunKind | undefined {
  const from = views[edge.from]?.status;
  const to = views[edge.to]?.status;
  if (!from || !to) return undefined;
  if (mode === "plan") {
    if (critical.has(edge.id)) return "critical";
    if (to === "skipped" && CANT_FEED.has(from)) return "cut";
    return undefined;
  }
  if (to === "running") return "hot";
  if (from === "done" && to === "done") return "done";
  return undefined;
}

/** The focus view's neighbour chips: direct dependencies and direct dependents. */
export function neighboursOf(taskId: string, tasks: Task[], tasksById: Record<string, Task>) {
  const upstream = (tasksById[taskId]?.dependencies ?? []).map((id) => tasksById[id]).filter((t): t is Task => !!t);
  const unblocks = tasks.filter((t) => t.dependencies?.includes(taskId));
  return { upstream, unblocks };
}
