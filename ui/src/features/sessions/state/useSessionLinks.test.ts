import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { LINK_REFETCH_COALESCE_MS, LINK_REFETCH_MAX_WAIT_MS, useSessionLinks } from "./useSessionLinks";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { router, RouterHTTPError } from "../../../router/client";
import { resetBulkLinksSupport } from "../../../router/fetchLinks";
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
    resetBulkLinksSupport();
  });

  it("reads every session's links in one bulk request", async () => {
    const link = makeLink({ id: "l1", granting_session_id: "s1", subject_session_id: "s2" });
    const fetchMock = vi
      .fn()
      .mockImplementation(() => Promise.resolve(new Response(JSON.stringify([link]), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionLinks(["s1", "s2"]));

    await waitFor(() => expect(result.current).toEqual([link]));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toMatch(/\/links\?session_ids=s1%2Cs2$/);
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
    let getLinks: MockInstance<(ids: string[]) => Promise<Link[]>>;
    let perSession: MockInstance<(id: string) => Promise<Link[]>>;
    // Only the missing-bulk-route tests may read per session.
    let perSessionAllowed = false;
    // Server-side truth: GET /links?session_ids= returns each touching link once.
    let serverLinks: Link[] = [];
    const linksFor = (ids: string[]) =>
      serverLinks.filter((l) => ids.includes(l.granting_session_id) || ids.includes(l.subject_session_id));

    beforeEach(() => {
      vi.useFakeTimers();
      seq = 0;
      serverLinks = [];
      perSessionAllowed = false;
      getLinks = vi.spyOn(router, "listLinks").mockImplementation((ids) => Promise.resolve(linksFor(ids)));
      perSession = vi.spyOn(router, "getSessionLinks").mockRejectedValue(new Error("use listLinks"));
    });

    afterEach(() => {
      if (!perSessionAllowed) expect(perSession).not.toHaveBeenCalled();
      perSession.mockRestore();
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
    const fetchedIds = () => getLinks.mock.calls.flatMap(([ids]) => ids).sort();
    // Zero-padded so the hook's sorted id order is numeric order: chunk k is s{200k}..s{200k+199}.
    const manyIds = (n: number) => Array.from({ length: n }, (_, i) => `s${String(i).padStart(3, "0")}`);

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

    it("coalesces a burst into one flush and one request for the unique ids", async () => {
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

      expect(getLinks).toHaveBeenCalledTimes(1);
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
      expect(getLinks).toHaveBeenCalledTimes(1);

      // A second event while the first batch is still out must wait for it.
      getLinks.mockImplementation(() => Promise.resolve([fresh]));
      emitLink({ granting_session_id: "s1", subject_session_id: "s2" });
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(getLinks).toHaveBeenCalledTimes(1);

      await act(async () => {
        for (const release of slow) release();
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(getLinks).toHaveBeenCalledTimes(2);
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

      expect(getLinks).toHaveBeenCalledTimes(1);
      expect(result.current).toEqual([link]);
    });

    it("falls back to per-session reads after a 404, and stays there", async () => {
      perSessionAllowed = true;
      const l12 = makeLink({ id: "l12", granting_session_id: "s1", subject_session_id: "s2" });
      serverLinks = [l12];
      getLinks.mockRejectedValue(new RouterHTTPError(404, "404 page not found"));
      perSession.mockImplementation((id) => Promise.resolve(linksFor([id])));
      const { result } = renderHook(() => useSessionLinks(["s1", "s2", "s3"]));
      await tick();
      expect(getLinks).toHaveBeenCalledTimes(1);
      expect(perSession.mock.calls.map(([id]) => id).sort()).toEqual(["s1", "s2", "s3"]);
      expect(result.current).toEqual([l12]);

      const l23 = makeLink({ id: "l23", granting_session_id: "s2", subject_session_id: "s3" });
      serverLinks = [l12, l23];
      emitLink({ granting_session_id: "s2", subject_session_id: "s3" }, "link.created");
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(getLinks).toHaveBeenCalledTimes(1);
      expect(perSession).toHaveBeenCalledTimes(5);
      expect(byId(result.current)).toEqual([l12, l23]);
    });

    it("keeps the cache on a 500 and never reads per session", async () => {
      const link = makeLink({ id: "l1" });
      serverLinks = [link];
      const { result } = await mountSettled(["s1", "s2"]);
      getLinks.mockRejectedValueOnce(new RouterHTTPError(500, "boom"));
      emitLink({ granting_session_id: "s1", subject_session_id: "s2" }, "link.deleted");
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(result.current).toEqual([link]);

      // Still bulk on the next flush.
      serverLinks = [];
      emitLink({ granting_session_id: "s1", subject_session_id: "s2" }, "link.deleted");
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(getLinks).toHaveBeenCalledTimes(2);
      expect(result.current).toEqual([]);
    });

    it("mounts 69 sessions with one request", async () => {
      const ids = manyIds(69);
      serverLinks = [makeLink({ id: "l1", granting_session_id: "s000", subject_session_id: "s068" })];
      const { result } = renderHook(() => useSessionLinks(ids));
      await tick();
      expect(getLinks).toHaveBeenCalledTimes(1);
      expect(fetchedIds()).toEqual(ids);
      expect(result.current).toEqual(serverLinks);
    });

    it("flushes a 50-event burst over ~30 sessions as one request", async () => {
      const ids = manyIds(69);
      const { result } = await mountSettled(ids);
      const types = ["link.created", "link.updated", "link.deleted"];
      for (let i = 0; i < 50; i++) {
        const g = ids[i % 30];
        const s = ids[(i + 1) % 30];
        serverLinks.push(makeLink({ id: `b${i}`, granting_session_id: g, subject_session_id: s }));
        emitLink({ granting_session_id: g, subject_session_id: s }, types[i % 3]);
      }
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(getLinks).toHaveBeenCalledTimes(1);
      expect(fetchedIds()).toEqual(ids.slice(0, 30));
      expect(byId(result.current)).toEqual(byId(serverLinks));

      // The next burst is the next flush: one more request.
      emitLink({ granting_session_id: ids[0], subject_session_id: ids[1] });
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(getLinks).toHaveBeenCalledTimes(2);
    });

    it("splits 450 sessions into 200/200/50 requests", async () => {
      renderHook(() => useSessionLinks(manyIds(450)));
      await tick();
      expect(getLinks.mock.calls.map(([ids]) => ids.length)).toEqual([200, 200, 50]);
    });

    it("keeps a failed chunk's links and replaces a fulfilled chunk's", async () => {
      const ids = manyIds(450);
      const oldA = makeLink({ id: "a", granting_session_id: "s001", subject_session_id: "s002" });
      const oldB = makeLink({ id: "b", granting_session_id: "s301", subject_session_id: "s302" });
      serverLinks = [oldA, oldB];
      const { result } = await mountSettled(ids);
      expect(byId(result.current)).toEqual([oldA, oldB]);

      const newB = makeLink({ id: "b", granting_session_id: "s301", subject_session_id: "s302", grants: ["read", "inject"] });
      serverLinks = [makeLink({ id: "a", granting_session_id: "s001", subject_session_id: "s002", grants: [] }), newB];
      getLinks.mockImplementation((chunk) =>
        chunk.includes("s000") ? Promise.reject(new Error("boom")) : Promise.resolve(linksFor(chunk)),
      );
      emitLink(undefined); // unreadable payload: full refetch
      await tick(LINK_REFETCH_COALESCE_MS);

      expect(getLinks).toHaveBeenCalledTimes(3);
      expect(byId(result.current)).toEqual([oldA, newB]);
    });

    it("keeps a link spanning two chunks once", async () => {
      const span = makeLink({ id: "span", granting_session_id: "s010", subject_session_id: "s310" });
      serverLinks = [span];
      const { result } = renderHook(() => useSessionLinks(manyIds(450)));
      await tick();
      expect(getLinks.mock.results.length).toBe(3);
      expect(result.current).toEqual([span]);
    });

    it("skips links only an id that left mid-flight brought back", async () => {
      const hook = await mountSettled(["s1", "s2", "s3"]);
      const shared = makeLink({ id: "shared", granting_session_id: "s2", subject_session_id: "s3" });
      const gone = makeLink({ id: "gone", granting_session_id: "s3", subject_session_id: "s9" });
      serverLinks = [shared, gone];
      const pending: (() => void)[] = [];
      getLinks.mockImplementation((chunk) => new Promise((resolve) => pending.push(() => resolve(linksFor(chunk)))));
      emitLink({ granting_session_id: "s2", subject_session_id: "s3" }, "link.created");
      await tick(LINK_REFETCH_COALESCE_MS);
      expect(fetchedIds()).toEqual(["s2", "s3"]);

      hook.rerender({ ids: ["s1", "s2"] });
      await act(async () => {
        for (const release of pending) release();
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(getLinks).toHaveBeenCalledTimes(1);
      expect(hook.result.current).toEqual([shared]);
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
