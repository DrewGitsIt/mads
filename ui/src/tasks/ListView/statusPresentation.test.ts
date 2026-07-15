import { describe, expect, it } from "vitest";
import { isDependencyQueued, statusPresentation } from "./statusPresentation";
import type { Task } from "../../router/types";

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

describe("isDependencyQueued", () => {
  it("is false for a non-blocked task", () => {
    expect(isDependencyQueued(task({ status: "ready" }), {})).toBe(false);
  });

  it("is false for blocked with no dependencies (needs-human)", () => {
    expect(isDependencyQueued(task({ status: "blocked" }), {})).toBe(false);
  });

  it("is false when every dependency is done (needs-human)", () => {
    const dep = task({ id: "tsk_dep", status: "done" });
    const t = task({ status: "blocked", dependencies: ["tsk_dep"] });
    expect(isDependencyQueued(t, { tsk_dep: dep })).toBe(false);
  });

  it("is true when a dependency is not done (calm queued)", () => {
    const dep = task({ id: "tsk_dep", status: "ready" });
    const t = task({ status: "blocked", dependencies: ["tsk_dep"] });
    expect(isDependencyQueued(t, { tsk_dep: dep })).toBe(true);
  });
});

describe("statusPresentation", () => {
  it("renders a dependency-queued block calmly with info tone and dep chips", () => {
    const dep = task({ id: "tsk_dep", status: "ready", title: "the dep" });
    const t = task({ status: "blocked", dependencies: ["tsk_dep"] });
    const p = statusPresentation(t, { tsk_dep: dep });
    expect(p.label).toBe("Queued");
    expect(p.chipTone).toBe("info");
    expect(p.dashedRing).toBe(true);
    expect(p.depChips).toEqual(["tsk_dep"]);
  });

  it("renders a needs-human block with bad tone, no dashed ring", () => {
    const t = task({ status: "blocked" });
    const p = statusPresentation(t, {});
    expect(p.label).toBe("Needs human");
    expect(p.chipTone).toBe("bad");
    expect(p.dashedRing).toBe(false);
  });

  it("renders needs-human even if dependencies list is set but all done", () => {
    const dep = task({ id: "tsk_dep", status: "done" });
    const t = task({ status: "blocked", dependencies: ["tsk_dep"] });
    const p = statusPresentation(t, { tsk_dep: dep });
    expect(p.label).toBe("Needs human");
    expect(p.chipTone).toBe("bad");
  });

  it("renders ready/in_progress/needs_review/done with expected tones", () => {
    expect(statusPresentation(task({ status: "ready" }), {}).chipTone).toBe("neutral");
    expect(statusPresentation(task({ status: "in_progress" }), {}).chipTone).toBe("neutral");
    expect(statusPresentation(task({ status: "needs_review" }), {}).chipTone).toBe("warn");
    expect(statusPresentation(task({ status: "done" }), {}).chipTone).toBe("ok");
  });
});
