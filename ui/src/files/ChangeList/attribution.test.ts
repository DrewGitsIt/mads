import { describe, expect, it } from "vitest";
import type { Commit, FileChange } from "../../router/types";
import { attributionForChange, attributionForCommit } from "./attribution";

function fileChange(overrides: Partial<FileChange>): FileChange {
  return {
    id: 1,
    session_id: "s1",
    path: "a.go",
    change: "modified",
    origin: "tool_use",
    attributed: true,
    timestamp: "2026-07-14T00:00:00Z",
    ...overrides,
  };
}

function commit(overrides: Partial<Commit>): Commit {
  return {
    hash: "abc1234",
    session_id: "s1",
    message: "a commit",
    timestamp: "2026-07-14T00:00:00Z",
    ...overrides,
  };
}

describe("attributionForChange", () => {
  it("returns actor for an attributed change", () => {
    expect(attributionForChange(fileChange({ attributed: true, actor: "agent" }))).toBe("actor");
  });

  it("returns unattributed for an observed, unattributed change", () => {
    expect(
      attributionForChange(fileChange({ attributed: false, origin: "observed", actor: undefined })),
    ).toBe("unattributed");
  });
});

describe("attributionForCommit", () => {
  it("returns trailer for an explicit Task: trailer link", () => {
    expect(attributionForCommit(commit({ linked_via: "trailer" }))).toBe("trailer");
  });

  it("returns session for a session-context link", () => {
    expect(attributionForCommit(commit({ linked_via: "session_context" }))).toBe("session");
  });

  it("returns heuristic for a file-overlap candidate link", () => {
    expect(attributionForCommit(commit({ linked_via: "heuristic" }))).toBe("heuristic");
  });

  it("returns orphan for a flagged orphan commit, even if linked_via is set", () => {
    expect(attributionForCommit(commit({ orphan: true, linked_via: "heuristic" }))).toBe("orphan");
  });

  it("falls back to unattributed when neither orphan nor linked_via is set", () => {
    expect(attributionForCommit(commit({}))).toBe("unattributed");
  });
});
