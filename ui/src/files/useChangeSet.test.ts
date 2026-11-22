import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { abortError } from "../router/client";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { ChangeSet } from "../router/types";
import { getGitUpdating } from "../router/gitUpdating";
import {
  CHANGESET_COLD_RETRY_MS,
  CHANGESET_REFRESH_DEBOUNCE_MS,
  getChangeSetSnapshot,
  refetchChangeSet,
  useChangeSet,
} from "./useChangeSet";

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

  it("marks the set updating on its triggers without reading, and refetches once the Router says it changed", async () => {
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
    expect(c.changeSetCalls.length).toBe(before);
    expect(getGitUpdating(SID)).toBe("repos changeset counts");
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["changeset"]));
    await settle();
    expect(c.changeSetCalls.length).toBe(before + 1);
    expect(getGitUpdating(SID)).toBe("repos counts");
  });

  it("doesn't refetch for an unchanged recompute's announcement, but clears its mark", async () => {
    const c = new FakeRouterClient();
    renderHook(() => useChangeSet(c, SID));
    await settle();
    const before = c.changeSetCalls.length;
    act(() => emit("file.changed"));
    expect(getGitUpdating(SID)).toBe("changeset counts");
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["changeset"], { changed: [] }));
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["counts"], { changed: [] }));
    await settle(CHANGESET_REFRESH_DEBOUNCE_MS + 50);
    expect(c.changeSetCalls.length).toBe(before);
    expect(getGitUpdating(SID)).toBe("");
  });

  it("marks on a turn's end but not on each working state", async () => {
    const c = new FakeRouterClient();
    renderHook(() => useChangeSet(c, SID));
    await settle();
    act(() => {
      emit("session.agent_state", { state: "working", label: "Running bash · ls", since: "t" });
      emit("session.agent_state", { state: "working", label: "Thinking…", since: "t" });
    });
    expect(getGitUpdating(SID)).toBe("");
    act(() => emit("session.agent_state", { state: "idle", since: "t" }));
    expect(getGitUpdating(SID)).toBe("changeset counts");
  });

  it("a GET saying updating marks it; one saying not updating clears it", async () => {
    const c = new FakeRouterClient();
    const cs = await c.getChangeSet(SID);
    c.changeSets.set(SID, { ...cs, updating: true });
    renderHook(() => useChangeSet(c, SID));
    await settle();
    expect(getGitUpdating(SID)).toBe("changeset");
    c.changeSets.set(SID, { ...cs, updating: false });
    act(() => refetchChangeSet(SID, "start"));
    await settle();
    expect(getGitUpdating(SID)).toBe("");
  });

  it("stays loading on a cold answer (never computed) until the announced set arrives", async () => {
    const c = new FakeRouterClient();
    const cs = await c.getChangeSet(SID);
    c.changeSets.set(SID, { ...cs, files: [], computed_at: "", updating: true });
    const { result } = renderHook(() => useChangeSet(c, SID));
    await settle();
    expect(result.current.loading).toBe(true);
    expect(result.current.changeSet).toBeNull();
    c.changeSets.set(SID, cs);
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["changeset"]));
    await settle();
    expect(result.current.loading).toBe(false);
    expect(result.current.changeSet?.files.length).toBe(cs.files.length);
  });

  it("reads a cold answer again later if no announcement comes", async () => {
    const c = new FakeRouterClient();
    const cs = await c.getChangeSet(SID);
    c.changeSets.set(SID, { ...cs, computed_at: "", updating: true });
    const { result } = renderHook(() => useChangeSet(c, SID));
    await settle();
    expect(result.current.loading).toBe(true);
    c.changeSets.set(SID, cs);
    await settle(CHANGESET_COLD_RETRY_MS);
    expect(result.current.loading).toBe(false);
  });

  it("against a Router without updating (an older peer), refetches on its triggers, debounced, and marks nothing", async () => {
    const c = new FakeRouterClient();
    const legacy: ChangeSet = await c.getChangeSet(SID);
    delete legacy.updating;
    c.changeSetOutcome = async () => structuredClone(legacy);
    c.sessionReposLegacy.add(SID);
    renderHook(() => useChangeSet(c, SID));
    await settle();
    const before = c.changeSetCalls.length;
    act(() => {
      emit("file.changed");
      emit("file.changed");
      emit("session.agent_state", { state: "working", label: "x", since: "t" });
      emit("commit.created");
    });
    await settle(CHANGESET_REFRESH_DEBOUNCE_MS + 50);
    expect(c.changeSetCalls.length).toBe(before + 1);
    expect(getGitUpdating(SID)).toBe("");
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

  it("gives a reader arriving before an aborted fetch settles a fresh read of its own", async () => {
    const c = new FakeRouterClient();
    const fresh = await new FakeRouterClient().getChangeSet(SID);
    // The first read settles late after its abort (a slow socket teardown).
    let settleAborted!: () => void;
    let calls = 0;
    vi.spyOn(c, "getChangeSet").mockImplementation((_id, _vs, opts) => {
      calls++;
      if (calls > 1) return Promise.resolve(structuredClone(fresh));
      return new Promise((_, reject) => {
        opts?.signal?.addEventListener("abort", () => {
          settleAborted = () => reject(abortError());
        });
      });
    });
    const first = renderHook(() => useChangeSet(c, SID));
    await settle();
    expect(calls).toBe(1);
    first.unmount();
    const second = renderHook(() => useChangeSet(c, SID));
    await settle();
    // Still single-flight: nothing beside the aborted read.
    expect(calls).toBe(1);
    await act(async () => settleAborted());
    await settle();
    expect(calls).toBe(2);
    expect(second.result.current.loading).toBe(false);
    expect(second.result.current.error).toBeUndefined();
    expect(second.result.current.changeSet?.files.length).toBe(fresh.files.length);
  });
});
