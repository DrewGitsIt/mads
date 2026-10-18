import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { LINK_REFETCH_COALESCE_MS, LINK_REFETCH_MAX_WAIT_MS, useSessionLinks } from "./useSessionLinks";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { router } from "../../../router/client";
import { FLAGS } from "../../../featureFlags";
import type { Link } from "../../../router/types";

function makeLink(overrides: Partial<Link>): Link {
  return {
    id: "link-1",
    granting_session_id: "s1",
    subject_session_id: "s2",
    grants: ["read"],
    cadence: "per_turn",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("useSessionLinks", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    FLAGS.linkedSessions = true;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    FLAGS.linkedSessions = false;
  });

  it("fetches per session and dedupes links shared by both endpoints", async () => {
    const link = makeLink({ id: "l1", granting_session_id: "s1", subject_session_id: "s2" });
    const fetchMock = vi
      .fn()
      .mockImplementation(() => Promise.resolve(new Response(JSON.stringify([link]), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionLinks(["s1", "s2"]));

    await waitFor(() => expect(result.current).toEqual([link]));
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("returns nothing and fetches nothing when the flag is off", async () => {
    FLAGS.linkedSessions = false;
    const fetchMock = vi.fn().mockResolvedValue(new Response("[]", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionLinks(["s1"]));

    await waitFor(() => expect(result.current).toEqual([]));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("refetches on a link.created event", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("[]", { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeLink({ id: "l1" })]), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionLinks(["s1"]));
    await waitFor(() => expect(result.current).toEqual([]));

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "link.created",
        JSON.stringify({ seq: 1, type: "link.created", actor: "user", link_id: "l1" }),
      );
    });

    await waitFor(() => expect(result.current).toEqual([makeLink({ id: "l1" })]));
  });

  describe("scoped, coalesced refetch", () => {
    let seq = 0;
    let getLinks: MockInstance<(id: string) => Promise<Link[]>>;
    // Server-side truth per session: GET /sessions/{id}/links.
    let serverLinks: Link[] = [];
    const linksFor = (id: string) =>
      serverLinks.filter((l) => l.granting_session_id === id || l.subject_session_id === id);

    beforeEach(() => {
      vi.useFakeTimers();
      seq = 0;
      serverLinks = [];
      getLinks = vi.spyOn(router, "getSessionLinks").mockImplementation((id) => Promise.resolve(linksFor(id)));
    });

    afterEach(() => {
      getLinks.mockRestore();
      vi.useRealTimers();
    });

    const tick = (ms = 0) =>
      act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });

    function emitLink(payload: unknown, type = "link.updated") {
      act(() => {
        FakeEventSource.instances[0].emit(type, JSON.stringify({ seq: ++seq, type, actor: "system", payload }));
      });
    }

    const byId = (links: Link[]) => [...links].sort((a, b) => a.id.localeCompare(b.id));
    const fetchedIds = () => getLinks.mock.calls.map(([id]) => id).sort();

    async function mountSettled(ids: string[]) {
      const hook = renderHook(({ ids }) => useSessionLinks(ids), { initialProps: { ids } });
      await tick();
      getLinks.mockClear();
      return hook;
    }

    it("fetches only the two endpoints an event touches", async () => {
      await mountSettled(["s1", "s2", "s3", "s4"]);
      serverLinks = [makeLink({ id: "l1", granting_session_id: "s1", subject_session_id: "s2" })];

      emitLink({ granting_session_id: "s1", subject_session_id: "s2" }, "link.created");
      await tick(LINK_REFETCH_COALESCE_MS);

      expect(fetchedIds()).toEqual(["s1", "s2"]);
    });

    it("accepts a JSON-string payload", async () => {
      await mountSettled(["s1", "s2", "s3"]);
      emitLink(JSON.stringify({ granting_session_id: "s1", subject_session_id: "s2" }));
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(fetchedIds()).toEqual(["s1", "s2"]);
    });

    it("coalesces a burst into one flush with one fetch per unique id", async () => {
      const { result } = await mountSettled(["s1", "s2", "s3", "s4", "s5"]);
      const pairs = [
        ["s1", "s2"],
        ["s2", "s3"],
        ["s3", "s4"],
        ["s4", "s1"],
        ["s1", "s3"],
      ];
      serverLinks = pairs.map(([g, s], i) => makeLink({ id: `l${i}`, granting_session_id: g, subject_session_id: s }));
      for (const [g, s] of pairs) {
        emitLink({ granting_session_id: g, subject_session_id: s });
        await tick(LINK_REFETCH_COALESCE_MS - 10);
      }
      expect(getLinks).not.toHaveBeenCalled();

      await tick(LINK_REFETCH_COALESCE_MS);

      expect(fetchedIds()).toEqual(["s1", "s2", "s3", "s4"]);
      expect(byId(result.current)).toEqual(byId(serverLinks));
    });

    it("flushes after the max wait even if events never go quiet", async () => {
      await mountSettled(["s1", "s2"]);
      for (let t = 0; t < LINK_REFETCH_MAX_WAIT_MS; t += 50) {
        emitLink({ granting_session_id: "s1", subject_session_id: "s2" });
        await tick(50);
      }
      expect(fetchedIds()).toEqual(["s1", "s2"]);
    });

    it("never lets a slow earlier flight overwrite a newer result", async () => {
      const { result } = await mountSettled(["s1", "s2"]);
      const stale = makeLink({ id: "l1", grants: ["read"] });
      const fresh = makeLink({ id: "l1", grants: ["read", "inject"] });
      const slow: (() => void)[] = [];
      getLinks.mockImplementation(() => new Promise((resolve) => slow.push(() => resolve([stale]))));
      emitLink({ granting_session_id: "s1", subject_session_id: "s2" });
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(getLinks).toHaveBeenCalledTimes(2);

      // A second event while the first batch is still out must wait for it.
      getLinks.mockImplementation(() => Promise.resolve([fresh]));
      emitLink({ granting_session_id: "s1", subject_session_id: "s2" });
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(getLinks).toHaveBeenCalledTimes(2);

      await act(async () => {
        for (const release of slow) release();
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(getLinks).toHaveBeenCalledTimes(4);
      expect(result.current).toEqual([fresh]);
    });

    it.each([
      ["missing", undefined],
      ["unparseable", "{not json"],
      ["missing ids", { link_id: "l1" }],
    ])("falls back to a full refetch when the payload is %s", async (_label, payload) => {
      await mountSettled(["s1", "s2", "s3"]);
      emitLink(payload);
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(fetchedIds()).toEqual(["s1", "s2", "s3"]);
    });

    it("ignores events for sessions outside the set", async () => {
      await mountSettled(["s1", "s2"]);
      emitLink({ granting_session_id: "s8", subject_session_id: "s9" });
      await tick(LINK_REFETCH_MAX_WAIT_MS);
      expect(getLinks).not.toHaveBeenCalled();
    });

    it("on an ids change fetches only added ids and keeps links that still touch the set", async () => {
      const kept = makeLink({ id: "kept", granting_session_id: "s1", subject_session_id: "s2" });
      const bridge = makeLink({ id: "bridge", granting_session_id: "s1", subject_session_id: "s3" });
      const orphan = makeLink({ id: "orphan", granting_session_id: "s3", subject_session_id: "s9" });
      const added = makeLink({ id: "added", granting_session_id: "s4", subject_session_id: "s9" });
      serverLinks = [kept, bridge, orphan, added];
      const { result, rerender } = await mountSettled(["s1", "s2", "s3"]);
      expect(byId(result.current)).toEqual(byId([kept, bridge, orphan]));

      rerender({ ids: ["s1", "s2", "s4"] });
      await tick();

      expect(fetchedIds()).toEqual(["s4"]);
      expect(byId(result.current)).toEqual(byId([added, bridge, kept]));
    });

    it("keeps the cache when a fetch fails", async () => {
      const link = makeLink({ id: "l1" });
      serverLinks = [link];
      const { result } = await mountSettled(["s1", "s2"]);
      expect(result.current).toEqual([link]);

      getLinks.mockRejectedValue(new Error("boom"));
      emitLink({ granting_session_id: "s1", subject_session_id: "s2" }, "link.deleted");
      await tick(LINK_REFETCH_COALESCE_MS);

      expect(getLinks).toHaveBeenCalledTimes(2);
      expect(result.current).toEqual([link]);
    });

    it("does nothing after unmounting mid-window or mid-flight", async () => {
      const errors = vi.spyOn(console, "error").mockImplementation(() => {});
      const { unmount } = await mountSettled(["s1", "s2"]);
      emitLink({ granting_session_id: "s1", subject_session_id: "s2" });
      unmount();
      await tick(LINK_REFETCH_MAX_WAIT_MS);
      expect(getLinks).not.toHaveBeenCalled();

      const pending: (() => void)[] = [];
      getLinks.mockImplementation(() => new Promise((resolve) => pending.push(() => resolve([makeLink({ id: "late" })]))));
      const second = renderHook(() => useSessionLinks(["s1"]));
      await tick();
      const before = second.result.current;
      second.unmount();
      await act(async () => {
        for (const release of pending) release();
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(second.result.current).toBe(before);
      expect(errors).not.toHaveBeenCalled();
      errors.mockRestore();
    });
  });
});
