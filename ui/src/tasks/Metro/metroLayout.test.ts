import { describe, expect, it } from "vitest";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { PickupInfo, RunMode } from "../../pipeline/plan/runStatus";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import { ARC_TOP, arcLevels, metroLayout, shortTitle, stationXs, STACK_MAX } from "./metroLayout";
import { attempt, byId, DEFAULT_STAGES, runState, task } from "./testTasks";

function layoutOf(
  tasks: Task[],
  opts: {
    mode?: RunMode;
    excluded?: string[];
    rs?: Record<string, TaskRunState>;
    stages?: RunStage[];
    focus?: string;
    pickup?: PickupInfo;
  } = {},
) {
  return metroLayout({
    tasks,
    tasksById: byId(tasks),
    mode: opts.mode ?? "live",
    excluded: new Set(opts.excluded ?? []),
    taskRunState: opts.rs ?? {},
    stages: opts.stages ?? DEFAULT_STAGES,
    focusTaskId: opts.focus,
    pickup: opts.pickup,
  });
}

const ids = (tokens: { id: string }[]) => tokens.map((t) => t.id);

describe("metroLayout placement", () => {
  const tasks = [
    task("ready"),
    task("run", { status: "in_progress" }),
    task("done", { status: "done" }),
    task("blk", { status: "blocked" }),
    task("draft", { draft: true }),
    task("excl"),
    task("dep", { dependencies: ["excl"] }),
  ];
  const rs = {
    run: runState("run", [attempt("executor", 1, "completed"), attempt("build", 1, "failed"), attempt("executor", 2, "started")], "executor"),
    blk: runState("blk", [attempt("executor", 1, "completed"), attempt("code_review", 1, "failed", "nope"), attempt("merge", 1, "skipped")], "code_review"),
  };

  it("puts each status where the design says (live)", () => {
    const l = layoutOf(tasks, { rs, excluded: ["excl"] });
    expect(l.startLabel).toBe("Queued");
    // dep depends on a task the run left out: it never runs, so it is out too.
    expect(ids(l.start.tokens)).toEqual(["ready"]);
    expect(ids(l.done.tokens)).toEqual(["done"]);
    expect(ids(l.out.tokens)).toEqual(["draft", "excl", "dep"]);
    expect(l.above.map((s) => [s.key, ids(s.tokens)])).toEqual([["stage:executor", ["run"]]]);
    expect(l.blocked.map((s) => [s.key, ids(s.tokens)])).toEqual([["blocked:code_review", ["blk"]]]);
    expect(l.blocked[0].heading).toBe("Blocked at code_review");
  });

  it("marks the station a task is running at as hot and carries its attempt", () => {
    const l = layoutOf(tasks, { rs });
    expect(l.stations.filter((s) => s.hot).map((s) => s.stage.name)).toEqual(["executor"]);
    expect(l.above[0].tokens[0].attempt).toBe(2);
  });

  it("in Plan, the start terminus is Ready and anything that won't run is out", () => {
    const l = layoutOf(tasks, { mode: "plan", excluded: ["excl"] });
    expect(l.startLabel).toBe("Ready");
    expect(ids(l.start.tokens)).toEqual(["ready"]);
    expect(ids(l.out.tokens).sort()).toEqual(["blk", "dep", "draft", "excl", "run"].sort());
    expect(l.blocked).toEqual([]);
    expect(l.above).toEqual([]);
  });

  it("puts a blocked task with no failed attempt under the start terminus", () => {
    const l = layoutOf([task("b", { status: "blocked" })]);
    expect(l.blocked).toHaveLength(1);
    expect(l.blocked[0].key).toBe("blocked:");
    expect(l.blocked[0].x).toBe(l.startX);
  });

  it("puts a running task with no stage yet on the first station", () => {
    const l = layoutOf([task("r", { status: "in_progress" })]);
    expect(l.above[0].key).toBe("stage:architect");
  });

  it("uses the stage of the LAST failed attempt for a blocked task", () => {
    const b = task("b", { status: "blocked" });
    const l = layoutOf([b], {
      rs: { b: runState("b", [attempt("build", 1, "failed"), attempt("executor", 2, "completed"), attempt("verify", 1, "failed")], "verify") },
    });
    expect(l.blocked[0].key).toBe("blocked:verify");
  });
});

