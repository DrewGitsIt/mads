import { describe, expect, it } from "vitest";
import { dotState, groupSessions, statusOf, totalOpenTasks } from "./railGroups";
import type { OpenTaskBreakdown, SessionAgentState } from "./railGroups";
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

function makeAgentState(overrides: Partial<SessionAgentState> = {}): SessionAgentState {
  return { subagents: 0, ...overrides };
}

describe("statusOf", () => {
  const idle = makeBreakdown({});

  it("reads a crash first, naming its blocked work when there is any", () => {
    const state = makeAgentState({ crashed: true, awaitingTool: "Bash", workingSince: "2026-07-01T00:00:00Z", done: true });
    expect(statusOf(makeSession({}), state, idle)).toMatchObject({
      kind: "crashed",
      tone: "bad",
      dot: "bad",
      glyph: "✕",
      label: "Crashed · reopen to restart",
    });
    expect(statusOf(makeSession({}), state, makeBreakdown({ blocked: 2 })).label).toBe(
      "Crashed · 2 blocked tasks",
    );
    expect(statusOf(makeSession({}), state, makeBreakdown({ blocked: 1 })).label).toBe(
      "Crashed · 1 blocked task",
    );
  });

  it("puts an approval park ahead of working and done", () => {
    const state = makeAgentState({
      awaitingTool: "Bash",
      awaitingSince: "2026-07-01T00:00:00Z",
      workingSince: "2026-07-01T00:00:00Z",
      done: true,
    });
    expect(statusOf(makeSession({}), state, idle)).toEqual({
      kind: "needs",
      tone: "needs",
      dot: "attn",
      glyph: "▲",
      label: "Needs approval · Bash",
      since: "2026-07-01T00:00:00Z",
    });
  });

  it("names no tool when the request didn't carry one", () => {
    const state = makeAgentState({ awaitingSince: "2026-07-01T00:00:00Z" });
    expect(statusOf(makeSession({}), state, idle).label).toBe("Needs approval");
  });

  it("spins — no glyph — while a turn is in flight, and counts subagents when it has them", () => {
    const working = makeAgentState({ workingSince: "2026-07-01T00:00:00Z", done: true });
    expect(statusOf(makeSession({}), working, idle)).toEqual({
      kind: "working",
      tone: "working",
      dot: "work",
      // Phase 0 has no tool title or thinking signal on the control plane.
      label: "Working…",
      since: "2026-07-01T00:00:00Z",
    });
    expect(statusOf(makeSession({}), { ...working, subagents: 3 }, idle).label).toBe(
      "3 subagents running",
    );
    expect(statusOf(makeSession({}), { ...working, subagents: 1 }, idle).label).toBe(
      "1 subagent running",
    );
  });

  it("holds 'Done · your turn' for a live session whose turn ended", () => {
    const state = makeAgentState({ done: true });
    expect(statusOf(makeSession({ activity: "active" }), state, idle)).toEqual({
      kind: "done",
      tone: "done",
      dot: "on",
      glyph: "✓",
      label: "Done · your turn",
    });
  });

  it("is 'Ready' for a live session that has said nothing", () => {
    expect(statusOf(makeSession({ activity: "active" }), makeAgentState(), idle)).toEqual({
      kind: "ready",
      tone: "idle",
      dot: "on",
      glyph: "◦",
      label: "Ready",
    });
  });

  it("is 'Sleeping' for an inactive session, even one whose turn once ended", () => {
    const sleeping = makeSession({ activity: "inactive" });
    expect(statusOf(sleeping, makeAgentState({ done: true }), idle)).toEqual({
      kind: "sleeping",
      tone: "idle",
      dot: "off",
      glyph: "◦",
      label: "Sleeping · wakes on send",
    });
  });

  it("keeps the rail's existing amber puck for a sleeping session with blocked work", () => {
    const sleeping = makeSession({ activity: "inactive" });
    expect(statusOf(sleeping, makeAgentState(), makeBreakdown({ blocked: 1 })).dot).toBe("attn");
  });

  it("carries an elapsed origin for exactly the two states that count", () => {
    const at = "2026-07-01T00:00:00Z";
    expect(statusOf(makeSession({}), makeAgentState({ crashed: true }), idle).since).toBeUndefined();
    expect(statusOf(makeSession({}), makeAgentState({ awaitingSince: at }), idle).since).toBe(at);
    expect(statusOf(makeSession({}), makeAgentState({ workingSince: at }), idle).since).toBe(at);
    expect(statusOf(makeSession({}), makeAgentState({ done: true }), idle).since).toBeUndefined();
    expect(statusOf(makeSession({}), makeAgentState(), idle).since).toBeUndefined();
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
