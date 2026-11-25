import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  eventsReady,
  getBusHealth,
  getEventsCursorForTests,
  getIncompatiblePeers,
  getSessionLive,
  resetEventsBusForTests,
  resumePeerBuses,
  subscribeToEvents,
} from "./events";
import { getMeshSnapshot, refreshPeers, resetMeshForTests, subscribeMesh } from "./mesh";
import { recordSessionHosts } from "./sessionHosts";
import { FakeEventSource } from "./testFakeEventSource";
import type { Envelope, PeerStatus, Session } from "./types";

// Mesh mode: THE CONTRACT (router/events.ts) holds per bus. A remote
// session's bus is its host's log, through /peers/{peer}/events: its own
// head read, cursor, ready barrier and incompatible state.

const remote = (id: string) => ({ id, host: { name: "pop-os", self: false, reachable: true } }) as Session;

function envelope(seq: number, type: string, sessionId: string): Envelope {
  return { seq, timestamp: new Date().toISOString(), type, actor: "user", session_id: sessionId };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

/** A fetch whose head reads are answered per log by `heads` (path → seq or status). */
function stubHeads(heads: Record<string, number | Response | (() => Promise<Response>)>, rest?: (url: URL) => Response) {
  const seen: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input);
      seen.push(url.pathname);
      const h = heads[url.pathname];
      if (h === undefined) return rest ? rest(url) : new Response("[]", { status: 200 });
      if (typeof h === "number") return new Response(JSON.stringify({ seq: h }), { status: 200 });
      if (typeof h === "function") return h();
      return h.clone();
    }),
  );
  return seen;
}

const sources = (path: string) => FakeEventSource.instances.filter((s) => new URL(s.url).pathname === path);

