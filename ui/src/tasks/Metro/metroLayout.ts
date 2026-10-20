import type { Task } from "../../router/types";
import type { RunStage } from "../../pipeline/plan/runStages";
import { interruptedIds, runStatusOf, type PickupInfo, type RunMode, type RunStatus } from "../../pipeline/plan/runStatus";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";

// Pure layout for the List view's metro line (docs/TASKS-PIPELINE-DESIGN.md,
// docs/mockups/tasks-pipeline.html): where each task's token sits, how a
// crowded stack collapses, and how loop-back arcs stack below the line.
// Horizontal positions are percentages of the metro's width; vertical ones
// are pixels from its top (the metro has a fixed, computed height).

/** A stack shows at most this many tokens; past it, STACK_MAX - 1 tokens and a "+N more" pill. */
export const STACK_MAX = 3;

// Vertical geometry (px). Tokens stack upward from just above the line;
// station name and kind sit under the ring; arcs start below those.
export const TOKEN_H = 20;
export const TOKEN_GAP = 3;
export const LINE_Y = 103;
/** Where loop-back arcs leave the line: below the station name and sub-label. */
export const ARC_TOP = 154;
const ARC_FIRST = 20; // first arc's bottom, below ARC_TOP
const ARC_LEVEL = 24; // each deeper arc; leaves room for the label riding the one above
const ARC_LABEL_H = 14;
const CAPTION_H = 14;
const ROW_GAP = 12;
const OUT_ROW_H = TOKEN_H + 4;

const START_X = 6;
const END_X = 95;
const FIRST_STATION_X = 17;
const LAST_STATION_X = 86;

export interface MetroToken {
  id: string;
  task: Task;
  status: RunStatus;
  /** Short label for the pill; the full title goes in its `title`. */
  label: string;
  /** The current stage's attempt (1-based), 0 when not running. */
  attempt: number;
}

export interface MetroStack {
  /** "start" | "done" | "out" | "stage:<name>" | "blocked:<name>" ("blocked:" when the stage is unknown). */
  key: string;
  /** Heading for the stack's full list, e.g. "executor", "Blocked at build". */
  heading: string;
  /** Centre, % of width. The "out" row is left-aligned at 0. */
  x: number;
  tokens: MetroToken[];
  /** Tokens drawn on the line; the rest are behind "+N more". */
  shown: MetroToken[];
  hidden: number;
}

/** How a station reads while a task is focused: its current stage, one it ran, or neither. */
export type StationFocus = "now" | "visited" | "dim";

export interface MetroStation {
  stage: RunStage;
  x: number;
  /** A task is running here. */
  hot: boolean;
  focus?: StationFocus;
}

export interface MetroArc {
  from: string;
  to: string;
  fromX: number;
  toX: number;
  /** 0 = shallowest. Arcs whose spans overlap (or share a station) get different levels. */
  level: number;
  /** y of the arc's horizontal run, px. */
  bottom: number;
  /** SVG path in a viewBox 1000 wide and `height` tall. */
  path: string;
  label: string;
  /** Label centre, % of width; it sits on the arc's horizontal run. */
  labelX: number;
}

export interface MetroGeometry {
  lineY: number;
  arcTop: number;
  blockedTop: number;
  outTop: number;
  height: number;
}

export interface MetroLayout {
  startX: number;
  endX: number;
  /** Max width of a station label or a station stack, % of width. */
  labelWidth: number;
  /** Max width of a terminus stack, % of width. */
  terminusWidth: number;
  startLabel: string;
  start: MetroStack;
  done: MetroStack;
  out: MetroStack;
  stations: MetroStation[];
  /** Running tokens above their stage (non-empty stacks only). */
  above: MetroStack[];
  /** Blocked tokens below the stage that blocked them (non-empty stacks only). */
  blocked: MetroStack[];
  arcs: MetroArc[];
  startFocus?: StationFocus;
  endFocus?: StationFocus;
  geometry: MetroGeometry;
}

