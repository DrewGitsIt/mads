import { describe, expect, it } from "vitest";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import { buildEdges } from "./layout";
import {
  criticalEdgeIds,
  edgeRunKindOf,
  nodeRunViews,
  skipsByLabel,
  stageStatesOf,
  type RunViewContext,
} from "./runView";

function task(overrides: Partial<Task>): Task {
  return {
    id: "tsk_1",
    session_id: "ses_1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "a task",
    source: "user",
    ...overrides,
  };
}

const STAGES: RunStage[] = [
  { name: "architect", kind: "agent", when: '!contains(labels, "trivial")' },
  { name: "executor", kind: "agent" },
  { name: "build", kind: "command" },
  { name: "code_review", kind: "agent" },
];

function runState(taskId: string, attempts: TaskRunState["attempts"], currentStage?: string): TaskRunState {
  return { taskId, currentStage, attempts, touchedFiles: [] };
}

function states(cells: { state: string }[]) {
  return cells.map((c) => c.state);
}

function ctxFor(tasks: Task[], overrides: Partial<RunViewContext> = {}): RunViewContext {
  return {
    mode: "live",
    tasksById: Object.fromEntries(tasks.map((t) => [t.id, t])),
    excluded: new Set(),
    stages: STAGES,
    taskRunState: {},
    ...overrides,
  };
}

describe("skipsByLabel", () => {
  it("skips a stage whose when: negates a label the task has", () => {
    expect(skipsByLabel(task({ labels: ["trivial"] }), STAGES[0])).toBe(true);
  });
  it("runs it when the task lacks the label, or the stage has no when:", () => {
    expect(skipsByLabel(task({ labels: ["docs"] }), STAGES[0])).toBe(false);
    expect(skipsByLabel(task({ labels: ["trivial"] }), STAGES[1])).toBe(false);
  });
  it("ignores conditions it doesn't recognize", () => {
    expect(skipsByLabel(task({ labels: ["trivial"] }), { name: "x", kind: "agent", when: 'contains(labels, "trivial")' })).toBe(
      false,
    );
  });
});

describe("stageStatesOf", () => {
  it("live: each stage shows its newest attempt — done, now, waiting", () => {
    const t = task({ status: "in_progress" });
    const rs = runState(
      t.id,
      [
        { stage: "architect", attempt: 1, status: "completed", startedSeq: 1 },
        { stage: "executor", attempt: 1, status: "started", startedSeq: 2 },
      ],
      "executor",
    );
    expect(states(stageStatesOf(t, "running", "live", STAGES, rs))).toEqual(["done", "now", "waiting", "waiting"]);
  });

  it("after a loop back the failure stays visible and stages to re-run read waiting", () => {
    const t = task({ status: "in_progress" });
    const rs = runState(
      t.id,
      [
        { stage: "architect", attempt: 1, status: "skipped", startedSeq: 1 },
        { stage: "executor", attempt: 1, status: "completed", startedSeq: 2 },
        { stage: "build", attempt: 1, status: "completed", startedSeq: 3 },
        { stage: "code_review", attempt: 1, status: "failed", reason: "nope", startedSeq: 4 },
        { stage: "executor", attempt: 2, status: "started", startedSeq: 5 },
      ],
      "executor",
    );
    expect(states(stageStatesOf(t, "running", "live", STAGES, rs))).toEqual(["skipped", "now", "waiting", "failed"]);
  });

  it("a done task with no attempts this run is all done; one with attempts marks stages it never ran skipped", () => {
    const t = task({ status: "done" });
    expect(states(stageStatesOf(t, "done", "past", STAGES, undefined))).toEqual(["done", "done", "done", "done"]);
    const rs = runState(t.id, [{ stage: "executor", attempt: 1, status: "completed", startedSeq: 1 }]);
    expect(states(stageStatesOf(t, "done", "past", STAGES, rs))).toEqual(["skipped", "done", "skipped", "skipped"]);
  });

  it("is off for a task not in this run", () => {
    expect(states(stageStatesOf(task({}), "excluded", "live", STAGES, undefined))).toEqual(["off", "off", "off", "off"]);
    expect(states(stageStatesOf(task({ draft: true }), "draft", "plan", STAGES, undefined))).toEqual([
      "off",
      "off",
      "off",
      "off",
    ]);
  });

  it("plan: a task that will run waits on every stage, skipping label-gated ones", () => {
    const t = task({ labels: ["trivial"] });
    expect(states(stageStatesOf(t, "ready", "plan", STAGES, undefined))).toEqual(["skipped", "waiting", "waiting", "waiting"]);
  });
});

