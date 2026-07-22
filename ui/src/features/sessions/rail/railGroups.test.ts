import { describe, expect, it } from "vitest";
import { dotState, groupSessions, totalOpenTasks } from "./railGroups";
import type { OpenTaskBreakdown } from "./railGroups";
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

function makeBreakdown(overrides: Partial<OpenTaskBreakdown>): OpenTaskBreakdown {
  return { running: 0, ready: 0, blocked: 0, ...overrides };
}

describe("dotState", () => {
  it("is always 'on' for an active session, regardless of open task breakdown", () => {
    const session = makeSession({ activity: "active" });
    expect(dotState(session, makeBreakdown({}))).toBe("on");
    expect(dotState(session, makeBreakdown({ blocked: 5 }))).toBe("on");
  });

  it("is 'attn' for an inactive session with blocked work", () => {
    const session = makeSession({ activity: "inactive" });
    expect(dotState(session, makeBreakdown({ blocked: 1 }))).toBe("attn");
  });

  it("is 'off' — the softer existing treatment — for an inactive session with only running/ready work", () => {
    const session = makeSession({ activity: "inactive" });
    expect(dotState(session, makeBreakdown({ running: 2, ready: 3 }))).toBe("off");
  });

  it("is 'off' for an inactive session with no open work", () => {
    const session = makeSession({ activity: "inactive" });
    expect(dotState(session, makeBreakdown({}))).toBe("off");
  });
});

describe("totalOpenTasks", () => {
  it("sums every bucket", () => {
    expect(totalOpenTasks(makeBreakdown({ running: 2, ready: 1, blocked: 3 }))).toBe(6);
  });

  it("is 0 for an all-zero breakdown", () => {
    expect(totalOpenTasks(makeBreakdown({}))).toBe(0);
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