export interface MetroInput {
  tasks: Task[];
  tasksById: Record<string, Task>;
  mode: RunMode;
  excluded: ReadonlySet<string>;
  taskRunState: Record<string, TaskRunState>;
  stages: RunStage[];
  focusTaskId?: string;
  /**
   * Plan and live: the session's Interrupted tasks and where a run picks
   * each up (the pickup endpoint's answer).
   */
  pickup?: PickupInfo;
}

/** A title cut to fit a token: whole words where it can, an ellipsis when it cuts. */
export function shortTitle(title: string, max = 18): string {
  const t = title.trim();
  if (t.length <= max) return t;
  const cut = t.slice(0, max);
  const space = cut.lastIndexOf(" ");
  const head = space >= Math.ceil(max / 2) ? cut.slice(0, space) : cut;
  return head.replace(/[\s,.;:–—-]+$/, "") + "…";
}

/** The attempt number of the task's current stage (1-based), or 0 with no current stage. */
export function currentAttempt(state: TaskRunState | undefined): number {
  if (!state?.currentStage) return 0;
  let n = 0;
  for (const a of state.attempts) if (a.stage === state.currentStage) n = Math.max(n, a.attempt);
  return n;
}

/** The stage of the task's last failed attempt: the one that blocked it. */
export function blockedAtStage(state: TaskRunState | undefined): string | undefined {
  const attempts = state?.attempts ?? [];
  for (let i = attempts.length - 1; i >= 0; i--) {
    if (attempts[i].status === "failed") return attempts[i].stage;
  }
  return undefined;
}

/** x (% of width) of each station, spread evenly between the termini. */
export function stationXs(count: number): number[] {
  if (count === 1) return [(FIRST_STATION_X + LAST_STATION_X) / 2];
  return Array.from(
    { length: count },
    (_, i) => FIRST_STATION_X + (i * (LAST_STATION_X - FIRST_STATION_X)) / (count - 1),
  );
}

function makeStack(key: string, heading: string, x: number, tokens: MetroToken[], focusId?: string): MetroStack {
  if (tokens.length <= STACK_MAX) return { key, heading, x, tokens, shown: tokens, hidden: 0 };
  let shown = tokens.slice(0, STACK_MAX - 1);
  // Keep the focused task visible even when its stack is collapsed.
  const focused = focusId ? tokens.find((t) => t.id === focusId) : undefined;
  if (focused && !shown.includes(focused)) shown = [...shown.slice(0, -1), focused];
  return { key, heading, x, tokens, shown, hidden: tokens.length - shown.length };
}

interface ArcSpan {
  from: number;
  to: number;
  lo: number;
  hi: number;
  maxLoops?: number;
}

/**
 * A level per loop-back, so overlapping arcs never share one: shortest spans
 * first, each on the shallowest level where no arc already there overlaps it
 * (sharing a station counts — two loops into the same target nest).
 */
export function arcLevels(spans: { lo: number; hi: number }[]): number[] {
  const order = spans.map((_, i) => i).sort((a, b) => spans[a].hi - spans[a].lo - (spans[b].hi - spans[b].lo) || spans[a].lo - spans[b].lo);
  const levels: number[] = new Array(spans.length);
  const placed: { lo: number; hi: number; level: number }[] = [];
  for (const i of order) {
    const { lo, hi } = spans[i];
    let level = 0;
    while (placed.some((p) => p.level === level && p.lo <= hi && lo <= p.hi)) level++;
    levels[i] = level;
    placed.push({ lo, hi, level });
  }
  return levels;
}