describe("metroLayout Interrupted tokens", () => {
  const tasks = [task("res", { status: "in_progress" }), task("rst", { status: "in_progress" }), task("bare", { status: "in_progress" })];
  const pickup: PickupInfo = {
    sharedDirectory: false,
    tasks: {
      res: { task_id: "res", mode: "resume", from_stage: "code_review", template_name: "t", template_version: 1 },
      rst: { task_id: "rst", mode: "restart", from_stage: "architect", template_name: "t", template_version: 2 },
      bare: { task_id: "bare", mode: "resume", template_name: "t", template_version: 1 },
    },
  };

  it("in Plan, sits at the stage a resume picks up from; a restart, or no stage, waits at Ready", () => {
    const l = layoutOf(tasks, { mode: "plan", pickup });
    expect(l.above.map((s) => [s.key, ids(s.tokens)])).toEqual([["stage:code_review", ["res"]]]);
    expect(l.above[0].tokens[0].status).toBe("interrupted");
    expect(ids(l.start.tokens)).toEqual(["rst", "bare"]);
    expect(l.stations.some((s) => s.hot)).toBe(false);
  });

  it("in a live run that hasn't picked it up yet, sits where it will resume", () => {
    const l = layoutOf(tasks, { pickup });
    expect(l.above.map((s) => [s.key, ids(s.tokens)])).toEqual([["stage:code_review", ["res"]]]);
    expect(ids(l.start.tokens)).toEqual(["rst", "bare"]);
  });

  it("in a past run, sits where that run's record says it stopped", () => {
    const rs = { res: runState("res", [attempt("executor", 1, "started")], "executor") };
    const l = layoutOf([tasks[0]], { mode: "past", rs });
    expect(l.above.map((s) => [s.key, ids(s.tokens)])).toEqual([["stage:executor", ["res"]]]);
  });
});

describe("metroLayout stack collapse", () => {
  const many = (n: number) => Array.from({ length: n }, (_, i) => task(`t${i}`));

  it("shows up to 3 tokens without collapsing", () => {
    const l = layoutOf(many(STACK_MAX));
    expect(l.start.shown).toHaveLength(3);
    expect(l.start.hidden).toBe(0);
  });

  it("past 3, shows 2 tokens and hides the rest behind +N", () => {
    const l = layoutOf(many(7));
    expect(ids(l.start.shown)).toEqual(["t0", "t1"]);
    expect(l.start.hidden).toBe(5);
    expect(l.start.tokens).toHaveLength(7);
  });

  it("keeps the focused task visible in a collapsed stack", () => {
    const l = layoutOf(many(7), { focus: "t5" });
    expect(ids(l.start.shown)).toEqual(["t0", "t5"]);
    expect(l.start.hidden).toBe(5);
  });

  it("collapses blocked and out stacks too", () => {
    const tasks = [...Array.from({ length: 4 }, (_, i) => task(`d${i}`, { draft: true })), ...Array.from({ length: 5 }, (_, i) => task(`b${i}`, { status: "blocked" }))];
    const l = layoutOf(tasks);
    expect(l.out.hidden).toBe(2);
    expect(l.blocked[0].hidden).toBe(3);
  });
});

describe("metroLayout focus", () => {
  const t = task("f", { status: "in_progress" });
  const rs = { f: runState("f", [attempt("architect", 1, "skipped"), attempt("executor", 1, "completed"), attempt("build", 1, "failed"), attempt("executor", 2, "started")], "executor") };

  it("highlights the focused task's visited and current stations and dims the rest", () => {
    const l = layoutOf([t], { rs, focus: "f" });
    const focus = Object.fromEntries(l.stations.map((s) => [s.stage.name, s.focus]));
    expect(focus).toEqual({ architect: "dim", executor: "now", build: "visited", code_review: "dim", merge: "dim", verify: "dim" });
    expect(l.startFocus).toBe("dim");
    expect(l.endFocus).toBe("dim");
  });

  it("lights the end terminus for a done task", () => {
    const d = task("d", { status: "done" });
    const l = layoutOf([d], { rs: { d: runState("d", [attempt("executor", 1, "completed")], "executor") }, focus: "d" });
    expect(l.endFocus).toBe("visited");
    expect(l.stations.find((s) => s.stage.name === "executor")?.focus).toBe("visited");
  });

  it("leaves stations alone without focus, and in Plan", () => {
    expect(layoutOf([t], { rs }).stations.every((s) => s.focus === undefined)).toBe(true);
    expect(layoutOf([task("p")], { mode: "plan", focus: "p" }).stations.every((s) => s.focus === undefined)).toBe(true);
  });
});

