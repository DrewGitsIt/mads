import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { subscribeToEvents } from "./events";
import {
  clientForHost,
  getMeshSnapshot,
  meshShowsAll,
  recordSessionHosts,
  meshApi,
  refreshPeers,
  resetMeshForTests,
  setMeshScope,
  subscribeMesh,
} from "./mesh";
import { router } from "./client";
import { getSessionsSnapshot, resetSessionsStoreForTests, subscribeSessions } from "./sessionsStore";
import { FakeEventSource } from "./testFakeEventSource";
import type { Envelope, PeerStatus, Session } from "./types";

const popOs: PeerStatus = {
  id: "per_1",
  name: "pop-os",
  mesh_url: "http://pop-os.ts.net:17380",
  created_at: "2026-11-12T00:00:00Z",
  updated_at: "2026-11-12T00:00:00Z",
  reachability: "connected",
};

function envelope(seq: number, type: string, sessionId: string): Envelope {
  return { seq, timestamp: "2026-11-12T00:00:00Z", type, actor: "router", session_id: sessionId };
}

function remote(id: string, reachable = true): Session {
  return { id, host: { name: "pop-os", self: false, peer_id: "per_1", reachable } } as Session;
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("mesh mode", () => {
  const cleanups: (() => void)[] = [];

  beforeEach(() => {
    localStorage.clear();
    resetMeshForTests();
    resetSessionsStoreForTests();
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    for (const c of cleanups.splice(0)) c();
    resetSessionsStoreForTests();
    resetMeshForTests();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("starts on This device, and the choice survives a reload", () => {
    expect(getMeshSnapshot().scope).toBe("self");
    setMeshScope("all");
    expect(localStorage.getItem("mads.mesh.scope")).toBe("all");
    resetMeshForTests();
    expect(getMeshSnapshot().scope).toBe("all");
  });

  it("All devices only means anything with a visible peer", () => {
    const snap = { peers: [], scope: "all" as const, loaded: true };
    expect(meshShowsAll(snap)).toBe(false);
    expect(meshShowsAll({ ...snap, peers: [{ ...popOs, hidden: true }] })).toBe(false);
    expect(meshShowsAll({ ...snap, peers: [popOs] })).toBe(true);
    expect(meshShowsAll({ ...snap, scope: "self", peers: [popOs] })).toBe(false);
  });

  it("a Router without /peers just means no devices", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("404 page not found", { status: 404 })));
    await refreshPeers();
    expect(getMeshSnapshot()).toMatchObject({ peers: [], loaded: true });
  });

  it("reads per-host data through the peer, and this device's through the router", () => {
    expect(clientForHost(undefined)).toBe(router);
    expect(clientForHost({ name: "mac", self: true, reachable: true })).toBe(router);
    const peer = clientForHost({ name: "pop-os", self: false, reachable: true });
    expect(peer).not.toBe(router);
    expect(clientForHost({ name: "pop-os", self: false, reachable: false })).toBe(peer);
  });

  it("a remote session's subscription gets its own server-filtered peer stream", () => {
    recordSessionHosts([remote("ses_remote")]);
    const got: Envelope[] = [];
    cleanups.push(subscribeToEvents({ sessionId: "ses_remote", types: ["session.queue_changed"] }, (e) => got.push(e)));
    cleanups.push(subscribeToEvents({ types: ["session.created"] }, () => {}));

    const urls = FakeEventSource.instances.map((s) => new URL(s.url));
    const peer = urls.find((u) => u.pathname.startsWith("/peers/"));
    const local = urls.find((u) => u.pathname === "/events");
    expect(peer?.pathname).toBe("/peers/pop-os/events");
    // The session filter is what keeps a peer stream from replaying the
    // peer's whole log over the tailnet.
    expect(peer?.searchParams.get("session_id")).toBe("ses_remote");
    expect(peer?.searchParams.get("types")).toBe("session.queue_changed");
    expect(local?.searchParams.get("types")).toBe("session.created");

    const peerSource = FakeEventSource.instances.find((s) => s.url.includes("/peers/"))!;
    peerSource.emit("session.queue_changed", JSON.stringify(envelope(9, "session.queue_changed", "ses_remote")));
    expect(got.map((e) => e.seq)).toEqual([9]);
  });

  it("a subscription made before its host was known moves to the peer stream", () => {
    const got: number[] = [];
    cleanups.push(subscribeToEvents({ sessionId: "ses_late", types: ["session.queue_changed"] }, (e) => got.push(e.seq)));
    const first = FakeEventSource.instances[0];
    expect(new URL(first.url).pathname).toBe("/events");

    recordSessionHosts([remote("ses_late")]);
    expect(first.closed).toBe(true); // the local bus had only this subscriber
    const moved = FakeEventSource.instances.at(-1)!;
    expect(new URL(moved.url).pathname).toBe("/peers/pop-os/events");
    moved.emit("session.queue_changed", JSON.stringify(envelope(3, "session.queue_changed", "ses_late")));
    expect(got).toEqual([3]);
  });

  it("one device is the merged list narrowed to that host", async () => {
    vi.spyOn(router, "listSessions").mockResolvedValue([
      { id: "ses_local", host: { name: "mac", self: true, reachable: true } } as Session,
      remote("ses_remote"),
    ]);
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify([popOs]), { status: 200 })));
    setMeshScope("peer:pop-os");
    cleanups.push(subscribeMesh(() => {}));
    cleanups.push(subscribeSessions(() => {}));
    await flush();
    await flush();
    await flush();
    expect(getSessionsSnapshot().sessions.map((s) => s.id)).toEqual(["ses_remote"]);
  });

  it("the session list asks for scope=all only on All devices with a peer, and learns hosts", async () => {
    const list = vi.spyOn(router, "listSessions").mockResolvedValue([remote("ses_remote")]);
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify([popOs]), { status: 200 })),
    );
    cleanups.push(subscribeMesh(() => {}));
    cleanups.push(subscribeSessions(() => {}));
    await flush();
    await flush();
    expect(list).toHaveBeenCalledWith(undefined); // This device: unchanged

    setMeshScope("all");
    await flush();
    expect(list).toHaveBeenLastCalledWith({ scope: "all" });
    await flush();
    expect(getSessionsSnapshot().sessions.map((s) => s.id)).toEqual(["ses_remote"]);

    // The store recorded the host, so the bus routes this session's events.
    cleanups.push(subscribeToEvents({ sessionId: "ses_remote", types: ["session.queue_changed"] }, () => {}));
    expect(FakeEventSource.instances.some((s) => s.url.includes("/peers/pop-os/events"))).toBe(true);
  });

  // The local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md H7): the renderer
  // never holds the install token. The peer API's calls go to the Router's
  // own origin with no credential, and Electron's main process (or a
  // Playwright context's extraHTTPHeaders) adds the bearer below the page.
  it("sends the peer API to the Router with no credential of its own", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify([popOs]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await meshApi.listPeers();
    await meshApi.addPeer({ dns_name: "pop-os.ts.net" });
    await meshApi.updatePeer("pop-os", { hidden: true });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    for (const [url, init] of fetchMock.mock.calls) {
      expect(url).toMatch(/\/peers/);
      const headers = new Headers(init?.headers);
      expect(headers.has("Authorization")).toBe(false);
      expect(init?.credentials).toBeUndefined();
    }
  });
});
