import { describe, expect, it } from "vitest";
import { dotState, filterBucketOf, isEarlier, mostUrgentBucket, statusOf, totalOpenTasks } from "./railGroups";
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

describe("filterBucketOf", () => {
  const idle = makeBreakdown({});
  const live = makeSession({ activity: "active" });
  const sleeping = makeSession({ activity: "inactive" });

  function bucket(session: Session, state: Partial<SessionAgentState>, breakdown = idle) {
    return filterBucketOf(statusOf(session, makeAgentState(state), breakdown));
  }

  it("counts a crash and a parked approval as 'needs you'", () => {
    expect(bucket(live, { crashed: true })).toBe("needs");
    expect(bucket(live, { awaitingSince: "2026-07-01T00:00:00Z" })).toBe("needs");
  });

  it("counts an in-flight turn as 'working'", () => {
    expect(bucket(live, { workingSince: "2026-07-01T00:00:00Z" })).toBe("working");
  });

  it("counts ready, done and sleeping as 'idle' — the turn they report is over", () => {
    expect(bucket(live, {})).toBe("idle");
    expect(bucket(live, { done: true })).toBe("idle");
    expect(bucket(sleeping, {})).toBe("idle");
  });

  it("counts a quiet session carrying blocked work as 'needs you' — the amber puck", () => {
    expect(bucket(sleeping, {}, makeBreakdown({ blocked: 1 }))).toBe("needs");
    // Running/ready work is calm progress, not something needing eyes.
    expect(bucket(sleeping, {}, makeBreakdown({ running: 2, ready: 3 }))).toBe("idle");
  });
});

describe("mostUrgentBucket", () => {
  it("is 'idle' for an empty set", () => {
    expect(mostUrgentBucket([])).toBe("idle");
  });

  it("puts needs ahead of working ahead of idle", () => {
    expect(mostUrgentBucket(["idle", "working"])).toBe("working");
    expect(mostUrgentBucket(["working", "needs", "idle"])).toBe("needs");
    expect(mostUrgentBucket(["idle", "idle"])).toBe("idle");
  });
});

describe("isEarlier", () => {
  const now = Date.parse("2026-07-02T12:00:00Z");

  it("is false inside the last day", () => {
    expect(isEarlier(makeSession({ updated_at: "2026-07-02T11:00:00Z" }), now)).toBe(false);
  });

  it("is true once last activity is a full day cold", () => {
    expect(isEarlier(makeSession({ updated_at: "2026-07-01T11:00:00Z" }), now)).toBe(true);
    expect(isEarlier(makeSession({ updated_at: "2026-07-01T12:00:00Z" }), now)).toBe(true);
  });

  it("is false for an unparseable stamp rather than folding the card away", () => {
    expect(isEarlier(makeSession({ updated_at: "not a date" }), now)).toBe(false);
  });
});