function buildArcs(stages: RunStage[], xs: number[]): MetroArc[] {
  const index = new Map(stages.map((s, i) => [s.name, i]));
  const spans: ArcSpan[] = [];
  stages.forEach((s, i) => {
    if (!s.loopBack) return;
    const j = index.get(s.loopBack);
    if (j === undefined || j === i) return;
    spans.push({ from: i, to: j, lo: Math.min(i, j), hi: Math.max(i, j), maxLoops: s.maxLoops });
  });
  const levels = arcLevels(spans);
  return spans.map((span, k) => {
    const level = levels[k];
    const bottom = ARC_TOP + ARC_FIRST + level * ARC_LEVEL;
    // A label rides its arc's horizontal run. Deeper arcs' verticals cross
    // that run where they leave or reach a station strictly inside it, so the
    // label takes the gap midpoint farthest from those.
    const blockers = spans
      .filter((o, m) => levels[m] > level)
      .flatMap((o) => [o.lo, o.hi])
      .filter((p) => p > span.lo && p < span.hi);
    const mid = (span.lo + span.hi) / 2;
    const candidates = [mid];
    for (let p = span.lo + 0.5; p < span.hi; p += 1) candidates.push(p);
    const clearance = (p: number) => (blockers.length ? Math.min(...blockers.map((b) => Math.abs(b - p))) : Infinity);
    let best = mid;
    for (const c of candidates) {
      const better = clearance(c) > clearance(best) || (clearance(c) === clearance(best) && Math.abs(c - mid) < Math.abs(best - mid));
      if (better) best = c;
    }
    const at = (p: number) => {
      const i = Math.floor(p);
      return i === p ? xs[i] : (xs[i] + xs[i + 1]) / 2;
    };
    const a = xs[span.from] * 10;
    const b = xs[span.to] * 10;
    const dir = b > a ? 1 : -1;
    const r = Math.min(8, Math.abs(b - a) / 2);
    const path =
      `M ${a} ${ARC_TOP} V ${bottom - r} Q ${a} ${bottom} ${a + dir * r} ${bottom} ` +
      `H ${b - dir * r} Q ${b} ${bottom} ${b} ${bottom - r} V ${ARC_TOP}`;
    return {
      from: stages[span.from].name,
      to: stages[span.to].name,
      fromX: xs[span.from],
      toX: xs[span.to],
      level,
      bottom,
      path,
      label: span.maxLoops !== undefined ? `↺ on fail ≤${span.maxLoops}` : "↺ on fail",
      labelX: at(best),
    };
  });
}

