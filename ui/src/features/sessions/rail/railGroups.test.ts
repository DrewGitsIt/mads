import { describe, expect, it } from "vitest";
import { dotState, groupSessions } from "./railGroups";
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

describe("dotState", () => {
  it("is always 'on' for an active session, regardless of open task count", () => {
    const session = makeSession({ activity: "active" });
    expect(dotState(session, 0)).toBe("on");
    expect(dotState(session, 5)).toBe("on");
  });

  it("is 'attn' for an inactive session with open tasks", () => {
    const session = makeSession({ activity: "inactive" });
    expect(dotState(session, 3)).toBe("attn");
  });

  it("is 'off' for an inactive session with no open tasks", () => {
    const session = makeSession({ activity: "inactive" });
    expect(dotState(session, 0)).toBe("off");
  });
});

describe("groupSessions", () => {
  it("splits active from everything else", () => {
    const active1 = makeSession({ id: "a1", activity: "active" });
    const recent1 = makeSession({ id: "r1", activity: "inactive" });
    const { active, recent } = groupSessions([active1, recent1]);
    expect(active.map((s) => s.id)).toEqual(["a1"]);
    expect(recent.map((s) => s.id)).toEqual(["r1"]);
  });

  it("sorts each group most-recently-updated first", () => {
    const older = makeSession({ id: "old", activity: "active", updated_at: "2026-07-01T00:00:00Z" });
    const newer = makeSession({ id: "new", activity: "active", updated_at: "2026-07-10T00:00:00Z" });
    const { active } = groupSessions([older, newer]);
    expect(active.map((s) => s.id)).toEqual(["new", "old"]);
  });
});