describe("nodeRunViews — plan", () => {
  const a = task({ id: "a", title: "Rotate tokens" });
  const b = task({ id: "b", title: "Revoke family", dependencies: ["a"], labels: ["trivial"] });
  const c = task({ id: "c", title: "Docs", dependencies: ["b"] });
  const d = task({ id: "d", title: "Draft idea", draft: true });
  const tasks = [a, b, c, d];

  it("gives ready tasks a wave and their skipped stages, and no status line", () => {
    const views = nodeRunViews(tasks, ctxFor(tasks, { mode: "plan" }));
    expect(views.a).toMatchObject({ status: "ready", wave: 1, skips: [], line: undefined });
    expect(views.b).toMatchObject({ wave: 2, skips: ["architect"] });
    expect(views.c.wave).toBe(3);
  });

  it("ticks the include box for included tasks, and disables it for drafts", () => {
    const views = nodeRunViews(tasks, ctxFor(tasks, { mode: "plan", excluded: new Set(["b"]) }));
    expect(views.a.include).toEqual({ checked: true, disabled: false });
    expect(views.b.include).toEqual({ checked: false, disabled: false });
    expect(views.d.include).toEqual({ checked: false, disabled: true, reason: "Drafts can't run until confirmed" });
  });

  it("gives someone's own in-progress task an unchecked, disabled box; an Interrupted one stays tickable", () => {
    const own = task({ id: "own", status: "in_progress" });
    const int = task({ id: "int", status: "in_progress" });
    const all = [own, int];
    const views = nodeRunViews(all, ctxFor(all, { mode: "plan", interrupted: new Set(["int"]) }));
    expect(views.own.include).toEqual({ checked: false, disabled: true, reason: "Already in progress: a run won't pick it up" });
    expect(views.int.include).toEqual({ checked: true, disabled: false });
  });

  it("names why a task won't run", () => {
    const views = nodeRunViews(tasks, ctxFor(tasks, { mode: "plan", excluded: new Set(["b"]) }));
    expect(views.b.line?.text).toBe("left out of this run");
    expect(views.c.line).toEqual({ text: "won't run: depends on Revoke family", tone: "warn" });
    expect(views.d.line?.text).toBe("draft: confirm it before it can run");
    expect(views.c.wave).toBeUndefined();
  });

  it("has no include box outside Plan", () => {
    expect(nodeRunViews(tasks, ctxFor(tasks)).a.include).toBeUndefined();
  });
});

describe("nodeRunViews — live and past status lines", () => {
  it("running: the stage, with the attempt once it's past the first", () => {
    const t = task({ id: "t", status: "in_progress" });
    const first = runState("t", [{ stage: "build", attempt: 1, status: "started", startedSeq: 1 }], "build");
    expect(nodeRunViews([t], ctxFor([t], { taskRunState: { t: first } })).t.line).toEqual({ text: "build", tone: "running" });
    const again = runState("t", [{ stage: "executor", attempt: 2, status: "started", startedSeq: 1 }], "executor");
    expect(nodeRunViews([t], ctxFor([t], { taskRunState: { t: again } })).t.line?.text).toBe("executor · attempt 2");
  });

  it("blocked: the stage that blocked it and why, and offers retry", () => {
    const t = task({ id: "t", status: "blocked" });
    const rs = runState("t", [{ stage: "code_review", attempt: 2, status: "failed", reason: "3 callers left", startedSeq: 1 }]);
    const view = nodeRunViews([t], ctxFor([t], { taskRunState: { t: rs } })).t;
    expect(view.line).toEqual({ text: "Blocked at code_review: 3 callers left", tone: "bad" });
    expect(view.retryable).toBe(true);
  });

  it("queued: the dependency it waits on", () => {
    const dep = task({ id: "dep", title: "Rotate tokens", status: "in_progress" });
    const t = task({ id: "t", dependencies: ["dep"] });
    expect(nodeRunViews([dep, t], ctxFor([dep, t])).t.line?.text).toBe("waits on Rotate tokens");
  });

  it("done, and not in this run", () => {
    const done = task({ id: "x", status: "done" });
    const draft = task({ id: "y", draft: true });
    const views = nodeRunViews([done, draft], ctxFor([done, draft], { mode: "past" }));
    expect(views.x.line?.text).toBe("✓ done");
    expect(views.y.line?.text).toBe("not in this run");
    expect(views.y.retryable).toBe(false);
  });
});

describe("edgeRunKindOf", () => {
  it("plan: the critical path, and cuts into tasks a left-out dependency stops", () => {
    const a = task({ id: "a" });
    const b = task({ id: "b", dependencies: ["a"] });
    const c = task({ id: "c" });
    const e = task({ id: "e", dependencies: ["c"] });
    const tasks = [a, b, c, e];
    const views = nodeRunViews(tasks, ctxFor(tasks, { mode: "plan", excluded: new Set(["c"]) }));
    const critical = criticalEdgeIds(["a", "b"]);
    const kinds = Object.fromEntries(buildEdges(tasks).map((edge) => [edge.id, edgeRunKindOf(edge, "plan", views, critical)]));
    expect(kinds).toEqual({ "a->b": "critical", "c->e": "cut" });
  });

  it("live: hot into a running task, done along a finished chain", () => {
    const a = task({ id: "a", status: "done" });
    const b = task({ id: "b", status: "in_progress", dependencies: ["a"] });
    const c = task({ id: "c", status: "done", dependencies: ["a"] });
    const d = task({ id: "d", dependencies: ["b"] });
    const tasks = [a, b, c, d];
    const views = nodeRunViews(tasks, ctxFor(tasks, { taskRunState: { b: { taskId: "b", attempts: [], touchedFiles: [] } } }));
    const kinds = Object.fromEntries(buildEdges(tasks).map((edge) => [edge.id, edgeRunKindOf(edge, "live", views, new Set())]));
    expect(kinds).toEqual({ "a->b": "hot", "a->c": "done", "b->d": undefined });
  });
});

describe("criticalEdgeIds", () => {
  it("pairs consecutive path steps as dep->task edge ids", () => {
    expect([...criticalEdgeIds(["a", "b", "c"])]).toEqual(["a->b", "b->c"]);
    expect(criticalEdgeIds(["a"]).size).toBe(0);
  });
});
