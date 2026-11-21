import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { ChangeSet } from "../router/types";
import { CHANGESET_REFRESH_DEBOUNCE_MS, getChangeSetSnapshot, refetchChangeSet, useChangeSet } from "./useChangeSet";

const SID = "session-files-clean";

let nextSeq = 1;
function emit(type: string, payload: unknown = {}, sessionId = SID): void {
  const data = JSON.stringify({ seq: nextSeq++, type, actor: "system", session_id: sessionId, payload });
  for (const s of FakeEventSource.instances) if (!s.closed && s.url.includes(type)) s.emit(type, data);
}

async function settle(ms = 0): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

/** Holds every getChangeSet until the test answers it. */
function held(c: FakeRouterClient) {
  const pending: { resolve: (cs: ChangeSet) => void; reject: (e: Error) => void }[] = [];
  c.changeSetOutcome = () => new Promise<ChangeSet>((resolve, reject) => pending.push({ resolve, reject }));
  return pending;
}

describe("useChangeSet's shared store", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("makes one request for two readers (the rail and the tab) and hands both one snapshot", async () => {
    const c = new FakeRouterClient();
    const { result } = renderHook(() => [useChangeSet(c, SID), useChangeSet(c, SID)] as const);
    expect(result.current[0].loading).toBe(true);
    await settle();
    expect(c.changeSetCalls).toEqual([{ sessionId: SID, vs: "start" }]);
    expect(result.current[0].loading).toBe(false);
    expect(result.current[0].changeSet).not.toBeNull();
    expect(result.current[1].changeSet).toBe(result.current[0].changeSet);
  });

  it("coalesces a burst of triggers from both readers into one refetch", async () => {
    const c = new FakeRouterClient();
    renderHook(() => [useChangeSet(c, SID), useChangeSet(c, SID)] as const);
    await settle();
    const before = c.changeSetCalls.length;
    act(() => {
      emit("file.changed");
      emit("file.changed");
      emit("commit.created");
    });
    await settle(CHANGESET_REFRESH_DEBOUNCE_MS + 50);
    expect(c.changeSetCalls.length).toBe(before + 1);
  });

  it("refetches on a turn's end but not on each working state", async () => {
    const c = new FakeRouterClient();
    renderHook(() => useChangeSet(c, SID));
    await settle();
    const before = c.changeSetCalls.length;
    act(() => {
      emit("session.agent_state", { state: "working", label: "Running bash · ls", since: "t" });
      emit("session.agent_state", { state: "working", label: "Thinking…", since: "t" });
    });
    await settle(CHANGESET_REFRESH_DEBOUNCE_MS + 50);
    expect(c.changeSetCalls.length).toBe(before);
    act(() => emit("session.agent_state", { state: "idle", since: "t" }));
    await settle(CHANGESET_REFRESH_DEBOUNCE_MS + 50);
    expect(c.changeSetCalls.length).toBe(before + 1);
  });

  it("keeps one request in flight while triggers land, then reads once more", async () => {
    const c = new FakeRouterClient();
    const pending = held(c);
    const { result } = renderHook(() => useChangeSet(c, SID));
    await settle();
    expect(pending).toHaveLength(1);
    act(() => {
      result.current.refetch();
      result.current.refetch();
    });
    await settle();
    expect(pending).toHaveLength(1);
    await act(async () => pending[0].resolve({ ...(await new FakeRouterClient().getChangeSet(SID)) }));
    await settle();
    expect(pending).toHaveLength(2);
  });

  it("keeps the snapshot across a remount and revalidates behind it", async () => {
    const c = new FakeRouterClient();
    const first = renderHook(() => useChangeSet(c, SID));
    await settle();
    const kept = first.result.current.changeSet;
    first.unmount();
    const second = renderHook(() => useChangeSet(c, SID));
    expect(second.result.current.changeSet).toBe(kept);
    expect(second.result.current.loading).toBe(false);
    await settle();
    expect(c.changeSetCalls).toHaveLength(2);
  });

  it("aborts the request when the last reader leaves and surfaces no error", async () => {
    const c = new FakeRouterClient();
    held(c);
    const a = renderHook(() => useChangeSet(c, SID));
    const b = renderHook(() => useChangeSet(c, SID));
    await settle();
    const signal = c.changeSetSignals[0]!;
    a.unmount();
    expect(signal.aborted).toBe(false);
    b.unmount();
    expect(signal.aborted).toBe(true);
    await settle();
    expect(getChangeSetSnapshot(c, SID, "start").error).toBeUndefined();
    // A refetch with no reader does nothing.
    refetchChangeSet(SID, "start");
    await settle();
    expect(c.changeSetCalls).toHaveLength(1);
  });

  it("still reports a real failure", async () => {
    const c = new FakeRouterClient();
    c.changeSetOutcome = () => Promise.reject(new Error("router unreachable"));
    const { result } = renderHook(() => useChangeSet(c, SID));
    await settle();
    expect(result.current.error).toBe("router unreachable");
    expect(result.current.loading).toBe(false);
  });

  it("doesn't show one session's files for another", async () => {
    const c = new FakeRouterClient();
    const { result, rerender } = renderHook(({ id }) => useChangeSet(c, id), { initialProps: { id: SID } });
    await settle();
    expect(result.current.changeSet).not.toBeNull();
    c.changeSetOutcome = () => new Promise(() => {});
    rerender({ id: "session-other" });
    expect(result.current.changeSet).toBeNull();
    expect(result.current.loading).toBe(true);
  });
});