describe("the event bus, per device", () => {
  const cleanups: (() => void)[] = [];

  beforeEach(() => {
    resetMeshForTests();
    resetEventsBusForTests(); // the production first connect: heads are read
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    for (const c of cleanups.splice(0)) c();
    resetMeshForTests();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("each bus reads its own log's head, keeps its own cursor and resumes from it", async () => {
    let peerHead = 7;
    const seen = stubHeads({
      "/events/head": 100,
      "/peers/pop-os/events/head": async () => new Response(JSON.stringify({ seq: peerHead }), { status: 200 }),
    });
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ types: ["session.created"] }, () => {}));
    const got: number[] = [];
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, (e) => got.push(e.seq)));
    await flush();
    expect(seen).toEqual(expect.arrayContaining(["/events/head", "/peers/pop-os/events/head"]));

    const local = new URL(sources("/events")[0].url);
    const peer = new URL(sources("/peers/pop-os/events")[0].url);
    expect(local.searchParams.get("since")).toBe("100");
    expect(peer.searchParams.get("since")).toBe("7");
    expect(peer.searchParams.get("session_id")).toBe("ses_r");

    // Seqs from two logs are never compared: 8 is new on the peer's log
    // although this device's cursor is at 100.
    sources("/peers/pop-os/events")[0].emit("session.queue_changed", JSON.stringify(envelope(8, "session.queue_changed", "ses_r")));
    expect(got).toEqual([8]);
    expect(getEventsCursorForTests("ses_r")).toBe(8);
    expect(getEventsCursorForTests()).toBe(100);

    // A drop resumes that bus from its own cursor (its recheck of the head
    // finds the log where it was), and leaves the other alone.
    peerHead = 8;
    sources("/peers/pop-os/events")[0].simulateError();
    await vi.waitFor(() => expect(sources("/peers/pop-os/events")).toHaveLength(2), { timeout: 3000 });
    expect(new URL(sources("/peers/pop-os/events")[1].url).searchParams.get("since")).toBe("8");
    expect(sources("/events")).toHaveLength(1);
    expect(sources("/events")[0].closed).toBe(false);
  });

  it("eventsReady(sessionId) waits for that session's bus, not this device's", async () => {
    let answerPeer!: (r: Response) => void;
    stubHeads({
      "/events/head": 3,
      "/peers/pop-os/events/head": () => new Promise<Response>((r) => (answerPeer = r)),
    });
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ types: ["session.created"] }, () => {}));
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, () => {}));
    let localReady = false;
    let peerReady = false;
    void eventsReady().then(() => (localReady = true));
    void eventsReady("ses_r").then(() => (peerReady = true));
    await flush();
    await flush();
    expect(localReady).toBe(true);
    expect(peerReady).toBe(false);
    expect(sources("/peers/pop-os/events")).toHaveLength(0); // no connect before its head
    answerPeer(new Response(JSON.stringify({ seq: 5 }), { status: 200 }));
    await flush();
    await flush();
    expect(peerReady).toBe(true);
    expect(new URL(sources("/peers/pop-os/events")[0].url).searchParams.get("since")).toBe("5");
  });

  it("a peer whose mads has no GET /events/head marks that peer, never the window", async () => {
    stubHeads(
      { "/events/head": 1, "/peers/pop-os/events/head": new Response("404 page not found", { status: 404 }) },
      (url) =>
        url.pathname === "/peers"
          ? new Response(JSON.stringify([{ id: "per_1", name: "pop-os", mesh_url: "http://pop-os:17380", reachability: "connected", created_at: "", updated_at: "" } satisfies PeerStatus]), { status: 200 })
          : new Response("[]", { status: 200 }),
    );
    vi.spyOn(console, "error").mockImplementation(() => {});
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ types: ["session.created"] }, () => {}));
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, () => {}));
    await flush();
    await flush();
    expect(getIncompatiblePeers().has("pop-os")).toBe(true);
    expect(getBusHealth().state).not.toBe("incompatible");
    // Degraded, not stuck: the session's REST reads run, without live updates.
    let ready = false;
    void eventsReady("ses_r").then(() => (ready = true));
    await flush();
    expect(ready).toBe(true);
    expect(getSessionLive("ses_r")).toMatchObject({ state: "unavailable", peer: "pop-os" });
    expect(sources("/events")).toHaveLength(1);
    expect(sources("/peers/pop-os/events")).toHaveLength(0);
    // Where reachability shows, the peer reads as needing an update.
    await refreshPeers();
    expect(getMeshSnapshot().peers[0]).toMatchObject({ name: "pop-os", reachability: "version_mismatch" });
    // This device's bus keeps delivering.
    const got: number[] = [];
    cleanups.push(subscribeToEvents({ types: ["session.created"] }, (e) => got.push(e.seq)));
    sources("/events")[0].emit("session.created", JSON.stringify(envelope(2, "session.created", "ses_x")));
    expect(got).toEqual([2]);
  });

  it("the Router's 404 for a peer it no longer has is a failed read, not an old mads", async () => {
    stubHeads({
      "/peers/pop-os/events/head": new Response(JSON.stringify({ code: "peer_not_found", error: "no such peer" }), { status: 404 }),
    });
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, () => {}));
    await flush();
    await flush();
    expect(getIncompatiblePeers().has("pop-os")).toBe(false);
  });

  it("a subscription that moves to its host's bus re-seeds once that bus is ready", async () => {
    let answerPeer!: (r: Response) => void;
    stubHeads({
      "/events/head": 50,
      "/peers/pop-os/events/head": () => new Promise<Response>((r) => (answerPeer = r)),
    });
    const reseeds: number[] = [];
    cleanups.push(
      subscribeToEvents({ sessionId: "ses_late", types: ["session.queue_changed"], onReseed: () => reseeds.push(Date.now()) }, () => {}),
    );
    await flush();
    expect(sources("/events")).toHaveLength(1);

    recordSessionHosts([remote("ses_late")]);
    expect(sources("/events")[0].closed).toBe(true);
    await flush();
    expect(reseeds).toHaveLength(0); // not before the new bus's cursor is fixed
    answerPeer(new Response(JSON.stringify({ seq: 4 }), { status: 200 }));
    await flush();
    await flush();
    expect(reseeds).toHaveLength(1);
    expect(new URL(sources("/peers/pop-os/events")[0].url).searchParams.get("since")).toBe("4");
  });

  it("a peer's Mesh Service answering 403 not_exposed (a build without the route) is an old mads, not a retry loop", async () => {
    let reads = 0;
    stubHeads({
      "/peers/pop-os/events/head": async () => {
        reads++;
        return new Response(JSON.stringify({ code: "not_exposed", error: "this route isn't available over the mesh" }), { status: 403 });
      },
    });
    vi.spyOn(console, "error").mockImplementation(() => {});
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, () => {}));
    await flush();
    await flush();
    expect(getIncompatiblePeers().has("pop-os")).toBe(true);
    expect(getSessionLive("ses_r").state).toBe("unavailable");
    await new Promise((r) => setTimeout(r, 2500));
    expect(reads).toBe(1); // no 2 s retry loop
  });

  it("an unavailable peer rechecks its head slowly, and recovers when it answers", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let head: Response | number = new Response("404 page not found", { status: 404 });
    let reads = 0;
    stubHeads({
      "/peers/pop-os/events/head": async () => {
        reads++;
        return typeof head === "number" ? new Response(JSON.stringify({ seq: head }), { status: 200 }) : head.clone();
      },
    });
    vi.spyOn(console, "error").mockImplementation(() => {});
    recordSessionHosts([remote("ses_r")]);
    let reseeds = 0;
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"], onReseed: () => reseeds++ }, () => {}));
    await vi.advanceTimersByTimeAsync(10);
    expect(reads).toBe(1);
    await vi.advanceTimersByTimeAsync(20_000);
    expect(reads).toBe(1); // slow, not the 2 s cap
    head = 12; // updated over there
    await vi.advanceTimersByTimeAsync(11_000);
    expect(reads).toBe(2);
    expect(getIncompatiblePeers().has("pop-os")).toBe(false);
    expect(getSessionLive("ses_r")).toMatchObject({ state: "live", epoch: 1 });
    expect(reseeds).toBe(1);
    expect(new URL(sources("/peers/pop-os/events")[0].url).searchParams.get("since")).toBe("12");
    vi.useRealTimers();
  });

  it("a peer that refuses this device pauses its bus until something may let it in", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let refused = true;
    let reads = 0;
    stubHeads({
      "/peers/pop-os/events/head": async () => {
        reads++;
        return refused
          ? new Response(JSON.stringify({ code: "no_access", error: "this device may not use mads here" }), { status: 403 })
          : new Response(JSON.stringify({ seq: 30 }), { status: 200 });
      },
    });
    vi.spyOn(console, "warn").mockImplementation(() => {});
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, () => {}));
    await vi.advanceTimersByTimeAsync(10);
    expect(getSessionLive("ses_r")).toMatchObject({ state: "paused", peer: "pop-os" });
    let ready = false;
    void eventsReady("ses_r").then(() => (ready = true));
    await vi.advanceTimersByTimeAsync(120_000);
    expect(ready).toBe(true); // reads run (and say what's refused)
    expect(reads).toBe(1); // no retries: each would be audited on the host
    refused = false;
    resumePeerBuses("pop-os"); // e.g. a peer.* event after a grant change
    await vi.advanceTimersByTimeAsync(10);
    expect(reads).toBe(2);
    expect(getSessionLive("ses_r")).toMatchObject({ state: "live", epoch: 1 });
    expect(new URL(sources("/peers/pop-os/events")[0].url).searchParams.get("since")).toBe("30");
    vi.useRealTimers();
  });

  it("a peer bus backs off to a slower cap than this device's 2 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(Math, "random").mockReturnValue(0.999);
    let reads = 0;
    stubHeads({
      "/peers/pop-os/events/head": async () => {
        reads++;
        return new Response("bad gateway", { status: 502 });
      },
    });
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, () => {}));
    // Reads at about 0, 1, 3 and 7 s (waits of 1, 2 and 4 s).
    await vi.advanceTimersByTimeAsync(7500);
    const after = reads;
    expect(after).toBe(4);
    await vi.advanceTimersByTimeAsync(3000);
    expect(reads).toBe(after); // the next wait is ~16 s, past LOCAL's 2 s cap
    vi.useRealTimers();
  });

  it("one session's bus finding the peer updated clears the mark an idle sibling bus left", async () => {
    let old = true;
    let headReads = 0;
    stubHeads({
      "/peers/pop-os/events/head": async () => {
        headReads++;
        return old
          ? new Response(JSON.stringify({ code: "not_exposed" }), { status: 403 })
          : new Response(JSON.stringify({ seq: 20 }), { status: 200 });
      },
    });
    vi.spyOn(console, "error").mockImplementation(() => {});
    recordSessionHosts([remote("ses_a"), remote("ses_b")]);
    // Session A, opened while pop-os was old, marks it; then it's closed.
    const offA = subscribeToEvents({ sessionId: "ses_a", types: ["session.queue_changed"] }, () => {});
    await flush();
    await flush();
    expect(getIncompatiblePeers().has("pop-os")).toBe(true);
    offA();
    // pop-os is updated; session B opens and its head answers.
    old = false;
    cleanups.push(subscribeToEvents({ sessionId: "ses_b", types: ["session.queue_changed"] }, () => {}));
    await flush();
    await flush();
    expect(getIncompatiblePeers().has("pop-os")).toBe(false);
    expect(getSessionLive("ses_a").state).toBe("live");
    // A, opened again, starts over: its reads wait for a real head read.
    const before = headReads;
    let readyA = false;
    cleanups.push(subscribeToEvents({ sessionId: "ses_a", types: ["session.queue_changed"] }, () => {}));
    void eventsReady("ses_a").then(() => (readyA = true));
    await flush();
    await flush();
    expect(headReads).toBe(before + 1);
    expect(readyA).toBe(true);
    expect(getEventsCursorForTests("ses_a")).toBe(20);
  });

  it("a paused peer bus tries again on its own every few minutes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let reads = 0;
    stubHeads({
      "/peers/pop-os/events/head": async () => {
        reads++;
        return new Response(JSON.stringify({ code: "no_access" }), { status: 403 });
      },
    });
    vi.spyOn(console, "warn").mockImplementation(() => {});
    recordSessionHosts([remote("ses_r")]);
    cleanups.push(subscribeToEvents({ sessionId: "ses_r", types: ["session.queue_changed"] }, () => {}));
    await vi.advanceTimersByTimeAsync(10);
    expect(reads).toBe(1);
    await vi.advanceTimersByTimeAsync(4 * 60_000);
    expect(reads).toBe(1);
    await vi.advanceTimersByTimeAsync(60_000 + 10);
    expect(reads).toBe(2);
    vi.useRealTimers();
  });

  it("resumePeerBuses(peer) leaves other peers' paused buses alone", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const reads: Record<string, number> = { "pop-os": 0, spark: 0 };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        const m = /^\/peers\/([^/]+)\/events\/head$/.exec(new URL(input).pathname);
        if (m) reads[m[1]]++;
        return new Response(JSON.stringify({ code: "no_access" }), { status: 403 });
      }),
    );
    vi.spyOn(console, "warn").mockImplementation(() => {});
    recordSessionHosts([remote("ses_p"), { id: "ses_s", host: { name: "spark", self: false, reachable: true } } as Session]);
    cleanups.push(subscribeToEvents({ sessionId: "ses_p", types: ["session.queue_changed"] }, () => {}));
    cleanups.push(subscribeToEvents({ sessionId: "ses_s", types: ["session.queue_changed"] }, () => {}));
    await vi.advanceTimersByTimeAsync(10);
    resumePeerBuses("spark");
    await vi.advanceTimersByTimeAsync(10);
    expect(reads).toEqual({ "pop-os": 1, spark: 2 });
    vi.useRealTimers();
  });

  it("a peer.* event retries only that peer's paused buses", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const reads: Record<string, number> = { "pop-os": 0, spark: 0 };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        const path = new URL(input).pathname;
        if (path === "/events/head") return new Response(JSON.stringify({ seq: 1 }), { status: 200 });
        const m = /^\/peers\/([^/]+)\/events\/head$/.exec(path);
        if (m) {
          reads[m[1]]++;
          return new Response(JSON.stringify({ code: "no_access" }), { status: 403 });
        }
        return new Response("[]", { status: 200 });
      }),
    );
    vi.spyOn(console, "warn").mockImplementation(() => {});
    cleanups.push(subscribeMesh(() => {}));
    recordSessionHosts([remote("ses_p"), { id: "ses_s", host: { name: "spark", self: false, reachable: true } } as Session]);
    cleanups.push(subscribeToEvents({ sessionId: "ses_p", types: ["session.queue_changed"] }, () => {}));
    cleanups.push(subscribeToEvents({ sessionId: "ses_s", types: ["session.queue_changed"] }, () => {}));
    await vi.advanceTimersByTimeAsync(10);
    sources("/events")[0].emit("peer.updated", JSON.stringify({ ...envelope(2, "peer.updated", ""), payload: { id: "per_s", name: "spark" } }));
    await vi.advanceTimersByTimeAsync(10);
    expect(reads).toEqual({ "pop-os": 1, spark: 2 });
    vi.useRealTimers();
  });
});
