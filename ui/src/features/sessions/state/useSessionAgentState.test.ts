import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionAgentState } from "./useSessionAgentState";
import { FakeEventSource } from "../../../router/testFakeEventSource";

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
    emit("session.turn_ended", "s1");
    emit("session.crashed", "s2", { reason: "exit status 1" });

    await waitFor(() => {
      expect(result.current.s1?.done).toBe(true);
      expect(result.current.s2?.crashed).toBe(true);
    });
    expect(FakeEventSource.instances.filter((i) => !i.closed)).toHaveLength(1);
  });

  it("holds an unresolved permission park with its tool, and drops it on resolve", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.permission_requested", "s1", { request_id: "r1", tool: "Bash" }, "2026-09-28T10:00:00Z");
    await waitFor(() => expect(result.current.s1?.awaitingTool).toBe("Bash"));
    expect(result.current.s1?.awaitingSince).toBe("2026-09-28T10:00:00Z");

    emit("session.permission_resolved", "s1", { request_id: "r1", status: "allowed" });
    // Resolved either way, the turn carries on.
    await waitFor(() => expect(result.current.s1?.awaitingSince).toBeUndefined());
    expect(result.current.s1?.workingSince).toBe("2026-09-28T10:00:00Z");
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
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t1" });
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t2" });
    emit("session.subagent_task", "s1", { phase: "progress", task_id: "t1", tool_uses: 3 });
    await waitFor(() => expect(result.current.s1?.subagents).toBe(2));

    emit("session.subagent_task", "s1", { phase: "notification", task_id: "t1", status: "completed" });
    await waitFor(() => expect(result.current.s1?.subagents).toBe(1));
  });

  it("marks a turn in flight from the first in-turn signal and ends it on turn_ended", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.subagent_task", "s1", { phase: "started", task_id: "t1" }, "2026-09-28T10:00:00Z");
    emit("session.subagent_task", "s1", { phase: "progress", task_id: "t1" }, "2026-09-28T10:00:09Z");
    // The counter's origin is when the turn went in flight, not the latest tick.
    await waitFor(() => expect(result.current.s1?.workingSince).toBe("2026-09-28T10:00:00Z"));

    emit("session.turn_ended", "s1", { delta: "…" });
    await waitFor(() => expect(result.current.s1).toEqual({ subagents: 0, done: true }));
  });

  it("keeps 'done' sticky across unrelated sessions' traffic", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.turn_ended", "s1");
    await waitFor(() => expect(result.current.s1?.done).toBe(true));

    emit("session.subagent_task", "s2", { phase: "started", task_id: "t1" });
    await waitFor(() => expect(result.current.s2?.workingSince).toBeTruthy());
    expect(result.current.s1?.done).toBe(true);
  });

  it("retires 'done' when the next turn starts", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.turn_ended", "s1");
    await waitFor(() => expect(result.current.s1?.done).toBe(true));

    emit("session.subagent_task", "s1", { phase: "started", task_id: "t1" });
    await waitFor(() => expect(result.current.s1?.done).toBeUndefined());
  });

  it("retires 'done' when the session is opened", async () => {
    const { result, rerender } = renderHook(({ open }: { open?: string }) => useSessionAgentState(open), {
      initialProps: {} as { open?: string },
    });
    emit("session.turn_ended", "s1");
    await waitFor(() => expect(result.current.s1?.done).toBe(true));

    rerender({ open: "s1" });
    await waitFor(() => expect(result.current.s1?.done).toBeUndefined());
  });

  it("never stages 'done' for the session already on screen", async () => {
    const { result } = renderHook(() => useSessionAgentState("s1"));
    emit("session.turn_ended", "s1");
    emit("session.turn_ended", "s2");
    await waitFor(() => expect(result.current.s2?.done).toBe(true));
    expect(result.current.s1).toBeUndefined();
  });

  it("clears a crash once the session is heard from again", async () => {
    const { result } = renderHook(() => useSessionAgentState());
    emit("session.crashed", "s1", { reason: "signal: killed" });
    await waitFor(() => expect(result.current.s1?.crashed).toBe(true));

    emit("session.turn_ended", "s1");
    await waitFor(() => expect(result.current.s1?.crashed).toBeUndefined());
  });

  it("drops in-flight work on a crash — nothing is running or parked any more", async () => {
    const { result } = renderHook(() => useSessionAgentState());
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
        "session.turn_ended",
        JSON.stringify({ seq: 1, timestamp: "2026-09-28T10:00:00Z", type: "session.turn_ended", actor: "agent" }),
      );
    });
    await waitFor(() => expect(result.current).toEqual({}));
  });
});