describe("metroLayout arcs", () => {
  const stagesN = (n: number, loops: Record<number, number> = {}): RunStage[] =>
    Array.from({ length: n }, (_, i) => ({
      name: `s${i}`,
      kind: i % 2 ? "command" : "agent",
      ...(loops[i] !== undefined ? { loopBack: `s${loops[i]}`, maxLoops: 2 } : {}),
    }));

  it("spreads 1–8 stations between the termini in order", () => {
    for (let n = 1; n <= 8; n++) {
      const xs = stationXs(n);
      expect(xs).toHaveLength(n);
      for (let i = 1; i < n; i++) expect(xs[i]).toBeGreaterThan(xs[i - 1]);
      const l = layoutOf([], { stages: stagesN(n) });
      expect(xs[0]).toBeGreaterThan(l.startX);
      expect(xs[n - 1]).toBeLessThan(l.endX);
      expect(l.arcs).toEqual([]);
    }
  });

  it("draws one arc per loop-back with its label, below the station labels", () => {
    const l = layoutOf([]);
    expect(l.arcs.map((a) => `${a.from}>${a.to}`)).toEqual(["build>executor", "code_review>executor"]);
    expect(l.arcs.map((a) => a.label)).toEqual(["↺ on fail ≤2", "↺ on fail ≤2"]);
    for (const a of l.arcs) {
      // The label (centred on the arc's run, 14px tall) clears the sub-labels above ARC_TOP.
      expect(a.bottom - 7).toBeGreaterThan(ARC_TOP);
      expect(a.path.startsWith(`M ${a.fromX * 10} ${ARC_TOP}`)).toBe(true);
    }
  });

  it("nests two loops into the same target at different depths, labels apart", () => {
    const [inner, outer] = layoutOf([]).arcs;
    expect(inner.level).toBe(0);
    expect(outer.level).toBe(1);
    // The outer arc's run sits below the inner one's label.
    expect(outer.bottom - 7).toBeGreaterThanOrEqual(inner.bottom + 7);
    expect(inner.labelX).not.toBe(outer.labelX);
  });

  it("moves a label off a deeper arc's vertical that crosses its run", () => {
    // s1>s0... s3>s1 sits deeper than s2>s0 and crosses it at s1.
    const stages = stagesN(4, { 2: 0, 3: 1 });
    const l = layoutOf([], { stages });
    const xs = stationXs(4);
    const a = l.arcs.find((x) => x.from === "s2")!;
    const b = l.arcs.find((x) => x.from === "s3")!;
    expect(a.level).not.toBe(b.level);
    const shallow = a.level < b.level ? a : b;
    const deep = shallow === a ? b : a;
    const crossing = [deep.fromX, deep.toX].find((x) => x > Math.min(shallow.fromX, shallow.toX) && x < Math.max(shallow.fromX, shallow.toX));
    expect(crossing).toBeDefined();
    expect(shallow.labelX).not.toBe(crossing);
    expect(Math.abs(shallow.labelX - crossing!)).toBeGreaterThanOrEqual((xs[1] - xs[0]) / 2 - 1e-9);
  });

  it("handles every loop shape on 1–8 stages: levels never collide where spans meet", () => {
    for (let n = 1; n <= 8; n++) {
      // Every stage past the first loops back to stage 0 or its predecessor.
      const loops: Record<number, number> = {};
      for (let i = 1; i < n; i++) loops[i] = i % 2 ? 0 : i - 1;
      const l = layoutOf([], { stages: stagesN(n, loops) });
      expect(l.arcs).toHaveLength(Math.max(0, n - 1));
      for (const a of l.arcs) {
        for (const b of l.arcs) {
          if (a === b) continue;
          const [alo, ahi] = [Math.min(a.fromX, a.toX), Math.max(a.fromX, a.toX)];
          const [blo, bhi] = [Math.min(b.fromX, b.toX), Math.max(b.fromX, b.toX)];
          if (alo <= bhi && blo <= ahi) expect(a.level).not.toBe(b.level);
        }
      }
      // The blocked row and out row sit below the deepest arc's label.
      const deepest = Math.max(ARC_TOP, ...l.arcs.map((a) => a.bottom + 7));
      expect(l.geometry.blockedTop).toBeGreaterThan(deepest);
      expect(l.geometry.height).toBeGreaterThanOrEqual(l.geometry.outTop);
    }
  });

  it("ignores self-loops and unknown targets, and labels a loop with no max", () => {
    const stages: RunStage[] = [
      { name: "a", kind: "agent" },
      { name: "b", kind: "agent", loopBack: "b" },
      { name: "c", kind: "agent", loopBack: "nope" },
      { name: "d", kind: "agent", loopBack: "a" },
    ];
    const l = layoutOf([], { stages });
    expect(l.arcs.map((a) => [a.from, a.to, a.label])).toEqual([["d", "a", "↺ on fail"]]);
  });

  it("arcLevels puts disjoint spans on one level and nested ones deeper", () => {
    expect(arcLevels([{ lo: 0, hi: 1 }, { lo: 2, hi: 3 }])).toEqual([0, 0]);
    expect(arcLevels([{ lo: 0, hi: 3 }, { lo: 1, hi: 2 }])).toEqual([1, 0]);
    expect(arcLevels([{ lo: 0, hi: 1 }, { lo: 1, hi: 2 }])).toEqual([0, 1]);
  });

  it("pushes the out row below blocked stacks", () => {
    const tasks = [task("b", { status: "blocked" }), task("d", { draft: true })];
    const l = layoutOf(tasks);
    expect(l.geometry.outTop).toBeGreaterThan(l.geometry.blockedTop);
    const noBlocked = layoutOf([task("d", { draft: true })]);
    expect(noBlocked.geometry.outTop).toBe(noBlocked.geometry.blockedTop);
  });
});

describe("shortTitle", () => {
  it("keeps short titles and cuts long ones at a word", () => {
    expect(shortTitle("Fix login")).toBe("Fix login");
    expect(shortTitle("Add refresh-token rotation with reuse detection")).toBe("Add refresh-token…");
    expect(shortTitle("Supercalifragilisticexpialidocious")).toBe("Supercalifragilist…");
    expect(shortTitle("Rate-limit POST /login, per IP")).toBe("Rate-limit POST…");
  });
});