export function metroLayout(input: MetroInput): MetroLayout {
  const { tasks, tasksById, mode, excluded, taskRunState, stages, focusTaskId, pickup } = input;
  const interrupted = interruptedIds(pickup);
  const xs = stationXs(stages.length);
  const xOf = new Map(stages.map((s, i) => [s.name, xs[i]]));
  const ctx = { mode, tasksById, excluded, taskRunState, interrupted };

  const start: MetroToken[] = [];
  const done: MetroToken[] = [];
  const out: MetroToken[] = [];
  const at = new Map<string, MetroToken[]>();
  const below = new Map<string, MetroToken[]>(); // "" = blocked at no known stage

  for (const task of tasks) {
    const status = runStatusOf(task, ctx);
    const rs = taskRunState[task.id];
    const token: MetroToken = {
      id: task.id,
      task,
      status,
      label: shortTitle(task.title),
      attempt: status === "running" || status === "interrupted" ? currentAttempt(rs) : 0,
    };
    if (status === "draft" || status === "skipped" || status === "excluded") out.push(token);
    else if (status === "ready" || status === "queued") start.push(token);
    else if (status === "interrupted") {
      // Where it stopped, when this run's record says (past); else where a
      // run will resume it (Plan, live: the pickup answer's from_stage). A
      // restart, or no answer, waits at the start.
      const p = pickup?.tasks[task.id];
      const resumeAt = p?.mode === "resume" ? p.from_stage : undefined;
      const where = rs?.currentStage ?? resumeAt;
      const stage = where && xOf.has(where) ? where : undefined;
      if (stage === undefined) start.push(token);
      else at.set(stage, [...(at.get(stage) ?? []), token]);
    }
    else if (status === "done") done.push(token);
    else if (status === "blocked") {
      // Plan has no run to be blocked in: it's simply not in this run.
      if (mode === "plan") out.push(token);
      else {
        const stage = blockedAtStage(rs) ?? rs?.currentStage;
        const key = stage && xOf.has(stage) ? stage : "";
        below.set(key, [...(below.get(key) ?? []), token]);
      }
    } else {
      // running: above its current stage; before the first stage starts, the first station.
      const stage = rs?.currentStage && xOf.has(rs.currentStage) ? rs.currentStage : stages[0]?.name;
      if (stage === undefined) start.push(token);
      else at.set(stage, [...(at.get(stage) ?? []), token]);
    }
  }

  // Focus: which stations the focused task visited (Plan has no run to trace).
  const focusRs = focusTaskId && mode !== "plan" ? taskRunState[focusTaskId] : undefined;
  const tracing = !!focusTaskId && mode !== "plan";
  const focusStatus = tracing && tasksById[focusTaskId!] ? runStatusOf(tasksById[focusTaskId!], ctx) : undefined;
  const visited = new Set(
    (focusRs?.attempts ?? []).filter((a) => a.status === "completed" || a.status === "failed").map((a) => a.stage),
  );
  const nowStage =
    focusStatus === "running"
      ? (focusRs?.currentStage && xOf.has(focusRs.currentStage) ? focusRs.currentStage : stages[0]?.name)
      : undefined;

  const stations: MetroStation[] = stages.map((stage, i) => ({
    stage,
    x: xs[i],
    hot: (at.get(stage.name) ?? []).some((t) => t.status === "running"),
    focus: tracing ? (stage.name === nowStage ? "now" : visited.has(stage.name) ? "visited" : "dim") : undefined,
  }));

  const above = stages
    .filter((s) => at.has(s.name))
    .map((s) => makeStack(`stage:${s.name}`, s.name, xOf.get(s.name)!, at.get(s.name)!, focusTaskId));
  const blockedKeys = [...(below.has("") ? [""] : []), ...stages.map((s) => s.name).filter((n) => below.has(n))];
  const blocked = blockedKeys.map((k) =>
    makeStack(`blocked:${k}`, k ? `Blocked at ${k}` : "Blocked", k ? xOf.get(k)! : START_X, below.get(k)!, focusTaskId),
  );

  const arcs = buildArcs(stages, xs);
  const arcsBottom = arcs.length ? Math.max(...arcs.map((a) => a.bottom)) + ARC_LABEL_H / 2 : ARC_TOP - 6;
  const blockedTop = arcsBottom + ROW_GAP;
  const tallestBlocked = Math.max(0, ...blocked.map((s) => s.shown.length + (s.hidden ? 1 : 0)));
  const blockedH = tallestBlocked ? CAPTION_H + tallestBlocked * (TOKEN_H + TOKEN_GAP) : 0;
  const outTop = blockedTop + (blockedH ? blockedH + ROW_GAP : 0);
  const height = outTop + (out.length ? OUT_ROW_H : 0) + 4;

  const gap = stages.length > 1 ? (LAST_STATION_X - FIRST_STATION_X) / (stages.length - 1) : 30;
  const startLabel = mode === "plan" ? "Ready" : "Queued";

  return {
    startX: START_X,
    endX: END_X,
    labelWidth: Math.min(gap - 1, 22),
    terminusWidth: 2 * Math.min(START_X, 100 - END_X),
    startLabel,
    start: makeStack("start", startLabel, START_X, start, focusTaskId),
    done: makeStack("done", "Done", END_X, done, focusTaskId),
    out: makeStack("out", "Not in this run", 0, out, focusTaskId),
    stations,
    above,
    blocked,
    arcs,
    startFocus: tracing ? "dim" : undefined,
    endFocus: tracing ? (focusStatus === "done" ? "visited" : "dim") : undefined,
    geometry: { lineY: LINE_Y, arcTop: ARC_TOP, blockedTop, outTop, height },
  };
}
