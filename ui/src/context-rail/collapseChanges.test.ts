import { describe, expect, it } from "vitest";
import { collapseChanges } from "./collapseChanges";
import type { FileChange } from "../router/types";

function change(overrides: Partial<FileChange>): FileChange {
  return {
    id: 1,
    session_id: "s1",
    path: "src/a.ts",
    change: "modified",
    origin: "tool_use",
    attributed: true,
    timestamp: "2026-07-30T00:00:00Z",
    ...overrides,
  };
}

describe("collapseChanges", () => {
  it("collapses the same path touched across tasks into a single row", () => {
    const out = collapseChanges([
      change({ id: 1, task_id: "t1", change: "modified" }),
      change({ id: 2, task_id: "t2", change: "modified" }),
      change({ id: 3, task_id: "t3", change: "modified" }),
    ]);

    expect(out).toHaveLength(1);
    expect(out[0].path).toBe("src/a.ts");
    expect(out[0].change).toBe("modified");
    // Representative row carries the latest touch's identity.
    expect(out[0].id).toBe(3);
  });

  it("counts a created-then-modified file once, as added", () => {
    const out = collapseChanges([
      change({ id: 1, change: "added" }),
      change({ id: 2, change: "modified" }),
    ]);

    expect(out).toHaveLength(1);
    expect(out[0].change).toBe("added");
  });

  it("drops a file created and then deleted within the session", () => {
    const out = collapseChanges([
      change({ id: 1, change: "added" }),
      change({ id: 2, change: "deleted" }),
    ]);

    expect(out).toHaveLength(0);
  });

  it("reports a pre-existing file that was deleted as deleted", () => {
    const out = collapseChanges([
      change({ id: 1, change: "modified" }),
      change({ id: 2, change: "deleted" }),
    ]);

    expect(out).toHaveLength(1);
    expect(out[0].change).toBe("deleted");
  });

  it("keeps the same path in different repos as separate rows", () => {
    const out = collapseChanges([
      change({ id: 1, repo: "web", path: "README.md" }),
      change({ id: 2, repo: "api", path: "README.md" }),
    ]);

    expect(out).toHaveLength(2);
    expect(out.map((c) => c.repo)).toEqual(["web", "api"]);
  });

  it("treats a file as attributed if any touch was attributed", () => {
    const out = collapseChanges([
      change({ id: 1, attributed: false, actor: undefined }),
      change({ id: 2, attributed: true, actor: "claude" }),
      change({ id: 3, attributed: false, actor: undefined }),
    ]);

    expect(out).toHaveLength(1);
    expect(out[0].attributed).toBe(true);
    expect(out[0].actor).toBe("claude");
  });

  it("preserves first-touch ordering across distinct files", () => {
    const out = collapseChanges([
      change({ id: 1, path: "b.ts" }),
      change({ id: 2, path: "a.ts" }),
      change({ id: 3, path: "b.ts" }),
    ]);

    expect(out.map((c) => c.path)).toEqual(["b.ts", "a.ts"]);
  });
});
