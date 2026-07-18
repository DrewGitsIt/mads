import { describe, expect, it } from "vitest";
import type { Task } from "../../router/types";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import { blockedReason, graphStatusAccessibleLabel, graphStatusOf } from "./graphStatus";

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

describe("graphStatusOf", () => {
  it("maps done", () => {
    expect(graphStatusOf(task({ status: "done" }))).toBe("done");
  });

  it("maps in_progress", () => {
    expect(graphStatusOf(task({ status: "in_progress" }))).toBe("in_progress");
  });

  it("maps blocked", () => {
    expect(graphStatusOf(task({ status: "blocked" }))).toBe("blocked");
  });

  it("maps a draft, non-terminal task to draft", () => {
    expect(graphStatusOf(task({ status: "ready", draft: true }))).toBe("draft");
  });

  it("maps ready", () => {
    expect(graphStatusOf(task({ status: "ready" }))).toBe("ready");
  });

  it("folds needs_review into ready", () => {
    expect(graphStatusOf(task({ status: "needs_review" }))).toBe("ready");
  });

  it("folds wont_do into ready", () => {
    expect(graphStatusOf(task({ status: "wont_do" }))).toBe("ready");
  });

  it("prioritizes done over draft", () => {
    expect(graphStatusOf(task({ status: "done", draft: true }))).toBe("done");
  });
});

describe("graphStatusAccessibleLabel", () => {
  it('returns "Draft" for a non-terminal draft task', () => {
    const t = task({ status: "ready", draft: true });
    expect(graphStatusAccessibleLabel(t, { [t.id]: t })).toBe("Draft");
  });

  it("delegates to statusPresentation's label otherwise", () => {
    const t = task({ status: "needs_review" });
    expect(graphStatusAccessibleLabel(t, { [t.id]: t })).toBe("Needs review");
  });

  it('reports "Needs human" for a blocked task with no undone dependency', () => {
    const t = task({ status: "blocked" });
    expect(graphStatusAccessibleLabel(t, { [t.id]: t })).toBe("Needs human");
  });

  it('reports "Queued" for a blocked task waiting on an undone dependency', () => {
    const dep = task({ id: "tsk_dep", status: "ready" });
    const t = task({ status: "blocked", dependencies: [dep.id] });
    const byId = { [t.id]: t, [dep.id]: dep };
    expect(graphStatusAccessibleLabel(t, byId)).toBe("Queued");
  });
});

describe("blockedReason", () => {
  it("returns undefined for a non-blocked task", () => {
    const t = task({ status: "ready" });
    expect(blockedReason(t, { [t.id]: t })).toBeUndefined();
  });

  it("reports a dependency-queued count first", () => {
    const dep = task({ id: "tsk_dep", status: "ready" });
    const t = task({ status: "blocked", dependencies: [dep.id] });
    const byId = { [t.id]: t, [dep.id]: dep };
    expect(blockedReason(t, byId)).toBe("waiting on 1 dependency");
  });

  it("pluralizes multiple undone dependencies", () => {
    const dep1 = task({ id: "tsk_dep1", status: "ready" });
    const dep2 = task({ id: "tsk_dep2", status: "ready" });
    const t = task({ status: "blocked", dependencies: [dep1.id, dep2.id] });
    const byId = { [t.id]: t, [dep1.id]: dep1, [dep2.id]: dep2 };
    expect(blockedReason(t, byId)).toBe("waiting on 2 dependencies");
  });

  it("falls back to the last failed stage's reason from run state", () => {
    const t = task({ status: "blocked" });
    const runState: Record<string, TaskRunState> = {
      [t.id]: {
        taskId: t.id,
        attempts: [
          { stage: "build", attempt: 1, status: "failed", reason: "compile error", startedSeq: 1, endedSeq: 2 },
        ],
        touchedFiles: [],
      },
    };
    expect(blockedReason(t, { [t.id]: t }, runState)).toBe("compile error");
  });

  it("falls back to the last blocker note when there's no failed-stage reason", () => {
    const t = task({
      status: "blocked",
      notes: [
        { type: "blocker", content: "waiting on human review", source: "agent", status: "confirmed", timestamp: "2026-01-01T00:00:00Z" },
      ],
    });
    expect(blockedReason(t, { [t.id]: t })).toBe("waiting on human review");
  });

  it("returns undefined when no source explains the block", () => {
    const t = task({ status: "blocked" });
    expect(blockedReason(t, { [t.id]: t })).toBeUndefined();
  });
});
