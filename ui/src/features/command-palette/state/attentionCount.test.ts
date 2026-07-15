import { describe, expect, it } from "vitest";
import { attentionCount } from "./attentionCount";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("attentionCount", () => {
  it("is 0 when no sessions need attention", () => {
    const sessions = [makeSession({ id: "s1", activity: "active" })];
    expect(attentionCount(sessions, {})).toBe(0);
  });

  it("counts inactive sessions with open tasks as needing attention", () => {
    const sessions = [
      makeSession({ id: "s1", activity: "inactive" }),
      makeSession({ id: "s2", activity: "inactive" }),
      makeSession({ id: "s3", activity: "active" }),
    ];
    expect(attentionCount(sessions, { s1: 2, s2: 0 })).toBe(1);
  });

  it("never counts an active session, regardless of open task count", () => {
    const sessions = [makeSession({ id: "s1", activity: "active" })];
    expect(attentionCount(sessions, { s1: 5 })).toBe(0);
  });

  it("treats a missing entry in openTaskCounts as zero", () => {
    const sessions = [makeSession({ id: "s1", activity: "inactive" })];
    expect(attentionCount(sessions, {})).toBe(0);
  });
});
