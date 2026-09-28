import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionAgentState } from "./useSessionAgentState";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { AgentState, Session } from "../../../router/types";

let seq = 0;

function emit(type: string, sessionId: string, payload?: unknown, timestamp = "2026-09-28T10:00:00Z") {
  const source = FakeEventSource.instances.filter((i) => !i.closed)[0];
  act(() => {
    source.emit(
      type,
      JSON.stringify({ seq: ++seq, timestamp, type, actor: "agent", session_id: sessionId, payload }),
    );
  });
}

/** The runner's own two edges, as they reach the browser. */
function working(sessionId: string, label: string, since: string, at = since) {
  emit("session.agent_state", sessionId, { state: "working", label, since }, at);
}

function idle(sessionId: string, at = "2026-09-28T10:01:00Z") {
  emit("session.agent_state", sessionId, { state: "idle", since: at }, at);
}

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-09-28T09:00:00Z",
    updated_at: "2026-09-28T09:00:00Z",
    ...overrides,
  };
}

const MID_TURN: AgentState = {
  state: "working",
  label: "Running bash · make check",
  since: "2026-09-28T10:00:00Z",
};

describe("useSessionAgentState", () => {
  beforeEach(() => {
    seq = 0;
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("is empty until a session says something", () => {
    const { result } = renderHook(() => useSessionAgentState());
    expect(result.current).toEqual({});
  });

  it("opens exactly one connection and keys state by session id", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    idle("s1");
    emit("session.crashed", "s2", { reason: "exit status 1" });

    await waitFor(() => {
      expect(result.current.s1?.done).toBe(true);
      expect(result.current.s2?.crashed).toBe(true);
    });
    expect(FakeEventSource.instances.filter((i) => !i.closed)).toHaveLength(1);
  });

  it("carries the live clause and the TURN's start through a label change", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    working("s1", "Working…", "2026-09-28T10:00:00Z");
    await waitFor(() => expect(result.current.s1?.label).toBe("Working…"));

    // A label transition inside the turn keeps the turn's own `since`, so the
    // card's elapsed counter spans the turn rather than resetting per tool —
    // the runner holds `since` for exactly this reason.
    working("s1", "Running bash · make check", "2026-09-28T10:00:00Z", "2026-09-28T10:00:09Z");
    await waitFor(() => expect(result.current.s1?.label).toBe("Running bash · make check"));
    expect(result.current.s1?.workingSince).toBe("2026-09-28T10:00:00Z");
  });

  it("falls back to the envelope stamp when a transition carries no since", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.agent_state", "s1", { state: "working", label: "Thinking…" }, "2026-09-28T10:00:03Z");
    await waitFor(() => expect(result.current.s1?.workingSince).toBe("2026-09-28T10:00:03Z"));
  });

  it("ignores a payload whose state is neither working nor idle", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.agent_state", "s1", { state: "pondering", since: "2026-09-28T10:00:00Z" });
    await waitFor(() => expect(result.current).toEqual({}));
  });

  it("holds an unresolved permission park with its tool, and drops it on resolve", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    working("s1", "Working…", "2026-09-28T09:59:00Z");
    emit("session.permission_requested", "s1", { request_id: "r1", tool: "Bash" }, "2026-09-28T10:00:00Z");
    await waitFor(() => expect(result.current.s1?.awaitingTool).toBe("Bash"));
    expect(result.current.s1?.awaitingSince).toBe("2026-09-28T10:00:00Z");

    emit("session.permission_resolved", "s1", { request_id: "r1", status: "allowed" });
    // Resolved either way, the turn carries on — and the turn's start is still
    // the turn's, not the park's.
    await waitFor(() => expect(result.current.s1?.awaitingSince).toBeUndefined());
    expect(result.current.s1?.workingSince).toBe("2026-09-28T09:59:00Z");
  });

  it("names the oldest park when two are outstanding", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.permission_requested", "s1", { request_id: "r1", tool: "Bash" }, "2026-09-28T10:00:00Z");
    emit("session.permission_requested", "s1", { request_id: "r2", tool: "Write" }, "2026-09-28T10:00:05Z");
    await waitFor(() => expect(result.current.s1?.awaitingTool).toBe("Bash"));

    emit("session.permission_resolved", "s1", { request_id: "r1", status: "denied" });
    await waitFor(() => expect(result.current.s1?.awaitingTool).toBe("Write"));
  });

  it("counts running subagents, retiring one only on its final notification", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    working("s1", "Delegating · review the diff", "2026-09-28T10:00:00Z");
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t1" });
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t2" });
    emit("session.subagent_task", "s1", { phase: "progress", task_id: "t1", tool_uses: 3 });
    await waitFor(() => expect(result.current.s1?.subagents).toBe(2));

    emit("session.subagent_task", "s1", { phase: "notification", task_id: "t1", status: "completed" });
    await waitFor(() => expect(result.current.s1?.subagents).toBe(1));
  });

  it("reports the turn in flight from agent_state alone — an in-turn signal is not a turn", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    // Subagent traffic used to be what made a card read "working". It no
    // longer is: the authoritative signal has its own event now, and a count
    // on its own was never news (the same rule as useOpenTaskCounts').
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t1" });
    emit("session.permission_resolved", "s1", { request_id: "r1" });
    await waitFor(() => expect(result.current).toEqual({}));

    working("s1", "Working…", "2026-09-28T10:00:00Z");
    await waitFor(() => expect(result.current.s1?.workingSince).toBe("2026-09-28T10:00:00Z"));
    expect(result.current.s1?.subagents).toBe(1);
  });

  it("ends the turn on idle, with no session.turn_ended needed — the no-delta gap", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    working("s1", "Running bash · make check", "2026-09-28T10:00:00Z");
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t1" });
    await waitFor(() => expect(result.current.s1?.subagents).toBe(1));

    // No turn_ended at all: a turn that produced no digest delta never appends
    // one, which is exactly the "Done · your turn" gap phase 0 left open.
    idle("s1");
    await waitFor(() => expect(result.current.s1).toEqual({ subagents: 0, done: true }));
  });

  it("keeps 'done' sticky across unrelated sessions' traffic", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    idle("s1");
    await waitFor(() => expect(result.current.s1?.done).toBe(true));

    working("s2", "Working…", "2026-09-28T10:02:00Z");
    await waitFor(() => expect(result.current.s2?.workingSince).toBeTruthy());
    expect(result.current.s1?.done).toBe(true);
  });

  it("retires 'done' when the next turn starts", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    idle("s1");
    await waitFor(() => expect(result.current.s1?.done).toBe(true));

    working("s1", "Working…", "2026-09-28T10:02:00Z");
    await waitFor(() => expect(result.current.s1?.done).toBeUndefined());
  });

  it("retires 'done' when the session is opened", async () => {
    const { result, rerender } = renderHook(
      ({ open }: { open?: string }) => useSessionAgentState([], open),
      { initialProps: {} as { open?: string } },
    );
    idle("s1");
    await waitFor(() => expect(result.current.s1?.done).toBe(true));

    rerender({ open: "s1" });
    await waitFor(() => expect(result.current.s1?.done).toBeUndefined());
  });

  it("never stages 'done' for the session already on screen", async () => {
    const { result } = renderHook(() => useSessionAgentState([], "s1"));
    idle("s1");
    idle("s2");
    await waitFor(() => expect(result.current.s2?.done).toBe(true));
    expect(result.current.s1).toBeUndefined();
  });

  it("clears a crash once the session is heard from again", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.crashed", "s1", { reason: "signal: killed" });
    await waitFor(() => expect(result.current.s1?.crashed).toBe(true));

    working("s1", "Working…", "2026-09-28T10:02:00Z");
    await waitFor(() => expect(result.current.s1?.crashed).toBeUndefined());
  });

  it("drops in-flight work on a crash — nothing is running or parked any more", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    working("s1", "Running bash · make check", "2026-09-28T10:00:00Z");
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t1" });
    emit("session.permission_requested", "s1", { request_id: "r1", tool: "Bash" });
    await waitFor(() => expect(result.current.s1?.awaitingTool).toBe("Bash"));

    emit("session.crashed", "s1", { reason: "exit status 1" });
    await waitFor(() => expect(result.current.s1).toEqual({ subagents: 0, crashed: true }));
  });

  it("ignores an envelope with no session id", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "session.agent_state",
        JSON.stringify({ seq: 1, timestamp: "2026-09-28T10:00:00Z", type: "session.agent_state", actor: "agent", payload: { state: "idle", since: "2026-09-28T10:00:00Z" } }),
      );
    });
    await waitFor(() => expect(result.current).toEqual({}));
  });

  describe("the record mirror — a cold reader that missed the transition", () => {
    it("reports a live turn from the session list alone, with no event replay", async () => {
      const session = makeSession({ agent_state: MID_TURN });
      const { result } = renderHook(() => useSessionAgentState([session]));
      await waitFor(() =>
        expect(result.current.s1).toEqual({
          subagents: 0,
          workingSince: "2026-09-28T10:00:00Z",
          label: "Running bash · make check",
        }),
      );
    });

    it("reads an inactive session as idle however its last transition reads", async () => {
      // Only the live Runner clears agent_state, so a hard-killed process
      // leaves a working state on the record — a card must not spin forever.
      const session = makeSession({ activity: "inactive", agent_state: MID_TURN });
      const { result } = renderHook(() => useSessionAgentState([session]));
      await waitFor(() => expect(result.current).toEqual({}));
    });

    it("stages no sticky done from an idle record — a reply from last week isn't news", async () => {
      const session = makeSession({
        agent_state: { state: "idle", since: "2026-09-21T10:00:00Z" },
      });
      const { result } = renderHook(() => useSessionAgentState([session]));
      await waitFor(() => expect(result.current).toEqual({}));
    });

    it("lets the stream override a mirror that is now stale, in both directions", async () => {
      const session = makeSession({ agent_state: MID_TURN });
      const { result, rerender } = renderHook(
        ({ sessions }: { sessions: Session[] }) => useSessionAgentState(sessions),
        { initialProps: { sessions: [session] } },
      );
      await waitFor(() => expect(result.current.s1?.workingSince).toBeTruthy());

      // The turn ends on the stream while the list still carries the old
      // mirror: the stream is newer, so the card is done, not working.
      idle("s1");
      await waitFor(() => expect(result.current.s1).toEqual({ subagents: 0, done: true }));

      // And a refetch that re-delivers the same stale mirror doesn't undo it.
      rerender({ sessions: [{ ...session }] });
      await waitFor(() => expect(result.current.s1).toEqual({ subagents: 0, done: true }));
    });

    it("picks a newer mirror up on a refetch", async () => {
      const { result, rerender } = renderHook(
        ({ sessions }: { sessions: Session[] }) => useSessionAgentState(sessions),
        { initialProps: { sessions: [makeSession()] } },
      );
      await waitFor(() => expect(result.current).toEqual({}));

      rerender({ sessions: [makeSession({ agent_state: MID_TURN })] });
      await waitFor(() => expect(result.current.s1?.label).toBe("Running bash · make check"));
    });
  });
});
