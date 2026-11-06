import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { FakeRouterClient } from "../../router/fixtures";
import type { Envelope, HeldUpdate } from "../../router/types";
import { useHeldUpdates } from "./useHeldUpdates";

// Capture the bus subscriber so a test can deliver envelopes without an EventSource.
const subs: { sessionId?: string; types: string[]; onEvent: (e: Envelope) => void }[] = [];
vi.mock("../../router/events", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  subscribeToEvents: (params: { sessionId?: string; types: string[] }, onEvent: (e: Envelope) => void) => {
    const sub = { ...params, onEvent };
    subs.push(sub);
    return () => subs.splice(subs.indexOf(sub), 1);
  },
}));

beforeEach(() => {
  subs.length = 0;
});

const updated = (): Envelope => ({ seq: 1, timestamp: "", type: "session.updated", session_id: "s1", actor: "data" });

function heldUpdate(id: number, over: Partial<HeldUpdate> = {}): HeldUpdate {
  return {
    id,
    link_id: "lnk_1",
    source_session_id: "src",
    source_name: "Pipeline study",
    kind: "digest",
    text: `update ${id}`,
    created_at: `2026-11-05T13:5${id}:00Z`,
    noop: true,
    ...over,
  };
}

describe("useHeldUpdates", () => {
  it("asks for nothing while the record says nothing is held", async () => {
    const client = new FakeRouterClient();
    const spy = vi.spyOn(client, "getHeldUpdates");
    renderHook(() => useHeldUpdates(client, "s1", { waiting: 0 }));
    await act(async () => {});
    expect(spy).not.toHaveBeenCalled();
    act(() => subs.forEach((s) => s.onEvent(updated())));
    expect(spy).not.toHaveBeenCalled();
  });

  it("fetches when updates are waiting, refetches on session.updated, and drops an empty answer", async () => {
    const client = new FakeRouterClient();
    client.heldUpdates.set("s1", { held: [heldUpdate(1)], discarded: [] });
    const { result } = renderHook(() => useHeldUpdates(client, "s1", { waiting: 1 }));
    await waitFor(() => expect(result.current.held.map((u) => u.id)).toEqual([1]));
    expect(subs.find((s) => s.types.includes("session.updated"))?.sessionId).toBe("s1");

    client.heldUpdates.set("s1", { held: [heldUpdate(1), heldUpdate(2)], discarded: [] });
    act(() => subs.forEach((s) => s.onEvent(updated())));
    await waitFor(() => expect(result.current.held.map((u) => u.id)).toEqual([1, 2]));

    // The send drained them.
    client.heldUpdates.delete("s1");
    act(() => subs.forEach((s) => s.onEvent(updated())));
    await waitFor(() => expect(result.current.held).toEqual([]));
  });

  it("fetches again when the record's count changes", async () => {
    const client = new FakeRouterClient();
    const { result, rerender } = renderHook(({ waiting }) => useHeldUpdates(client, "s1", { waiting }), {
      initialProps: { waiting: 0 },
    });
    client.heldUpdates.set("s1", { held: [heldUpdate(1)], discarded: [] });
    rerender({ waiting: 1 });
    await waitFor(() => expect(result.current.held).toHaveLength(1));
  });

  it("discard moves updates off the list at once; restore brings them back", async () => {
    const client = new FakeRouterClient();
    client.heldUpdates.set("s1", { held: [heldUpdate(1), heldUpdate(2)], discarded: [] });
    const discard = vi.spyOn(client, "discardHeldUpdates");
    const { result } = renderHook(() => useHeldUpdates(client, "s1", { waiting: 2 }));
    await waitFor(() => expect(result.current.held).toHaveLength(2));

    let done!: Promise<void>;
    act(() => {
      done = result.current.discard([1]);
    });
    expect(result.current.held.map((u) => u.id)).toEqual([2]);
    expect(result.current.discarded.map((u) => u.id)).toEqual([1]);
    await act(() => done);
    expect(discard).toHaveBeenCalledWith([1]);

    await act(() => result.current.restore([1]));
    await waitFor(() => expect(result.current.held.map((u) => u.id)).toEqual([1, 2]));
    expect(result.current.discarded).toEqual([]);
  });

  it("keeps listing what was discarded after the count drops to zero, for Undo", async () => {
    const client = new FakeRouterClient();
    client.heldUpdates.set("s1", { held: [heldUpdate(1)], discarded: [] });
    const { result, rerender } = renderHook(({ waiting }) => useHeldUpdates(client, "s1", { waiting }), {
      initialProps: { waiting: 1 },
    });
    await waitFor(() => expect(result.current.held).toHaveLength(1));
    await act(() => result.current.discard([1]));
    rerender({ waiting: 0 });
    act(() => subs.forEach((s) => s.onEvent(updated())));
    await waitFor(() => expect(result.current.discarded.map((u) => u.id)).toEqual([1]));
    expect(result.current.held).toEqual([]);
  });
});
