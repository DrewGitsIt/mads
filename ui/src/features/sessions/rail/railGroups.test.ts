import { describe, expect, it } from "vitest";
import {
  dotState,
  filterBucketOf,
  lastReplyKey,
  railRankOf,
  mostUrgentBucket,
  statusOf,
  totalOpenTasks,
  WORKING_STALE_AFTER_MS,
} from "./railGroups";
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
  // Every fixture below stamps its state at TURN_START. Reading them "now"
  // would make each one older than WORKING_STALE_AFTER_MS, which is the point
  // of the stale rule — so the live-state cases pass a clock that sits inside
  // the turn, and the stale case is the one that moves it.
  const TURN_START = "2026-07-01T00:00:00Z";
  const DURING = Date.parse(TURN_START) + 60_000;

  it("says what an otherwise idle session is waiting on — calm, not Idle", () => {
    const session = makeSession({ activity: "active" });
    expect(statusOf(session, makeAgentState({ background: 1 }), idle, DURING)).toMatchObject({
      kind: "idle",
      tone: "idle",
      label: "Waiting on 1 process",
    });
    expect(statusOf(session, makeAgentState({ background: 2 }), idle, DURING).label).toBe("Waiting on 2 processes");
    expect(statusOf(session, makeAgentState({ background: 1, done: true }), idle, DURING)).toMatchObject({
      kind: "done",
      label: "Done · waiting on 1 process",
    });
    // Work in flight, or a subagent, still owns the line.
    const busy = makeAgentState({ background: 1, workingSince: TURN_START });
    expect(statusOf(session, busy, idle, DURING).kind).toBe("working");
    expect(statusOf(session, makeAgentState({ background: 1, subagents: 1 }), idle, DURING).kind).toBe("delegating");
    // Asleep, its processes went with its child.
    const asleep = makeSession({ activity: "inactive" });
    expect(statusOf(asleep, makeAgentState({ background: 1 }), idle, DURING).label).toBe("Idle");
  });

  it("reads delegates still running after the turn as busy — never plain Idle", () => {
    const session = makeSession({ activity: "active" });
    const state = makeAgentState({ subagents: 2, blockingSubagents: 0, subagentsSince: TURN_START });
    expect(statusOf(session, state, idle, DURING)).toMatchObject({
      kind: "delegating",
      tone: "working",
      dot: "work",
      label: "2 subagents running",
      since: TURN_START,
    });
    expect(statusOf(session, { ...state, done: true }, idle, DURING).label).toBe("Done · 2 subagents running");
    // A turn in flight still owns the line.
    expect(statusOf(session, { ...state, workingSince: TURN_START }, idle, DURING).kind).toBe("working");
    // Asleep, nothing can be running in it.
    expect(statusOf(makeSession({ activity: "inactive" }), state, idle, DURING).kind).toBe("idle");
  });

  it("reads a crash first, naming its blocked work when there is any", () => {
    const state = makeAgentState({ crashed: true, awaitingTool: "Bash", workingSince: "2026-07-01T00:00:00Z", done: true });
    expect(statusOf(makeSession({}), state, idle, DURING)).toMatchObject({
      kind: "crashed",
      tone: "bad",
      dot: "bad",
      label: "Crashed · reopen to restart",
    });
    expect(statusOf(makeSession({}), state, makeBreakdown({ blocked: 2 }), DURING).label).toBe(
      "Crashed · 2 blocked tasks",
    );
    expect(statusOf(makeSession({}), state, makeBreakdown({ blocked: 1 }), DURING).label).toBe(
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
    expect(statusOf(makeSession({}), state, idle, DURING)).toEqual({
      kind: "needs",
      tone: "needs",
      dot: "attn",
      label: "Needs approval · Bash",
      since: "2026-07-01T00:00:00Z",
    });
  });

  it("names no tool when the request didn't carry one", () => {
    const state = makeAgentState({ awaitingSince: "2026-07-01T00:00:00Z" });
    expect(statusOf(makeSession({}), state, idle).label).toBe("Needs approval");
  });

  it("spins while a turn is in flight, and counts subagents when it has them", () => {
    const working = makeAgentState({ workingSince: "2026-07-01T00:00:00Z", done: true });
    expect(statusOf(makeSession({}), working, idle, DURING)).toEqual({
      kind: "working",
      tone: "working",
      dot: "work",
      // The generic clause is now only the fallback: a turn whose first label
      // hasn't reached this browser yet.
      label: "Working…",
      since: "2026-07-01T00:00:00Z",
    });
    expect(statusOf(makeSession({}), { ...working, subagents: 3 }, idle, DURING).label).toBe(
      "3 subagents running",
    );
    expect(statusOf(makeSession({}), { ...working, subagents: 1 }, idle, DURING).label).toBe(
      "1 subagent running",
    );
  });

  it("holds 'Done · your turn' for a live session whose turn ended", () => {
    const state = makeAgentState({ done: true });
    expect(statusOf(makeSession({ activity: "active" }), state, idle)).toEqual({
      kind: "done",
      tone: "done",
      dot: "on",
      label: "Done · your turn",
    });
  });

  // "Ready" and "Sleeping" were one fact in two words — whether a provider
  // child happened to be alive. Nothing the user does depends on it (a
  // session with no child wakes on the first send), so both are "Idle" and
  // the card's one status line goes back to naming what the session is doing.
  it("is 'Idle' for a live session that has said nothing", () => {
    expect(statusOf(makeSession({ activity: "active" }), makeAgentState(), idle)).toEqual({
      kind: "idle",
      tone: "idle",
      dot: "on",
      label: "Idle",
    });
  });

  it("is the same 'Idle' for an inactive session, even one whose turn once ended", () => {
    const sleeping = makeSession({ activity: "inactive" });
    expect(statusOf(sleeping, makeAgentState({ done: true }), idle)).toEqual({
      kind: "idle",
      tone: "idle",
      // The dot still knows — the filter chips and the palette's attention
      // badge read it. Only the words stopped saying so.
      dot: "off",
      label: "Idle",
    });
  });

  it("names the blocked tasks, in the needs tone, for an idle session with blocked work", () => {
    const sleeping = makeSession({ activity: "inactive" });
    expect(statusOf(sleeping, makeAgentState(), makeBreakdown({ blocked: 1 }))).toEqual({
      kind: "idle",
      tone: "needs",
      dot: "attn",
      label: "Idle · 1 blocked task",
    });
    expect(statusOf(sleeping, makeAgentState(), makeBreakdown({ blocked: 3 })).label).toBe(
      "Idle · 3 blocked tasks",
    );
    // Running/ready work is calm progress: no amber, no count.
    const calm = statusOf(sleeping, makeAgentState(), makeBreakdown({ running: 2, ready: 1 }));
    expect(calm.tone).toBe("idle");
    expect(calm.label).toBe("Idle");
  });

  it("leaves dotState() — which the palette's attention badge counts — agreeing with the label", () => {
    const sleeping = makeSession({ activity: "inactive" });
    expect(dotState(sleeping, makeBreakdown({ blocked: 2 }))).toBe("attn");
    expect(dotState(sleeping, makeBreakdown({}))).toBe("off");
    expect(dotState(makeSession({ activity: "active" }), makeBreakdown({ blocked: 2 }))).toBe("on");
    // An awake session's blocked work is not announced on the status line.
    expect(
      statusOf(makeSession({ activity: "active" }), makeAgentState(), makeBreakdown({ blocked: 2 })).label,
    ).toBe("Idle");
  });

  it("says what the agent is actually doing, from the server-built clause", () => {
    const at = "2026-07-01T00:00:00Z";
    // The same string the open conversation's working-indicator ticker shows
    // for the same call — one server-side enrichment feeds both.
    for (const label of ["Running bash · make check", "Thinking…", "Reading · railGroups.ts"]) {
      expect(
        statusOf(makeSession({}), makeAgentState({ workingSince: at, label }), idle, DURING),
      ).toMatchObject({
        kind: "working",
        label,
        since: at,
      });
    }
  });

  it("still rolls a blocking sidechain up as a count, ahead of the main agent's own clause", () => {
    // The runner deliberately skips parent-tagged events when it builds the
    // label, so a busy subagent can't churn it — and while the main agent is
    // waiting on one it has nothing of its own to report, so the count is the
    // more useful line.
    const state = makeAgentState({
      workingSince: "2026-07-01T00:00:00Z",
      label: "Delegating · review the diff",
      subagents: 2,
      blockingSubagents: 2,
    });
    expect(statusOf(makeSession({}), state, idle, DURING).label).toBe("2 subagents running");
  });

  it("lets the main agent keep the line while only backgrounded subagents run", () => {
    // A backgrounded Agent call doesn't block the turn: the main agent goes
    // right on working, and these outlive whole turns. Counting them here
    // pinned the card to one sentence for minutes while the label underneath
    // changed every few seconds — the card looked frozen because it was.
    const state = makeAgentState({
      workingSince: "2026-07-01T00:00:00Z",
      label: "Running bash · make check",
      subagents: 2,
      blockingSubagents: 0,
    });
    expect(statusOf(makeSession({}), state, idle, DURING).label).toBe("Running bash · make check");
  });

  it("counts every running sidechain once any of them blocks, backgrounded or not", () => {
    // The clause is about the main agent being stuck, but once it is, all the
    // sidechains are the story — reporting only the blocking ones would
    // undercount what is actually running.
    const state = makeAgentState({
      workingSince: "2026-07-01T00:00:00Z",
      label: "Delegating · review the diff",
      subagents: 3,
      blockingSubagents: 1,
    });
    expect(statusOf(makeSession({}), state, idle, DURING).label).toBe("3 subagents running");
  });

  it("assumes an unsplit count blocks, so a producer that can't tell stays loud", () => {
    const state = makeAgentState({ workingSince: "2026-07-01T00:00:00Z", label: "Thinking…", subagents: 2 });
    expect(statusOf(makeSession({}), state, idle, DURING).label).toBe("2 subagents running");
  });

  it("carries an elapsed origin for exactly the two states that count", () => {
    const at = TURN_START;
    const at_ = (state: Partial<SessionAgentState>) =>
      statusOf(makeSession({}), makeAgentState(state), idle, DURING).since;
    expect(at_({ crashed: true })).toBeUndefined();
    expect(at_({ awaitingSince: at })).toBe(at);
    expect(at_({ workingSince: at })).toBe(at);
    expect(at_({ done: true })).toBeUndefined();
    expect(at_({})).toBeUndefined();
  });

  // The 2026-10-01 rail: two cards spinning "Thinking… 23h", one of them for a
  // session whose provider child had been gone for most of that time. Nothing
  // retires agent_state but the Runner that wrote it, so a wedged agent's last
  // word stands forever — the card must not keep asserting it in the present
  // tense.
  it("stops believing a working claim once nothing has been heard for the bound", () => {
    const state = makeAgentState({ workingSince: TURN_START, label: "Thinking…" });
    const stale = Date.parse(TURN_START) + WORKING_STALE_AFTER_MS;
    expect(statusOf(makeSession({ activity: "active" }), state, idle, stale)).toEqual({
      kind: "stalled",
      tone: "needs",
      dot: "attn",
      label: "Stalled · no agent activity",
      // Nothing heard since the turn began, so the silence counts from there.
      since: TURN_START,
    });
  });

  // 2026-10-14: a laptop slept mid-turn; on waking the agent picked up where
  // it left off, but the card read "Stalled" because the TURN was old.
  it("measures silence, not turn length: a long turn that keeps reporting stays working", () => {
    const now = Date.parse(TURN_START) + 3 * WORKING_STALE_AFTER_MS;
    const state = makeAgentState({
      workingSince: TURN_START,
      lastHeardAt: new Date(now - 10 * 60_000).toISOString(),
      label: "Running bash · make check",
    });
    expect(statusOf(makeSession({}), state, idle, now)).toMatchObject({
      kind: "working",
      label: "Running bash · make check",
      // The working counter still spans the whole turn.
      since: TURN_START,
    });
  });

  it("calls a long turn stalled once it has gone silent, counting the silence", () => {
    const lastHeardAt = new Date(Date.parse(TURN_START) + 60 * 60_000).toISOString();
    const now = Date.parse(lastHeardAt) + WORKING_STALE_AFTER_MS;
    const state = makeAgentState({ workingSince: TURN_START, lastHeardAt, label: "Thinking…" });
    expect(statusOf(makeSession({}), state, idle, now)).toMatchObject({
      kind: "stalled",
      since: lastHeardAt,
    });
  });

  it("lets a turn run well past 30 minutes before suspecting it", () => {
    const state = makeAgentState({ workingSince: TURN_START, label: "Thinking…" });
    expect(statusOf(makeSession({}), state, idle, Date.parse(TURN_START) + 90 * 60_000).kind).toBe("working");
  });

  it("still believes a claim right up to the bound", () => {
    const state = makeAgentState({ workingSince: TURN_START, label: "Thinking…" });
    const justInside = Date.parse(TURN_START) + WORKING_STALE_AFTER_MS - 1;
    expect(statusOf(makeSession({}), state, idle, justInside).kind).toBe("working");
  });

  it("never calls a claim stale on an unparseable stamp", () => {
    // A bad value is not evidence of a dead agent; guessing from one would
    // turn a clock-skewed record into a false alarm.
    const state = makeAgentState({ workingSince: "not a date", label: "Thinking…" });
    expect(statusOf(makeSession({}), state, idle, Date.now()).kind).toBe("working");
  });

  it("keeps a crash and a parked approval ahead of stalled", () => {
    const old = { workingSince: TURN_START };
    const stale = Date.parse(TURN_START) + WORKING_STALE_AFTER_MS;
    expect(statusOf(makeSession({}), makeAgentState({ ...old, crashed: true }), idle, stale).kind).toBe(
      "crashed",
    );
    expect(
      statusOf(makeSession({}), makeAgentState({ ...old, awaitingSince: TURN_START }), idle, stale).kind,
    ).toBe("needs");
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

  const TURN_START = "2026-07-01T00:00:00Z";
  const DURING = Date.parse(TURN_START) + 60_000;

  function bucket(
    session: Session,
    state: Partial<SessionAgentState>,
    breakdown = idle,
    now = DURING,
  ) {
    return filterBucketOf(statusOf(session, makeAgentState(state), breakdown, now));
  }

  it("files a session busy only with its delegates under Working", () => {
    expect(bucket(live, { subagents: 1, blockingSubagents: 0 })).toBe("working");
  });

  it("counts a crash and a parked approval as 'needs you'", () => {
    expect(bucket(live, { crashed: true })).toBe("needs");
    expect(bucket(live, { awaitingSince: "2026-07-01T00:00:00Z" })).toBe("needs");
  });

  it("counts an in-flight turn as 'working'", () => {
    expect(bucket(live, { workingSince: "2026-07-01T00:00:00Z" })).toBe("working");
  });

  it("counts a stalled turn as 'needs you' — a wedged agent will not recover alone", () => {
    expect(
      bucket(live, { workingSince: TURN_START }, idle, Date.parse(TURN_START) + WORKING_STALE_AFTER_MS),
    ).toBe("needs");
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

describe("lastReplyKey", () => {
  it("is last_turn_at — the only field that means the agent answered", () => {
    const session = makeSession({
      created_at: "2026-06-01T00:00:00Z",
      // A rename bumped this seconds ago; nothing replied.
      updated_at: "2026-07-02T11:59:00Z",
      last_turn_at: "2026-07-01T09:00:00Z",
    });
    expect(lastReplyKey(session)).toBe("2026-07-01T09:00:00Z");
  });

  it("falls back to created_at for a session that has never replied", () => {
    const session = makeSession({
      created_at: "2026-06-01T00:00:00Z",
      updated_at: "2026-07-02T11:59:00Z",
    });
    // Immutable, so no record write can move this card either.
    expect(lastReplyKey(session)).toBe("2026-06-01T00:00:00Z");
  });
});

describe("railRankOf", () => {
  it("is the user's rank when they have moved the card", () => {
    expect(railRankOf(makeSession({ rail_rank: 42.5, created_at: "2026-07-01T00:00:00Z" }))).toBe(42.5);
  });

  it("is the start time, in the same unit, until then", () => {
    const session = makeSession({ created_at: "2026-07-01T00:00:00Z", last_turn_at: "2026-07-03T00:00:00Z" });
    // Not the last reply: a turn ending must not move a card.
    expect(railRankOf(session)).toBe(Date.parse("2026-07-01T00:00:00Z"));
  });
});
